//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package experiments

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

func waitForForkProcessExit(pid int) error {
	queue, err := unix.Kqueue()
	if err != nil {
		return err
	}
	defer unix.Close(queue)

	changes := []unix.Kevent_t{{
		Ident:  uint64(pid),
		Filter: unix.EVFILT_PROC,
		Flags:  unix.EV_ADD | unix.EV_ONESHOT,
		Fflags: unix.NOTE_EXIT,
	}}
	events := make([]unix.Kevent_t, 1)
	for {
		count, err := unix.Kevent(queue, changes, events, nil)
		changes = nil
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if count == 1 {
			return nil
		}
	}
}
