//go:build unix

package compatibility

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func prepareProcessTree(command *exec.Cmd) (*appServerProcessTree, error) {
	attributes := syscall.SysProcAttr{}
	if command.SysProcAttr != nil {
		attributes = *command.SysProcAttr
	}
	attributes.Setpgid = true
	command.SysProcAttr = &attributes
	return &appServerProcessTree{command: command}, nil
}

type appServerProcessTree struct {
	command *exec.Cmd
}

func attachProcessTree(_ *appServerProcessTree, _ *exec.Cmd) error { return nil }

func releaseProcessTree(processTree *appServerProcessTree) {
	if processTree == nil || processTree.command == nil || processTree.command.Process == nil {
		return
	}
	err := syscall.Kill(-processTree.command.Process.Pid, syscall.SIGKILL)
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		_ = processTree.command.Process.Kill()
	}
}

func killProcessTree(_ *appServerProcessTree, command *exec.Cmd, _ time.Duration) error {
	if command.Process == nil {
		return nil
	}
	err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if directErr := command.Process.Kill(); directErr != nil && !errors.Is(directErr, os.ErrProcessDone) {
		return errors.Join(err, directErr)
	}
	return nil
}
