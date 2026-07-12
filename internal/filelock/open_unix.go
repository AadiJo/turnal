//go:build unix

package filelock

import (
	"os"

	"golang.org/x/sys/unix"
)

func openLockFileNoFollow(path string, create bool) (*os.File, error) {
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
	if create {
		flags |= unix.O_CREAT
	}
	fd, err := unix.Open(path, flags, 0o600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func isRegularLockFile(file *os.File) (bool, error) {
	var info unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &info); err != nil {
		return false, err
	}
	return info.Mode&unix.S_IFMT == unix.S_IFREG && info.Nlink == 1, nil
}
