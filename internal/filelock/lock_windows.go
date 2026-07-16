//go:build windows

package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// Windows byte-range locks are mandatory: locking byte 0 would make the owner
// identity JSON at the start of the file unreadable by Inspect while the lock
// is held. Lock a single byte far past any offset the file will ever reach
// instead (locking beyond EOF is permitted).
const (
	lockByteOffsetLow  = 0xFFFFFFFE
	lockByteOffsetHigh = 0x7FFFFFFF
)

func tryLock(file *os.File) (bool, error) {
	overlapped := windows.Overlapped{Offset: lockByteOffsetLow, OffsetHigh: lockByteOffsetHigh}
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&overlapped,
	)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return false, err
}

func unlock(file *os.File) error {
	overlapped := windows.Overlapped{Offset: lockByteOffsetLow, OffsetHigh: lockByteOffsetHigh}
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
}
