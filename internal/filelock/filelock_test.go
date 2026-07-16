package filelock

import (
	"errors"
	"os"
	"path/filepath"
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

func TestInspectReadsOwnerWhileLockIsHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inspect.lock")
	lock, err := Acquire(path, time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	owner, held, err := Inspect(path)
	if err != nil {
		t.Fatalf("Inspect while held: %v", err)
	}
	if !held {
		t.Fatal("Inspect held=false while lock is acquired")
	}
	if owner != lock.Identity() {
		t.Fatalf("Inspect owner = %+v, want %+v", owner, lock.Identity())
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

func TestAcquireRefusesSymlinkWithoutChangingTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sentinel")
	want := []byte("must remain unchanged\n")
	if err := os.WriteFile(target, want, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "crafted.lock")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Acquire(path, time.Second); err == nil {
		t.Fatal("Acquire accepted a symlink lock path")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("symlink target changed to %q", got)
	}
}

func TestAcquireRefusesHardLinkWithoutChangingTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sentinel")
	want := []byte("must remain unchanged\n")
	if err := os.WriteFile(target, want, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "crafted.lock")
	if err := os.Link(target, path); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if _, err := Acquire(path, time.Second); err == nil {
		t.Fatal("Acquire accepted a multiply linked lock file")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("hard-link target changed to %q", got)
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
