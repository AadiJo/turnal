package fsidentity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSameAndWithinResolveFilesystemAliases(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatalf("create root: %v", err)
	}
	file := filepath.Join(root, "nested", "transcript.jsonl")
	if err := os.WriteFile(file, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if !Same(root, alias) {
		t.Fatalf("Same(%q, %q) = false", root, alias)
	}
	if !Within(filepath.Join(alias, "nested", "transcript.jsonl"), root) {
		t.Fatal("aliased child was not recognized within root")
	}
}

func TestWithinRejectsSymlinkEscapeAndSiblingPrefix(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("create outside: %v", err)
	}
	outsideFile := filepath.Join(outside, "transcript.jsonl")
	if err := os.WriteFile(outsideFile, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	escape := filepath.Join(root, "escape")
	if err := os.Symlink(outside, escape); err == nil {
		if Within(filepath.Join(escape, "transcript.jsonl"), root) {
			t.Fatal("symlink escape was accepted within root")
		}
	}
	if Within(filepath.Join(parent, "root-sibling", "transcript.jsonl"), root) {
		t.Fatal("sibling prefix was accepted within root")
	}
}
