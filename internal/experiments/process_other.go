//go:build !unix && !windows

package experiments

import "os/exec"

type directForkProcessController struct{ cmd *exec.Cmd }

func newForkProcessController(cmd *exec.Cmd) (forkProcessController, error) {
	return &directForkProcessController{cmd: cmd}, nil
}
func (controller *directForkProcessController) AfterStart() error { return nil }
func (controller *directForkProcessController) Cancel() error {
	if controller.cmd.Process == nil {
		return nil
	}
	return controller.cmd.Process.Kill()
}
func (controller *directForkProcessController) Close() error { return nil }
