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
