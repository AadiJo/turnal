//go:build windows

package adapterplugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverIgnoresWindowsCommandShims(t *testing.T) {
	bin := t.TempDir()
	for _, filename := range []string{
		"turnal-adapter-opencode.exe",
		"turnal-adapter-opencode.cmd",
		"turnal-adapter-opencode.ps1",
	} {
		if err := os.WriteFile(filepath.Join(bin, filename), nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)

	discovered, err := Discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(discovered) != 1 {
		t.Fatalf("discovered %d adapters, want 1: %#v", len(discovered), discovered)
	}
	if discovered[0].Name != "opencode" {
		t.Fatalf("adapter name = %q, want %q", discovered[0].Name, "opencode")
	}
	if discovered[0].Path != filepath.Join(bin, "turnal-adapter-opencode.exe") {
		t.Fatalf("adapter path = %q, want native executable", discovered[0].Path)
	}
}

func TestFindIgnoresEarlierWindowsCommandShim(t *testing.T) {
	shimDir := t.TempDir()
	nativeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(shimDir, "turnal-adapter-opencode.cmd"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	nativePath := filepath.Join(nativeDir, "turnal-adapter-opencode.exe")
	if err := os.WriteFile(nativePath, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+nativeDir)

	adapter, err := Find("opencode")
	if err != nil {
		t.Fatal(err)
	}
	if adapter.Path != nativePath {
		t.Fatalf("adapter path = %q, want %q", adapter.Path, nativePath)
	}
}
