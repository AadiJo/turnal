package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/adapters"
	"github.com/AadiJo/turnal/internal/checkpoint"
)

func TestDestroyCommandRemovesMetadataAndLeavesWorkspaceFiles(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	if _, err := checkpoint.Init(root); err != nil {
		t.Fatalf("Init: %v", err)
	}
	writeFile(t, root, "app.txt", "content\n")
	t.Chdir(root.String())

	output := runRootStdout(t, "destroy")
	if !strings.Contains(output, "removed metadata:") {
		t.Fatalf("destroy output missing removal summary:\n%s", output)
	}
	if _, err := os.Stat(filepath.Join(root.String(), ".turnal")); !os.IsNotExist(err) {
		t.Fatalf(".turnal exists or could not be checked after destroy: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(root.String(), "app.txt")); err != nil || string(data) != "content\n" {
		t.Fatalf("workspace file changed after destroy: data=%q err=%v", data, err)
	}
}

func TestDestroyCommandRemovesPartialMetadata(t *testing.T) {
	root := workspaceRoot(t)
	if err := os.MkdirAll(filepath.Join(root.String(), ".turnal", "log"), 0o755); err != nil {
		t.Fatalf("mkdir partial metadata: %v", err)
	}
	nested := filepath.Join(root.String(), "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	t.Chdir(nested)

	output := runRootStdout(t, "destroy")
	if !strings.Contains(output, "removed metadata:") {
		t.Fatalf("destroy output missing removal summary:\n%s", output)
	}
	if _, err := os.Stat(filepath.Join(root.String(), ".turnal")); !os.IsNotExist(err) {
		t.Fatalf("partial .turnal exists or could not be checked after destroy: %v", err)
	}
}

func TestDestroyCommandDryRunKeepsMetadataAndHookConfig(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	if _, err := checkpoint.Init(root); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := adapters.InstallWithOptions(root.String(), []adapters.Target{adapters.TargetClaude, adapters.TargetCodex}, adapters.InstallOptions{HookCommand: "turnal"}); err != nil {
		t.Fatalf("InstallWithOptions: %v", err)
	}
	claudeSettings := filepath.Join(root.String(), ".claude", "settings.json")
	codexConfig := filepath.Join(root.String(), ".codex", "config.toml")
	beforeClaude, err := os.ReadFile(claudeSettings)
	if err != nil {
		t.Fatalf("read Claude settings: %v", err)
	}
	beforeCodex, err := os.ReadFile(codexConfig)
	if err != nil {
		t.Fatalf("read Codex config: %v", err)
	}
	t.Chdir(root.String())

	output := runRootStdout(t, "destroy", "--dry-run", "--remove-hooks", "--agent", "all")
	for _, want := range []string{"would remove claude hooks:", "would remove codex hooks:", "would remove metadata:"} {
		if !strings.Contains(output, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, output)
		}
	}
	if _, err := os.Stat(filepath.Join(root.String(), ".turnal")); err != nil {
		t.Fatalf(".turnal missing after dry-run: %v", err)
	}
	afterClaude, err := os.ReadFile(claudeSettings)
	if err != nil {
		t.Fatalf("read Claude settings after dry-run: %v", err)
	}
	afterCodex, err := os.ReadFile(codexConfig)
	if err != nil {
		t.Fatalf("read Codex config after dry-run: %v", err)
	}
	if string(afterClaude) != string(beforeClaude) {
		t.Fatalf("Claude settings changed during dry-run:\nbefore=%s\nafter=%s", beforeClaude, afterClaude)
	}
	if string(afterCodex) != string(beforeCodex) {
		t.Fatalf("Codex config changed during dry-run:\nbefore=%s\nafter=%s", beforeCodex, afterCodex)
	}
}

func TestDestroyCommandRemovesRootHooksFromAttachedLinkedWorktree(t *testing.T) {
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
	runForkUserGit(t, mainPath, "worktree", "add", "-b", "destroy-linked-test", linkedPath)
	t.Chdir(linkedPath)

	initOutput := runRootStdout(t, "init", "--agent", "codex")
	if !strings.Contains(initOutput, "configured codex hooks:") {
		t.Fatalf("init output missing Codex hooks:\n%s", initOutput)
	}
	metadataDir := filepath.Join(mainPath, ".turnal")
	configPath := filepath.Join(mainPath, ".codex", "config.toml")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read root-checkout Codex config: %v", err)
	}

	dryRunOutput := runRootStdout(t, "destroy", "--dry-run", "--remove-hooks", "--agent", "codex")
	for _, want := range []string{"would remove codex hooks:", "would remove metadata:"} {
		if !strings.Contains(dryRunOutput, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, dryRunOutput)
		}
	}
	afterDryRun, err := os.ReadFile(configPath)
	if err != nil || string(afterDryRun) != string(before) {
		t.Fatalf("dry-run changed root-checkout config: err=%v\nbefore=%s\nafter=%s", err, before, afterDryRun)
	}
	if _, err := os.Stat(metadataDir); err != nil {
		t.Fatalf("dry-run removed shared metadata: %v", err)
	}

	removeOutput := runRootStdout(t, "destroy", "--remove-hooks", "--agent", "codex")
	for _, want := range []string{"removed codex hooks:", "removed metadata:"} {
		if !strings.Contains(removeOutput, want) {
			t.Fatalf("removal output missing %q:\n%s", want, removeOutput)
		}
	}
	afterRemoval, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read root-checkout config after removal: %v", err)
	}
	if strings.Contains(string(afterRemoval), "turnal codex-hook") {
		t.Fatalf("root-checkout hooks remain after removal:\n%s", afterRemoval)
	}
	if _, err := os.Stat(metadataDir); !os.IsNotExist(err) {
		t.Fatalf("shared metadata exists or could not be checked after removal: %v", err)
	}
}

