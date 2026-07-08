//go:build !unix

package cli

import "os"

func terminalSize(file *os.File) (height int, width int, ok bool) {
	return 0, 0, false
}
