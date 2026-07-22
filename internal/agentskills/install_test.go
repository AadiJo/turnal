package agentskills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallLinksBundledSkillsForSelectedAgents(t *testing.T) {
	root := t.TempDir()
	metadataDir := filepath.Join(root, ".turnal")

	results, err := Install(root, metadataDir, []string{"codex", "claude"})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v, want two agent directories", results)
	}

	for _, destinationRoot := range []string{
		filepath.Join(root, ".agents", "skills"),
		filepath.Join(root, ".claude", "skills"),
	} {
		for _, skill := range bundledSkillNames() {
			destination := filepath.Join(destinationRoot, skill)
			if _, err := os.Readlink(destination); err != nil {
				t.Fatalf("%s is not a directory link: %v", destination, err)
			}
			data, err := os.ReadFile(filepath.Join(destination, "SKILL.md"))
			if err != nil {
				t.Fatalf("read linked SKILL.md: %v", err)
			}
			if !strings.Contains(string(data), "name: "+skill) {
				t.Fatalf("linked %s contains the wrong skill", destination)
			}
		}
	}

	if _, err := Install(root, metadataDir, []string{"codex", "claude"}); err != nil {
		t.Fatalf("idempotent Install: %v", err)
	}
}

func TestInstallDoesNotReplaceExistingSkillDirectory(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, ".agents", "skills", bundledSkillNames()[0])
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := Install(root, filepath.Join(root, ".turnal"), []string{"codex"})
	if err == nil || !strings.Contains(err.Error(), "already exists and is not a directory link") {
		t.Fatalf("Install error = %v, want safe collision error", err)
	}
	if info, statErr := os.Lstat(destination); statErr != nil || !info.IsDir() {
		t.Fatalf("existing directory was changed: info=%v err=%v", info, statErr)
	}
}
