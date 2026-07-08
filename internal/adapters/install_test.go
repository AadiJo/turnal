package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestInstallClaudeHookPreservesExistingHooksAndIsIdempotent(t *testing.T) {
	t.Setenv("TURNAL_HOOK_COMMAND", "turnal")

	root := t.TempDir()
	claudeDir := filepath.Join(root, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	existing := `{
  "hooks": {
    "Stop": [
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "echo keep"},
          {"type": "command", "command": "turnal claude-hook assistant"}
        ]
      }
    ]
  }
}`
	if err := os.WriteFile(settingsPath, []byte(existing), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	if _, err := InstallClaudeHook(root); err != nil {
		t.Fatalf("InstallClaudeHook: %v", err)
	}
	if _, err := InstallClaudeHook(root); err != nil {
		t.Fatalf("second InstallClaudeHook: %v", err)
	}

	var settings map[string]any
	readJSONFile(t, settingsPath, &settings)
	hooks := settings["hooks"].(map[string]any)

	stopCommands := hookCommands(t, hooks["Stop"])
	if countCommand(stopCommands, "echo keep") != 1 {
		t.Fatalf("existing Stop hook not preserved once: %#v", stopCommands)
	}
	if countCommand(stopCommands, claudeAssistantHook("turnal")) != 1 {
		t.Fatalf("assistant hook count = %d, commands=%#v", countCommand(stopCommands, claudeAssistantHook("turnal")), stopCommands)
	}
	for eventName, command := range map[string]string{
		"UserPromptSubmit": claudeUserHook("turnal"),
		"PostToolUse":      claudeToolUseHook("turnal"),
	} {
		commands := hookCommands(t, hooks[eventName])
		if countCommand(commands, command) != 1 {
			t.Fatalf("%s hook count = %d, commands=%#v", eventName, countCommand(commands, command), commands)
		}
	}
}

func TestInstallCodexHookMergesConfigAndEnablesHooksFeature(t *testing.T) {
	t.Setenv("TURNAL_HOOK_COMMAND", "turnal")

	root := t.TempDir()
	codexDir := filepath.Join(root, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("mkdir .codex: %v", err)
	}
	configPath := filepath.Join(codexDir, "config.toml")
	existing := `
model = "gpt-5.5"

[features]
web_search = true

[hooks]
[[hooks.PostToolUse]]
matcher = "Bash"
[[hooks.PostToolUse.hooks]]
type = "command"
command = "echo keep"
`
	if err := os.WriteFile(configPath, []byte(existing), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := InstallCodexHook(root); err != nil {
		t.Fatalf("InstallCodexHook: %v", err)
	}
	if _, err := InstallCodexHook(root); err != nil {
		t.Fatalf("second InstallCodexHook: %v", err)
	}

	var config map[string]any
	readTOMLFile(t, configPath, &config)
	if config["model"] != "gpt-5.5" {
		t.Fatalf("model was not preserved: %#v", config["model"])
	}
	features := config["features"].(map[string]any)
	if features["web_search"] != true || features["hooks"] != true {
		t.Fatalf("features not merged: %#v", features)
	}

	hooks := config["hooks"].(map[string]any)
	for _, eventName := range []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Stop"} {
		commands := hookCommands(t, hooks[eventName])
		if countCommand(commands, codexHookCommand("turnal")) != 1 {
			t.Fatalf("%s hook count = %d, commands=%#v", eventName, countCommand(commands, codexHookCommand("turnal")), commands)
		}
	}

	postToolUseCommands := hookCommands(t, hooks["PostToolUse"])
	if countCommand(postToolUseCommands, "echo keep") != 1 {
		t.Fatalf("existing PostToolUse hook not preserved: %#v", postToolUseCommands)
	}
}

func TestUninstallHooksRemovesTurnalCommandsAndPreservesOthers(t *testing.T) {
	root := t.TempDir()

	claudeDir := filepath.Join(root, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	claudeSettings := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(claudeSettings, []byte(`{
  "hooks": {
    "Stop": [
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "echo keep"},
          {"type": "command", "command": "turnal claude-hook assistant"}
        ]
      }
    ],
    "UserPromptSubmit": [
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "turnal claude-hook user"}
        ]
      }
    ]
  }
}`), 0o644); err != nil {
		t.Fatalf("write Claude settings: %v", err)
	}

	codexDir := filepath.Join(root, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("mkdir .codex: %v", err)
	}
	codexConfig := filepath.Join(codexDir, "config.toml")
	if err := os.WriteFile(codexConfig, []byte(`
model = "gpt-5.5"

[features]
web_search = true
hooks = true

[hooks]
[[hooks.PostToolUse]]
matcher = "Bash"
[[hooks.PostToolUse.hooks]]
type = "command"
command = "echo keep"
[[hooks.PostToolUse.hooks]]
type = "command"
command = "turnal codex-hook"

[[hooks.Stop]]
matcher = ""
[[hooks.Stop.hooks]]
type = "command"
command = "turnal codex-hook"
`), 0o644); err != nil {
		t.Fatalf("write Codex config: %v", err)
	}

	results, err := Uninstall(root, []Target{TargetClaude, TargetCodex})
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if len(results) != 2 || results[0].RemovedCommands != 2 || results[1].RemovedCommands != 2 {
		t.Fatalf("uninstall results = %#v, want two removed commands per target", results)
	}

	var settings map[string]any
	readJSONFile(t, claudeSettings, &settings)
	claudeHooks := settings["hooks"].(map[string]any)
	stopCommands := hookCommands(t, claudeHooks["Stop"])
	if countCommand(stopCommands, "echo keep") != 1 {
		t.Fatalf("Claude Stop hook was not preserved: %#v", stopCommands)
	}
	if countCommand(stopCommands, "turnal claude-hook assistant") != 0 {
		t.Fatalf("Claude turnal hook was preserved: %#v", stopCommands)
	}
	if _, ok := claudeHooks["UserPromptSubmit"]; ok {
		t.Fatalf("empty Claude UserPromptSubmit hook group was preserved: %#v", claudeHooks["UserPromptSubmit"])
	}

	var config map[string]any
	readTOMLFile(t, codexConfig, &config)
	if config["model"] != "gpt-5.5" {
		t.Fatalf("Codex model was not preserved: %#v", config["model"])
	}
	features := config["features"].(map[string]any)
	if features["web_search"] != true || features["hooks"] != true {
		t.Fatalf("Codex features were not preserved: %#v", features)
	}
	codexHooks := config["hooks"].(map[string]any)
	postToolUseCommands := hookCommands(t, codexHooks["PostToolUse"])
	if countCommand(postToolUseCommands, "echo keep") != 1 {
		t.Fatalf("Codex PostToolUse hook was not preserved: %#v", postToolUseCommands)
	}
	if countCommand(postToolUseCommands, "turnal codex-hook") != 0 {
		t.Fatalf("Codex turnal hook was preserved: %#v", postToolUseCommands)
	}
	if _, ok := codexHooks["Stop"]; ok {
		t.Fatalf("empty Codex Stop hook group was preserved: %#v", codexHooks["Stop"])
	}
}

func TestUninstallHooksDryRunDoesNotWriteConfig(t *testing.T) {
	root := t.TempDir()
	if _, err := InstallWithOptions(root, []Target{TargetClaude, TargetCodex}, InstallOptions{HookCommand: "turnal"}); err != nil {
		t.Fatalf("InstallWithOptions: %v", err)
	}

	claudeSettings := filepath.Join(root, ".claude", "settings.json")
	codexConfig := filepath.Join(root, ".codex", "config.toml")
	beforeClaude, err := os.ReadFile(claudeSettings)
	if err != nil {
		t.Fatalf("read Claude settings: %v", err)
	}
	beforeCodex, err := os.ReadFile(codexConfig)
	if err != nil {
		t.Fatalf("read Codex config: %v", err)
	}

	results, err := UninstallWithOptions(root, []Target{TargetClaude, TargetCodex}, UninstallOptions{DryRun: true})
	if err != nil {
		t.Fatalf("UninstallWithOptions dry run: %v", err)
	}
	if len(results) != 2 || results[0].RemovedCommands == 0 || results[1].RemovedCommands == 0 {
		t.Fatalf("dry-run results = %#v, want planned removals", results)
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

func TestInstallHooksBackUpInvalidConfig(t *testing.T) {
	t.Setenv("TURNAL_HOOK_COMMAND", "turnal")

	root := t.TempDir()

	claudeDir := filepath.Join(root, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	claudeSettings := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(claudeSettings, []byte("{broken\n"), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	claudeResult, err := InstallClaudeHook(root)
	if err != nil {
		t.Fatalf("InstallClaudeHook: %v", err)
	}
	if claudeResult.BackupPath != claudeSettings+".backup" {
		t.Fatalf("Claude backup = %q, want %q", claudeResult.BackupPath, claudeSettings+".backup")
	}

	codexDir := filepath.Join(root, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("mkdir .codex: %v", err)
	}
	codexConfig := filepath.Join(codexDir, "config.toml")
	if err := os.WriteFile(codexConfig, []byte("[broken\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	codexResult, err := InstallCodexHook(root)
	if err != nil {
		t.Fatalf("InstallCodexHook: %v", err)
	}
	if codexResult.BackupPath != codexConfig+".backup" {
		t.Fatalf("Codex backup = %q, want %q", codexResult.BackupPath, codexConfig+".backup")
	}
}

func TestInstallHooksCanUseConfiguredCommandPrefix(t *testing.T) {
	root := t.TempDir()
	opts := InstallOptions{HookCommand: "/tmp/turnal-live"}
	if _, err := InstallCodexHookWithOptions(root, opts); err != nil {
		t.Fatalf("InstallCodexHook: %v", err)
	}
	if _, err := InstallCodexHookWithOptions(root, opts); err != nil {
		t.Fatalf("second InstallCodexHook: %v", err)
	}

	var config map[string]any
	readTOMLFile(t, filepath.Join(root, ".codex", "config.toml"), &config)
	commands := hookCommands(t, config["hooks"].(map[string]any)["Stop"])
	if countCommand(commands, "/tmp/turnal-live codex-hook") != 1 {
		t.Fatalf("configured command not used: %#v", commands)
	}
}

func TestInspectHooksReportsInstalledHooks(t *testing.T) {
	root := t.TempDir()
	opts := InstallOptions{HookCommand: "/tmp/turnal-live"}
	if _, err := InstallWithOptions(root, []Target{TargetClaude, TargetCodex}, opts); err != nil {
		t.Fatalf("InstallWithOptions: %v", err)
	}

	health := InspectHooks(root, opts.HookCommand)
	for _, item := range health {
		if !item.OK() {
			t.Fatalf("%s health problems = %#v", item.Target, item.Problems)
		}
	}

	health = InspectHooks(root, "turnal")
	var problems []string
	for _, item := range health {
		problems = append(problems, item.Problems...)
	}
	if len(problems) == 0 {
		t.Fatal("InspectHooks reported no problems for mismatched command")
	}
}

func TestResolveTargets(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}

	targets, err := ResolveTargets(root, TargetAuto)
	if err != nil {
		t.Fatalf("ResolveTargets: %v", err)
	}
	if len(targets) == 0 || targets[0] != TargetClaude {
		t.Fatalf("auto targets = %#v, want Claude first", targets)
	}

	targets, err = ResolveTargets(root, TargetAll)
	if err != nil {
		t.Fatalf("ResolveTargets all: %v", err)
	}
	if len(targets) != 2 || targets[0] != TargetClaude || targets[1] != TargetCodex {
		t.Fatalf("all targets = %#v", targets)
	}

	if _, err := ResolveTargets(root, Target("both")); err == nil {
		t.Fatal("ResolveTargets both succeeded, want invalid target error")
	}

	targets, err = ResolveTargets(root, TargetNone)
	if err != nil {
		t.Fatalf("ResolveTargets none: %v", err)
	}
	if len(targets) != 0 {
		t.Fatalf("none targets = %#v, want empty", targets)
	}
}

func readJSONFile(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, data)
	}
}

func readTOMLFile(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := toml.Unmarshal(data, out); err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, data)
	}
}

func hookCommands(t *testing.T, value any) []string {
	t.Helper()

	var commands []string
	for _, group := range normalizeHookGroups(value) {
		groupMap, ok := group.(map[string]any)
		if !ok {
			continue
		}
		hooks, _ := normalizeHookArray(groupMap["hooks"])
		for _, hook := range hooks {
			hookMap, ok := hook.(map[string]any)
			if !ok {
				continue
			}
			command, _ := hookMap["command"].(string)
			commands = append(commands, command)
		}
	}
	return commands
}

func countCommand(commands []string, expected string) int {
	count := 0
	for _, command := range commands {
		if command == expected {
			count++
		}
	}
	return count
}
