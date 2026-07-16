//go:build aix || dragonfly || freebsd || illumos || netbsd || openbsd || solaris

package experiments

import (
	"fmt"
	"os/exec"
	"runtime"
)

func newForkProcessController(_ *exec.Cmd) (forkProcessController, error) {
	return nil, fmt.Errorf("fork process containment is unsupported on %s", runtime.GOOS)
}
