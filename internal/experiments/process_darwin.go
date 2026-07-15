//go:build darwin

package experiments

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

const darwinForkGateArgument = "__turnal_internal_fork_gate_7d69d1be__"

var killDarwinForkProcessGroup = syscall.Kill

func init() {
	if len(os.Args) < 6 || os.Args[1] != darwinForkGateArgument {
		return
	}
	gateFD, err := strconv.Atoi(os.Args[2])
	if err != nil || gateFD < 3 {
		fmt.Fprintln(os.Stderr, "turnal: invalid internal fork gate")
		os.Exit(127)
	}
	environmentFD, environmentErr := strconv.Atoi(os.Args[3])
	if environmentErr != nil || environmentFD < 3 || environmentFD == gateFD {
		fmt.Fprintln(os.Stderr, "turnal: invalid internal fork environment")
		os.Exit(127)
	}
	environmentFile := os.NewFile(uintptr(environmentFD), "turnal-fork-environment")
	if environmentFile == nil {
		fmt.Fprintln(os.Stderr, "turnal: open internal fork environment")
		os.Exit(127)
	}
	environmentBytes, readEnvironmentErr := io.ReadAll(environmentFile)
	closeEnvironmentErr := environmentFile.Close()
	if readEnvironmentErr != nil || closeEnvironmentErr != nil {
		fmt.Fprintf(os.Stderr, "turnal: read internal fork environment: %v\n", errors.Join(readEnvironmentErr, closeEnvironmentErr))
		os.Exit(127)
	}
	targetEnvironment, err := decodeDarwinForkEnvironment(environmentBytes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "turnal: decode internal fork environment: %v\n", err)
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
	originalPath := os.Args[4]
	originalArgs := append([]string{os.Args[5]}, os.Args[6:]...)
	if err := syscall.Exec(originalPath, originalArgs, targetEnvironment); err != nil {
		fmt.Fprintf(os.Stderr, "turnal: execute gated fork command: %v\n", err)
		os.Exit(127)
	}
}

type darwinForkProcessController struct {
	cmd               *exec.Cmd
	gateRead          *os.File
	gateWrite         *os.File
	environmentRead   *os.File
	environmentWrite  *os.File
	targetEnvironment []byte
	queue             int
	immediateExit     bool
	terminateOnce     sync.Once
	terminateErr      error
	closeOnce         sync.Once
	closeErr          error
}

func newForkProcessController(cmd *exec.Cmd) (forkProcessController, error) {
	gateRead, gateWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	environmentRead, environmentWrite, err := os.Pipe()
	if err != nil {
		_ = gateRead.Close()
		_ = gateWrite.Close()
		return nil, err
	}
	helperPath, err := os.Executable()
	if err != nil {
		_ = gateRead.Close()
		_ = gateWrite.Close()
		_ = environmentRead.Close()
		_ = environmentWrite.Close()
		return nil, fmt.Errorf("resolve fork gate helper: %w", err)
	}
	targetEnvironment, err := encodeDarwinForkEnvironment(cmd.Env)
	if err != nil {
		_ = gateRead.Close()
		_ = gateWrite.Close()
		_ = environmentRead.Close()
		_ = environmentWrite.Close()
		return nil, err
	}
	gateFD := 3 + len(cmd.ExtraFiles)
	environmentFD := gateFD + 1
	originalPath := cmd.Path
	originalArgs := append([]string(nil), cmd.Args...)
	cmd.Path = helperPath
	cmd.Args = append([]string{helperPath, darwinForkGateArgument, strconv.Itoa(gateFD), strconv.Itoa(environmentFD), originalPath, originalArgs[0]}, originalArgs[1:]...)
	cmd.Env = []string{}
	cmd.ExtraFiles = append(cmd.ExtraFiles, gateRead, environmentRead)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &darwinForkProcessController{
		cmd: cmd, gateRead: gateRead, gateWrite: gateWrite,
		environmentRead: environmentRead, environmentWrite: environmentWrite,
		targetEnvironment: targetEnvironment, queue: -1,
	}, nil
}

func encodeDarwinForkEnvironment(environment []string) ([]byte, error) {
	normalized := make([]string, 0, len(environment))
	seen := make(map[string]bool, len(environment))
	for index := len(environment) - 1; index >= 0; index-- {
		entry := environment[index]
		if strings.IndexByte(entry, 0) >= 0 {
			return nil, fmt.Errorf("fork environment variable contains NUL")
		}
		separator := strings.IndexByte(entry, '=')
		if separator == 0 {
			separator = strings.IndexByte(entry[1:], '=') + 1
		}
		if separator < 0 {
			if entry != "" {
				normalized = append(normalized, entry)
			}
			continue
		}
		name := entry[:separator]
		if seen[name] {
			continue
		}
		seen[name] = true
		normalized = append(normalized, entry)
	}
	for left, right := 0, len(normalized)-1; left < right; left, right = left+1, right-1 {
		normalized[left], normalized[right] = normalized[right], normalized[left]
	}
	var encoded []byte
	for _, entry := range normalized {
		encoded = append(encoded, entry...)
		encoded = append(encoded, 0)
	}
	return encoded, nil
}

func decodeDarwinForkEnvironment(encoded []byte) ([]string, error) {
	if len(encoded) == 0 {
		return []string{}, nil
	}
	if encoded[len(encoded)-1] != 0 {
		return nil, fmt.Errorf("fork environment is missing its terminator")
	}
	entries := strings.Split(string(encoded[:len(encoded)-1]), "\x00")
	for _, entry := range entries {
		if entry == "" {
			return nil, fmt.Errorf("fork environment contains an empty entry")
		}
	}
	return entries, nil
}

func (controller *darwinForkProcessController) AfterStart() error {
	if controller.cmd.Process == nil {
		return fmt.Errorf("fork process has not started")
	}
	if err := controller.gateRead.Close(); err != nil {
		return fmt.Errorf("close parent fork gate reader: %w", err)
	}
	controller.gateRead = nil
	if err := controller.environmentRead.Close(); err != nil {
		return fmt.Errorf("close parent fork environment reader: %w", err)
	}
	controller.environmentRead = nil
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
	if _, err := controller.environmentWrite.Write(controller.targetEnvironment); err != nil {
		return fmt.Errorf("send fork process environment: %w", err)
	}
	if err := controller.environmentWrite.Close(); err != nil {
		return fmt.Errorf("finish fork process environment: %w", err)
	}
	controller.environmentWrite = nil
	controller.targetEnvironment = nil
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
		if controller.environmentRead != nil {
			controller.closeErr = errors.Join(controller.closeErr, controller.environmentRead.Close())
			controller.environmentRead = nil
		}
		if controller.environmentWrite != nil {
			controller.closeErr = errors.Join(controller.closeErr, controller.environmentWrite.Close())
			controller.environmentWrite = nil
		}
		controller.targetEnvironment = nil
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
