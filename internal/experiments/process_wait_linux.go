//go:build linux

package experiments

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

func waitForForkProcessExit(pid int) error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		return err
	}
}
