//go:build unix

package experiments

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

var killForkProcessGroup = syscall.Kill

type unixForkProcessController struct {
	cmd  *exec.Cmd
	once sync.Once
	err  error
}

func newForkProcessController(cmd *exec.Cmd) (forkProcessController, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &unixForkProcessController{cmd: cmd}, nil
}

func (controller *unixForkProcessController) AfterStart() error { return nil }
func (controller *unixForkProcessController) Cancel() error     { return controller.terminate() }
func (controller *unixForkProcessController) Close() error      { return controller.terminate() }

func (controller *unixForkProcessController) terminate() error {
	controller.once.Do(func() {
		if controller.cmd.Process == nil {
			return
		}
		controller.err = killForkProcessGroup(-controller.cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(controller.err, syscall.ESRCH) || errors.Is(controller.err, os.ErrProcessDone) {
			controller.err = nil
		}
	})
	return controller.err
}
