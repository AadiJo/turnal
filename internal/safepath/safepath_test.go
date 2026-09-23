package safepath

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMkdirAllNoSymlinksCreatesNestedDirectories(t *testing.T) {
	root := t.TempDir()
	if err := MkdirAllNoSymlinks(root, filepath.Join("one", "two"), 0o755); err != nil {
		t.Fatalf("MkdirAllNoSymlinks: %v", err)
	}
	info, err := os.Lstat(filepath.Join(root, "one", "two"))
	if err != nil || !info.IsDir() {
		t.Fatalf("nested directory info=%v err=%v", info, err)
	}
}

func TestMkdirAllNoSymlinksAllowsConcurrentCreation(t *testing.T) {
	root := t.TempDir()
	var wait sync.WaitGroup
	errors := make(chan error, 20)
	for range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errors <- MkdirAllNoSymlinks(root, filepath.Join("one", "two"), 0o755)
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("MkdirAllNoSymlinks: %v", err)
		}
	}
}

func TestWorkspaceOwnedPathsRejectSymlinks(t *testing.T) {
	for _, test := range []struct {
		name     string
		relative string
		create   bool
	}{
		{name: "intermediate", relative: filepath.Join("link", "child")},
		{name: "final", relative: "link"},
		{name: "mkdir", relative: filepath.Join("link", "child"), create: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Symlink(t.TempDir(), filepath.Join(root, "link")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			var err error
			if test.create {
				err = MkdirAllNoSymlinks(root, test.relative, 0o755)
			} else {
				err = ValidateNoSymlinks(root, test.relative)
			}
			if err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("error = %v, want symlink refusal", err)
			}
		})
	}
}

func TestWorkspaceOwnedPathsRejectParentTraversal(t *testing.T) {
	root := t.TempDir()
	if err := ValidateNoSymlinks(root, filepath.Join("..", "outside")); err == nil {
		t.Fatal("ValidateNoSymlinks accepted parent traversal")
	}
}
