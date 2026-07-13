package recall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/primitives"
)

func TestValidateTranscriptPathAcceptsFilesystemAliasOfAllowedRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "codex-home")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create root: %v", err)
	}
	alias := filepath.Join(parent, "codex-home-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	path := filepath.Join(root, "sessions", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create transcript dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	t.Setenv("CODEX_HOME", alias)

	resolved, err := validateTranscriptPath(path, primitives.AdapterCodex)
	if err != nil {
		t.Fatalf("validate aliased transcript root: %v", err)
	}
	if resolved == "" {
		t.Fatal("resolved transcript path is empty")
	}
}

func TestValidateTranscriptPathRejectsMetadataAndSymlinkEscape(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "codex-home")
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(filepath.Join(root, ".GIT"), 0o700); err != nil {
		t.Fatalf("create metadata dir: %v", err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatalf("create outside dir: %v", err)
	}
	metadataPath := filepath.Join(root, ".GIT", "transcript.jsonl")
	outsidePath := filepath.Join(outside, "transcript.jsonl")
	for _, path := range []string{metadataPath, outsidePath} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	t.Setenv("CODEX_HOME", root)

	if _, err := validateTranscriptPath(metadataPath, primitives.AdapterCodex); err == nil || !strings.Contains(err.Error(), ".git") {
		t.Fatalf("metadata transcript error = %v, want .git rejection", err)
	}
	escape := filepath.Join(root, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := validateTranscriptPath(filepath.Join(escape, "transcript.jsonl"), primitives.AdapterCodex); err == nil || !strings.Contains(err.Error(), "outside allowed") {
		t.Fatalf("symlink escape error = %v, want outside-root rejection", err)
	}
}
