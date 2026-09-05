//go:build !windows

package adapters

import "syscall"

const transcriptReadFlags = syscall.O_RDONLY | syscall.O_NONBLOCK
