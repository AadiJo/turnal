//go:build unix

package cli

import (
	"os"
	"syscall"
	"unsafe"
)

type terminalWindowSize struct {
	Rows    uint16
	Cols    uint16
	XPixels uint16
	YPixels uint16
}

func terminalSize(file *os.File) (height int, width int, ok bool) {
	var size terminalWindowSize
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&size)))
	if errno != 0 || size.Rows == 0 {
		return 0, 0, false
	}
	return int(size.Rows), int(size.Cols), true
}
