//go:build aix || illumos || solaris

package experiments

import (
	"fmt"
	"os/exec"
	"runtime"
)

func newForkProcessController(_ *exec.Cmd) (forkProcessController, error) {
	return nil, fmt.Errorf("fork process containment is unsupported on %s", runtime.GOOS)
}