func TestDestroyCommandCanRemoveHooks(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	if _, err := checkpoint.Init(root); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := adapters.InstallWithOptions(root.String(), []adapters.Target{adapters.TargetClaude, adapters.TargetCodex}, adapters.InstallOptions{HookCommand: "turnal"}); err != nil {
		t.Fatalf("InstallWithOptions: %v", err)
	}
	t.Chdir(root.String())

	output := runRootStdout(t, "destroy", "--remove-hooks", "--agent", "all")
	for _, want := range []string{"removed claude hooks:", "removed codex hooks:", "removed metadata:"} {
		if !strings.Contains(output, want) {
			t.Fatalf("destroy output missing %q:\n%s", want, output)
		}
	}
	if _, err := os.Stat(filepath.Join(root.String(), ".turnal")); !os.IsNotExist(err) {
		t.Fatalf(".turnal exists or could not be checked after destroy: %v", err)
	}
	for _, path := range []string{
		filepath.Join(root.String(), ".claude", "settings.json"),
		filepath.Join(root.String(), ".codex", "config.toml"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read hook config %s: %v", path, err)
		}
		if strings.Contains(string(data), "turnal") {
			t.Fatalf("hook config still contains turnal command %s:\n%s", path, data)
		}
	}
}

func TestDestroyCommandAgentNoneLeavesHooks(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	if _, err := checkpoint.Init(root); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := adapters.InstallWithOptions(root.String(), []adapters.Target{adapters.TargetClaude, adapters.TargetCodex}, adapters.InstallOptions{HookCommand: "turnal"}); err != nil {
		t.Fatalf("InstallWithOptions: %v", err)
	}
	t.Chdir(root.String())

	output := runRootStdout(t, "destroy", "--remove-hooks", "--agent", "none")
	if strings.Contains(output, "hooks:") {
		t.Fatalf("destroy output includes hook cleanup despite --agent none:\n%s", output)
	}
	if _, err := os.Stat(filepath.Join(root.String(), ".turnal")); !os.IsNotExist(err) {
		t.Fatalf(".turnal exists or could not be checked after destroy: %v", err)
	}
	for _, path := range []string{
		filepath.Join(root.String(), ".claude", "settings.json"),
		filepath.Join(root.String(), ".codex", "config.toml"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read hook config %s: %v", path, err)
		}
		if !strings.Contains(string(data), "turnal") {
			t.Fatalf("hook config lost turnal command despite --agent none %s:\n%s", path, data)
		}
	}
}
