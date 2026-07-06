package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-vcs-again/internal/checkpoint"
)

func TestInitCommandInitializesWorkspaceGitAndGitignore(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	t.Chdir(root.String())

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"init", "--skip-hooks"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init command: %v\n%s", err, out.String())
	}

	if _, err := os.Stat(filepath.Join(root.String(), ".git")); err != nil {
		t.Fatalf("expected workspace .git: %v", err)
	}

	gitignore, err := os.ReadFile(filepath.Join(root.String(), ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if string(gitignore) != checkpoint.GitignoreEntry+"\n" {
		t.Fatalf(".gitignore = %q, want %q", gitignore, checkpoint.GitignoreEntry+"\n")
	}

	output := out.String()
	for _, want := range []string{
		"initialized hidden git repo:",
		"initialized workspace git repo:",
		"updated gitignore:",
		"adapter hooks skipped",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("init output missing %q:\n%s", want, output)
		}
	}
}
