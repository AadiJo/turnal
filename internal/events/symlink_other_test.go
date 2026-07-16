//go:build !windows

package events

import (
	"os"
	"testing"
)

func createTestSymlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("symlink: %v", err)
	}
}
