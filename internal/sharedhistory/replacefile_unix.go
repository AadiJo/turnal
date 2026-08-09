//go:build !windows

package sharedhistory

import "os"

func replaceFile(source, target string) error {
	return os.Rename(source, target)
}
