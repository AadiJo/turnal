//go:build darwin

package processidentity

import (
	"errors"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func startedAt(pid int) (string, error) {
	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) {
			return "", os.ErrNotExist
		}
		return "", err
	}
	if process == nil || int(process.Proc.P_pid) != pid || process.Proc.P_stat == 5 {
		return "", os.ErrNotExist
	}
	started := process.Proc.P_starttime
	return strconv.FormatInt(started.Sec, 10) + ":" + strconv.FormatInt(int64(started.Usec), 10), nil
}
