//go:build aix || illumos || solaris

package experiments

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

type unsupportedUnixForkProcessController struct {
	cmd  *exec.Cmd
	once sync.Once
	err  error
}

func newForkProcessController(cmd *exec.Cmd) (forkProcessController, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &unsupportedUnixForkProcessController{cmd: cmd}, nil
}

func (controller *unsupportedUnixForkProcessController) AfterStart() error { return nil }
func (controller *unsupportedUnixForkProcessController) WaitMain() error {
	return errForkWaitMainUnsupported
}
func (controller *unsupportedUnixForkProcessController) Cancel() error {
	controller.once.Do(func() {
		if controller.cmd.Process == nil {
			return
		}
		controller.err = syscall.Kill(-controller.cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(controller.err, syscall.ESRCH) || errors.Is(controller.err, os.ErrProcessDone) {
			controller.err = nil
		}
	})
	return controller.err
}

// After the leader has been reaped its process-group ID can be reused, so
// Close intentionally does not signal on platforms that cannot observe exit
// without reaping. Context cancellation still contains the live process group.
func (controller *unsupportedUnixForkProcessController) Close() error { return nil }
