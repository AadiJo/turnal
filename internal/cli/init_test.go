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
	isolateAgentConfig(t)

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

func TestInitCommandUsesGlobalConfig(t *testing.T) {
	requireGit(t)
	writeGlobalAgentConfig(t, `
version = 1

[init]
agent = "none"

[bootstrap]
init_workspace_git = false
update_gitignore = false
`)

	root := workspaceRoot(t)
	t.Chdir(root.String())

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"init"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init command: %v\n%s", err, out.String())
	}

	if _, err := os.Stat(filepath.Join(root.String(), ".git")); !os.IsNotExist(err) {
		t.Fatalf("workspace .git exists or could not be checked: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root.String(), ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf(".gitignore exists or could not be checked: %v", err)
	}

	output := out.String()
	for _, want := range []string{
		"workspace git skipped",
		"gitignore update skipped",
		"adapter hooks skipped",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("init output missing %q:\n%s", want, output)
		}
	}
}

func TestInitCommandUsesGitSyncConfig(t *testing.T) {
	requireGit(t)
	writeGlobalAgentConfig(t, `
version = 1

[init]
agent = "none"

[git_sync]
enabled = true
`)

	root := workspaceRoot(t)
	t.Chdir(root.String())

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"init"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init command: %v\n%s", err, out.String())
	}

	configData, err := os.ReadFile(filepath.Join(root.String(), ".agent-vcs", "config.toml"))
	if err != nil {
		t.Fatalf("read workspace config: %v", err)
	}
	if !strings.Contains(string(configData), "[git_sync]") || !strings.Contains(string(configData), "enabled = true") {
		t.Fatalf("workspace config missing enabled git-sync:\n%s", configData)
	}
	if !strings.Contains(out.String(), "enabled git-sync capture:") {
		t.Fatalf("init output missing git-sync enable message:\n%s", out.String())
	}
}
