//go:build unix

package verifier

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

type unixProcessController struct {
	cmd *exec.Cmd
}

func newProcessController(cmd *exec.Cmd) (processController, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &unixProcessController{cmd: cmd}, nil
}

func (controller *unixProcessController) AfterStart() error { return nil }

func (controller *unixProcessController) Cancel() error {
	if controller.cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-controller.cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func (controller *unixProcessController) Close() error { return nil }
