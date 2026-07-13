//go:build !unix && !windows

package verifier

import "os/exec"

type directProcessController struct {
	cmd *exec.Cmd
}

func newProcessController(cmd *exec.Cmd) (processController, error) {
	return &directProcessController{cmd: cmd}, nil
}

func (controller *directProcessController) AfterStart() error { return nil }

func (controller *directProcessController) Cancel() error {
	if controller.cmd.Process == nil {
		return nil
	}
	return controller.cmd.Process.Kill()
}

func (controller *directProcessController) Close() error { return nil }
