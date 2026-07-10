package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLockReportsHeldAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")
	lock, err := Acquire(path, time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	held, err := Held(path)
	if err != nil {
		t.Fatalf("Held: %v", err)
	}
	if !held {
		t.Fatal("Held=false while lock is acquired")
	}
	if _, err := Acquire(path, 20*time.Millisecond); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Acquire error = %v, want ErrBusy", err)
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	held, err = Held(path)
	if err != nil {
		t.Fatalf("Held after release: %v", err)
	}
	if held {
		t.Fatal("Held=true after release")
	}
}

func TestAcquireRefusesLegacyLockDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.lock")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("mkdir legacy lock: %v", err)
	}
	if _, err := Acquire(path, time.Second); err == nil || !strings.Contains(err.Error(), "ensure no older Turnal process") {
		t.Fatalf("Acquire legacy lock error = %v", err)
	}
}

func TestHeldDoesNotRewriteOwnerMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.lock")
	lock, err := Acquire(path, time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read owner before Held: %v", err)
	}
	held, err := Held(path)
	if err != nil || held {
		t.Fatalf("Held = %v, %v", held, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read owner after Held: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("Held rewrote owner metadata: before=%q after=%q", before, after)
	}
}

func TestAcquireQuietDoesNotWriteOwnerMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quiet.lock")
	lock, err := AcquireQuiet(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("quiet lock wrote owner metadata: %q", data)
	}
}

func TestAcquireRefusesSymlinkWithoutTouchingTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on some Windows hosts")
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "lock")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(path, time.Second); err == nil {
		t.Fatal("lock symlink was accepted")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "unchanged" {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
}
