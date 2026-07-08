//go:build windows

package checkpoint

import (
	"errors"
	"syscall"
)

func isDirectoryNotEmpty(err error) bool {
	return errors.Is(err, syscall.Errno(145)) || errors.Is(err, syscall.EEXIST)
}
