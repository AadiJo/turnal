//go:build unix

package compatibility

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func prepareProcessTree(command *exec.Cmd) {
	attributes := syscall.SysProcAttr{}
	if command.SysProcAttr != nil {
		attributes = *command.SysProcAttr
	}
	attributes.Setpgid = true
	command.SysProcAttr = &attributes
}

type appServerProcessTree struct{}

func attachProcessTree(_ *exec.Cmd) (*appServerProcessTree, error) {
	return &appServerProcessTree{}, nil
}

func releaseProcessTree(_ *appServerProcessTree) {}

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
