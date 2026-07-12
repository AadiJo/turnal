//go:build !unix && !windows

package compatibility

import (
	"errors"
	"os"
	"os/exec"
	"time"
)

func prepareProcessTree(_ *exec.Cmd) {}

type appServerProcessTree struct{}

func attachProcessTree(_ *exec.Cmd) (*appServerProcessTree, error) {
	return &appServerProcessTree{}, nil
}

func releaseProcessTree(_ *appServerProcessTree) {}

func killProcessTree(_ *appServerProcessTree, command *exec.Cmd, _ time.Duration) error {
	if command.Process == nil {
		return nil
	}
	if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
