//go:build windows

package events

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func createTestSymlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			t.Skip("Windows symlink privilege is unavailable; symlink rejection remains covered on other platforms")
		}
		t.Fatalf("symlink: %v", err)
	}
}
