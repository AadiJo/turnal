package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/AadiJo/turnal/internal/checkpoint"
)

func TestInitCommandInitializesHiddenGitAndGitignoreOnly(t *testing.T) {
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

	if _, err := os.Lstat(filepath.Join(root.String(), ".git")); !os.IsNotExist(err) {
		t.Fatalf("workspace .git exists or could not be checked: %v", err)
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
		"gitignore update skipped",
		"adapter hooks skipped",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("init output missing %q:\n%s", want, output)
		}
	}
}

func TestInitCommandCanSkipGitignoreFromFlag(t *testing.T) {
	requireGit(t)
	isolateAgentConfig(t)

	root := workspaceRoot(t)
	t.Chdir(root.String())

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"init", "--skip-hooks", "--update-gitignore=false"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init command: %v\n%s", err, out.String())
	}

	if _, err := os.Stat(filepath.Join(root.String(), ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf(".gitignore exists or could not be checked: %v", err)
	}
	if !strings.Contains(out.String(), "gitignore update skipped") {
		t.Fatalf("init output did not report skipped gitignore update:\n%s", out.String())
	}
}

func TestInitUsesRootCheckoutCodexHooksFromLinkedWorktree(t *testing.T) {
	requireGit(t)
	isolateAgentConfig(t)
	parent := t.TempDir()
	mainPath := filepath.Join(parent, "main")
	linkedPath := filepath.Join(parent, "linked")
	if err := os.MkdirAll(mainPath, 0o755); err != nil {
		t.Fatalf("mkdir main worktree: %v", err)
	}
	runForkUserGit(t, mainPath, "init")
	runForkUserGit(t, mainPath, "config", "user.email", "turnal@example.test")
	runForkUserGit(t, mainPath, "config", "user.name", "Turnal Test")
	if err := os.WriteFile(filepath.Join(mainPath, "tracked.txt"), []byte("tracked\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	runForkUserGit(t, mainPath, "add", "tracked.txt")
	runForkUserGit(t, mainPath, "commit", "-m", "initial")
	runForkUserGit(t, mainPath, "worktree", "add", "-b", "init-linked-test", linkedPath)
	t.Chdir(linkedPath)

	initCmd := NewRootCmd()
	var initOut bytes.Buffer
	initCmd.SetOut(&initOut)
	initCmd.SetErr(&initOut)
	initCmd.SetArgs([]string{"init", "--agent", "codex"})
	if err := initCmd.Execute(); err != nil {
		t.Fatalf("linked-worktree init: %v\n%s", err, initOut.String())
	}
	rootConfig := filepath.Join(mainPath, ".codex", "config.toml")
	if !strings.Contains(initOut.String(), "configured codex hooks:") {
		t.Fatalf("init output did not report configured Codex hooks:\n%s", initOut.String())
	}
	if _, err := os.Stat(rootConfig); err != nil {
		t.Fatalf("root-checkout config was not installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(linkedPath, ".codex", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("linked worktree config exists or could not be checked: %v", err)
	}

	statusCmd := NewRootCmd()
	var statusOut bytes.Buffer
	statusCmd.SetOut(&statusOut)
	statusCmd.SetErr(&statusOut)
	statusCmd.SetArgs([]string{"status"})
	if err := statusCmd.Execute(); err != nil {
		t.Fatalf("status after linked-worktree init: %v\n%s", err, statusOut.String())
	}
	if !strings.Contains(statusOut.String(), "hooks:      ok") || !strings.Contains(statusOut.String(), "state:      ok") {
		t.Fatalf("status output not ok:\n%s", statusOut.String())
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

	configData, err := os.ReadFile(filepath.Join(root.String(), ".turnal", "config.toml"))
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

func TestInitCommandPersistsAgentNoneForStatus(t *testing.T) {
	requireGit(t)
	isolateAgentConfig(t)

	root := workspaceRoot(t)
	t.Chdir(root.String())

	initCmd := NewRootCmd()
	var initOut bytes.Buffer
	initCmd.SetOut(&initOut)
	initCmd.SetErr(&initOut)
	initCmd.SetArgs([]string{"init", "--agent", "none"})
	if err := initCmd.Execute(); err != nil {
		t.Fatalf("init command: %v\n%s", err, initOut.String())
	}

	configData, err := os.ReadFile(filepath.Join(root.String(), ".turnal", "config.toml"))
	if err != nil {
		t.Fatalf("read workspace config: %v", err)
	}
	if !strings.Contains(string(configData), "[init]") || !strings.Contains(string(configData), "agent = 'none'") {
		t.Fatalf("workspace config missing persisted init agent:\n%s", configData)
	}

	statusCmd := NewRootCmd()
	var statusOut bytes.Buffer
	statusCmd.SetOut(&statusOut)
	statusCmd.SetErr(&statusOut)
	statusCmd.SetArgs([]string{"status"})
	if err := statusCmd.Execute(); err != nil {
		t.Fatalf("status command after init --agent none: %v\n%s", err, statusOut.String())
	}
	if !strings.Contains(statusOut.String(), "hooks:      ok") || !strings.Contains(statusOut.String(), "state:      ok") {
		t.Fatalf("status output not ok:\n%s", statusOut.String())
	}
}

func TestInitCommandPersistsSkipHooksForStatus(t *testing.T) {
	requireGit(t)
	isolateAgentConfig(t)

	root := workspaceRoot(t)
	t.Chdir(root.String())

	initCmd := NewRootCmd()
	var initOut bytes.Buffer
	initCmd.SetOut(&initOut)
	initCmd.SetErr(&initOut)
	initCmd.SetArgs([]string{"init", "--skip-hooks"})
	if err := initCmd.Execute(); err != nil {
		t.Fatalf("init command: %v\n%s", err, initOut.String())
	}

	configData, err := os.ReadFile(filepath.Join(root.String(), ".turnal", "config.toml"))
	if err != nil {
		t.Fatalf("read workspace config: %v", err)
	}
	if !strings.Contains(string(configData), "[init]") || !strings.Contains(string(configData), "install_hooks = false") {
		t.Fatalf("workspace config missing persisted skip-hooks:\n%s", configData)
	}

	statusCmd := NewRootCmd()
	var statusOut bytes.Buffer
	statusCmd.SetOut(&statusOut)
	statusCmd.SetErr(&statusOut)
	statusCmd.SetArgs([]string{"status"})
	if err := statusCmd.Execute(); err != nil {
		t.Fatalf("status command after init --skip-hooks: %v\n%s", err, statusOut.String())
	}
	if !strings.Contains(statusOut.String(), "hooks:      ok") || !strings.Contains(statusOut.String(), "state:      ok") {
		t.Fatalf("status output not ok:\n%s", statusOut.String())
	}
}

func TestCodexTrustNoticeExplainsCLIApproval(t *testing.T) {
	var out bytes.Buffer
	writeCodexTrustNotice(&out)
	for _, want := range []string{"Codex hook trust required", "Codex desktop app", "app-server wrapper", "Codex CLI", "trust the Turnal hooks"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("trust notice missing %q:\n%s", want, out.String())
		}
	}
}

func TestCodexTrustNoticeHasAlignedBorders(t *testing.T) {
	var out bytes.Buffer
	writeCodexTrustNotice(&out)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("notice has %d lines, want 4:\n%s", len(lines), out.String())
	}
	wantWidth := utf8.RuneCountInString(lines[0])
	for index, line := range lines[1:] {
		if got := utf8.RuneCountInString(line); got != wantWidth {
			t.Fatalf("line %d width = %d, want %d:\n%s", index+2, got, wantWidth, out.String())
		}
	}
}

func TestConfirmSkillInstallationDefaultsToYes(t *testing.T) {
	for _, answer := range []string{"\n", "y\n", "YES\n"} {
		var prompt bytes.Buffer
		confirmed, err := confirmSkillInstallation(strings.NewReader(answer), &prompt)
		if err != nil || !confirmed {
			t.Fatalf("answer %q: confirmed=%v err=%v", answer, confirmed, err)
		}
		if !strings.Contains(prompt.String(), "[Y/n]") {
			t.Fatalf("prompt = %q, want default-yes marker", prompt.String())
		}
	}
}

func TestConfirmSkillInstallationDeclinesOtherAnswers(t *testing.T) {
	confirmed, err := confirmSkillInstallation(strings.NewReader("n\n"), &bytes.Buffer{})
	if err != nil || confirmed {
		t.Fatalf("confirmed=%v err=%v, want declined", confirmed, err)
	}
}
