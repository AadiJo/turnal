//go:build darwin

package experiments

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

var killDarwinForkProcessGroup = syscall.Kill

type darwinForkProcessController struct {
	cmd           *exec.Cmd
	gateRead      *os.File
	gateWrite     *os.File
	queue         int
	immediateExit bool
	terminateOnce sync.Once
	terminateErr  error
	closeOnce     sync.Once
	closeErr      error
}

func newForkProcessController(cmd *exec.Cmd) (forkProcessController, error) {
	gateRead, gateWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	gateFD := 3 + len(cmd.ExtraFiles)
	originalPath := cmd.Path
	originalArgs := append([]string(nil), cmd.Args...)
	gateScript := fmt.Sprintf(`IFS= read -r _ <&%d; exec %d<&-; exec -a "$1" "$2" "${@:3}"`, gateFD, gateFD)
	cmd.Path = "/bin/bash"
	cmd.Args = append([]string{"bash", "--noprofile", "--norc", "-c", gateScript, "turnal-fork-gate", originalArgs[0], originalPath}, originalArgs[1:]...)
	cmd.ExtraFiles = append(cmd.ExtraFiles, gateRead)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &darwinForkProcessController{cmd: cmd, gateRead: gateRead, gateWrite: gateWrite, queue: -1}, nil
}

func (controller *darwinForkProcessController) AfterStart() error {
	if controller.cmd.Process == nil {
		return fmt.Errorf("fork process has not started")
	}
	if err := controller.gateRead.Close(); err != nil {
		return fmt.Errorf("close parent fork gate reader: %w", err)
	}
	controller.gateRead = nil
	queue, err := unix.Kqueue()
	if err != nil {
		return err
	}
	controller.queue = queue
	immediateExit, err := registerDarwinForkExit(queue, controller.cmd.Process.Pid)
	if err != nil {
		return err
	}
	controller.immediateExit = immediateExit
	if err := controller.gateWrite.Close(); err != nil {
		return fmt.Errorf("release fork process gate: %w", err)
	}
	controller.gateWrite = nil
	return nil
}

func (controller *darwinForkProcessController) WaitMain() error {
	if controller.immediateExit {
		return nil
	}
	if controller.queue < 0 || controller.cmd.Process == nil {
		return fmt.Errorf("fork process exit observation is not initialized")
	}
	events := make([]unix.Kevent_t, 1)
	for {
		count, err := unix.Kevent(controller.queue, nil, events, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if count != 1 {
			continue
		}
		exited, err := validateDarwinForkExit(events[0], controller.cmd.Process.Pid)
		if err != nil {
			return err
		}
		if exited {
			return nil
		}
	}
}

func (controller *darwinForkProcessController) Cancel() error {
	return controller.terminate()
}

func (controller *darwinForkProcessController) Close() error {
	terminateErr := controller.terminate()
	controller.closeOnce.Do(func() {
		if controller.gateRead != nil {
			controller.closeErr = errors.Join(controller.closeErr, controller.gateRead.Close())
			controller.gateRead = nil
		}
		if controller.gateWrite != nil {
			controller.closeErr = errors.Join(controller.closeErr, controller.gateWrite.Close())
			controller.gateWrite = nil
		}
		if controller.queue >= 0 {
			controller.closeErr = errors.Join(controller.closeErr, unix.Close(controller.queue))
			controller.queue = -1
		}
	})
	return errors.Join(terminateErr, controller.closeErr)
}

func (controller *darwinForkProcessController) terminate() error {
	controller.terminateOnce.Do(func() {
		if controller.cmd.Process == nil {
			return
		}
		controller.terminateErr = killDarwinForkProcessGroup(-controller.cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(controller.terminateErr, syscall.ESRCH) || errors.Is(controller.terminateErr, os.ErrProcessDone) {
			controller.terminateErr = nil
		}
	})
	return controller.terminateErr
}

func registerDarwinForkExit(queue, pid int) (bool, error) {
	changes := []unix.Kevent_t{{
		Ident:  uint64(pid),
		Filter: unix.EVFILT_PROC,
		Flags:  unix.EV_ADD | unix.EV_ONESHOT,
		Fflags: unix.NOTE_EXIT,
	}}
	events := make([]unix.Kevent_t, 1)
	timeout := unix.Timespec{}
	for {
		count, err := unix.Kevent(queue, changes, events, &timeout)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return false, err
		}
		if count == 0 {
			return false, nil
		}
		return validateDarwinForkExit(events[0], pid)
	}
}

func validateDarwinForkExit(event unix.Kevent_t, pid int) (bool, error) {
	if event.Flags&unix.EV_ERROR != 0 {
		if event.Data == 0 {
			return false, nil
		}
		return false, syscall.Errno(event.Data)
	}
	if event.Ident != uint64(pid) || event.Filter != unix.EVFILT_PROC || event.Fflags&unix.NOTE_EXIT == 0 {
		return false, fmt.Errorf("unexpected fork process exit notification")
	}
	return true, nil
}
