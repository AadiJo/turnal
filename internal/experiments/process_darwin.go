//go:build darwin

package experiments

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

const darwinForkGateArgument = "__turnal_internal_fork_gate_7d69d1be__"

var killDarwinForkProcessGroup = syscall.Kill

func init() {
	if len(os.Args) < 5 || os.Args[1] != darwinForkGateArgument {
		return
	}
	gateFD, err := strconv.Atoi(os.Args[2])
	if err != nil || gateFD < 3 {
		fmt.Fprintln(os.Stderr, "turnal: invalid internal fork gate")
		os.Exit(127)
	}
	gate := os.NewFile(uintptr(gateFD), "turnal-fork-gate")
	if gate == nil {
		fmt.Fprintln(os.Stderr, "turnal: open internal fork gate")
		os.Exit(127)
	}
	_, readErr := gate.Read(make([]byte, 1))
	closeErr := gate.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		fmt.Fprintf(os.Stderr, "turnal: wait for internal fork gate: %v\n", readErr)
		os.Exit(127)
	}
	if closeErr != nil {
		fmt.Fprintf(os.Stderr, "turnal: close internal fork gate: %v\n", closeErr)
		os.Exit(127)
	}
	originalPath := os.Args[3]
	originalArgs := append([]string{os.Args[4]}, os.Args[5:]...)
	if err := syscall.Exec(originalPath, originalArgs, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "turnal: execute gated fork command: %v\n", err)
		os.Exit(127)
	}
}

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
	helperPath, err := os.Executable()
	if err != nil {
		_ = gateRead.Close()
		_ = gateWrite.Close()
		return nil, fmt.Errorf("resolve fork gate helper: %w", err)
	}
	gateFD := 3 + len(cmd.ExtraFiles)
	originalPath := cmd.Path
	originalArgs := append([]string(nil), cmd.Args...)
	cmd.Path = helperPath
	cmd.Args = append([]string{helperPath, darwinForkGateArgument, strconv.Itoa(gateFD), originalPath, originalArgs[0]}, originalArgs[1:]...)
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
