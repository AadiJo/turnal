//go:build !windows

package checkpoint

import (
	"errors"
	"syscall"
)

func isDirectoryNotEmpty(err error) bool {
	return errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST)
}
