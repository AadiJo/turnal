//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package experiments

import (
	"errors"
	"fmt"
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
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		changes = nil
		if count != 1 {
			continue
		}
		event := events[0]
		if event.Flags&unix.EV_ERROR != 0 {
			if event.Data == 0 {
				return fmt.Errorf("register fork process exit notification")
			}
			return syscall.Errno(event.Data)
		}
		if event.Ident == uint64(pid) && event.Filter == unix.EVFILT_PROC && event.Fflags&unix.NOTE_EXIT != 0 {
			return nil
		}
	}
}
