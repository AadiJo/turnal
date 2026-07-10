//go:build windows

package telemetry

import "golang.org/x/sys/windows"

func replaceFile(oldPath, newPath string) error {
	return windows.Rename(oldPath, newPath)
}

func syncDirectory(string) error {
	return nil
}
