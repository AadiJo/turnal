//go:build !windows

package upgrade

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestCopyExecutableOverridesRestrictiveUmask(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(source, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "destination")
	previous := syscall.Umask(0o077)
	defer syscall.Umask(previous)

	if err := copyExecutable(source, destination); err != nil {
		t.Fatalf("copyExecutable: %v", err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("mode = %#o, want 0755", got)
	}
}

func TestExtractStandaloneArchiveOverridesRestrictiveUmask(t *testing.T) {
	files := make(map[string][]byte, len(standaloneExecutables))
	for _, name := range standaloneExecutables {
		files[name] = []byte("payload-" + name)
	}
	archive := standaloneTestArchive(t, files)
	destination := t.TempDir()
	previous := syscall.Umask(0o077)
	defer syscall.Umask(previous)

	if err := extractStandaloneArchive(archive, destination); err != nil {
		t.Fatalf("extractStandaloneArchive: %v", err)
	}
	for _, name := range standaloneExecutables {
		info, err := os.Stat(filepath.Join(destination, name))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o755 {
			t.Fatalf("%s mode = %#o, want 0755", name, got)
		}
	}
}

func TestResolveInstallDirFollowsExecutableSymlink(t *testing.T) {
	realDir := t.TempDir()
	executable := filepath.Join(realDir, "turnal")
	if err := os.WriteFile(executable, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "turnal")
	if err := os.Symlink(executable, link); err != nil {
		t.Fatal(err)
	}

	got, err := resolveInstallDir(StandaloneInstallOptions{ExecutablePath: link})
	if err != nil {
		t.Fatalf("resolveInstallDir: %v", err)
	}
	want, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("install dir = %q, want %q", got, want)
	}
}
