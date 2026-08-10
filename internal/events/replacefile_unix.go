//go:build !windows

package events

import "os"

func replaceEventFile(source, target string) error {
	return os.Rename(source, target)
}
