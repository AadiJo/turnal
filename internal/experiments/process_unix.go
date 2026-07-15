//go:build unix

package experiments

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

type unixForkProcessController struct{ cmd *exec.Cmd }

func newForkProcessController(cmd *exec.Cmd) (forkProcessController, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &unixForkProcessController{cmd: cmd}, nil
}

func (controller *unixForkProcessController) AfterStart() error { return nil }
func (controller *unixForkProcessController) Cancel() error {
	if controller.cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-controller.cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
func (controller *unixForkProcessController) Close() error {
	if controller.cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-controller.cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
