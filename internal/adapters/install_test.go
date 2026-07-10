package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

func TestInstallHooksRejectInvalidConfigWithoutReplacingIt(t *testing.T) {
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
	if _, err := InstallClaudeHook(root); err == nil {
		t.Fatal("InstallClaudeHook accepted invalid settings")
	}
	if data, err := os.ReadFile(claudeSettings); err != nil || string(data) != "{broken\n" {
		t.Fatalf("Claude settings changed: data=%q err=%v", data, err)
	}

	codexDir := filepath.Join(root, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("mkdir .codex: %v", err)
	}
	codexConfig := filepath.Join(codexDir, "config.toml")
	if err := os.WriteFile(codexConfig, []byte("[broken\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := InstallCodexHook(root); err == nil {
		t.Fatal("InstallCodexHook accepted invalid config")
	}
	if data, err := os.ReadFile(codexConfig); err != nil || string(data) != "[broken\n" {
		t.Fatalf("Codex config changed: data=%q err=%v", data, err)
	}
}

func TestInstallHooksRejectUnexpectedSectionShapes(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".codex"), 0o755); err != nil {
		t.Fatalf("mkdir .codex: %v", err)
	}
	path := filepath.Join(root, ".codex", "config.toml")
	original := []byte("hooks = \"unexpected\"\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := InstallCodexHook(root); err == nil || !strings.Contains(err.Error(), "must be an object/table") {
		t.Fatalf("InstallCodexHook error = %v, want schema error", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(original) {
		t.Fatalf("unexpected-shape config changed: data=%q err=%v", after, err)
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

func TestInstallCodexHookDoesNotRewriteSemanticallyCurrentConfig(t *testing.T) {
	root := t.TempDir()
	if _, err := InstallCodexHook(root); err != nil {
		t.Fatalf("initial InstallCodexHook: %v", err)
	}
	path := filepath.Join(root, ".codex", "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	withComment := append([]byte("# operator comment must survive\n"), data...)
	if err := os.WriteFile(path, withComment, 0o600); err != nil {
		t.Fatalf("write commented config: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod commented config: %v", err)
	}

	if _, err := InstallCodexHook(root); err != nil {
		t.Fatalf("second InstallCodexHook: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config after reinstall: %v", err)
	}
	if string(after) != string(withComment) {
		t.Fatalf("idempotent install rewrote config:\nbefore=%s\nafter=%s", withComment, after)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o, want 600", info.Mode().Perm())
	}
}

func TestUninstallCodexHookDisablesFeatureWhenNoHooksRemain(t *testing.T) {
	root := t.TempDir()
	if _, err := InstallCodexHook(root); err != nil {
		t.Fatalf("InstallCodexHook: %v", err)
	}
	if _, err := UninstallCodexHook(root); err != nil {
		t.Fatalf("UninstallCodexHook: %v", err)
	}
	var config map[string]any
	readTOMLFile(t, filepath.Join(root, ".codex", "config.toml"), &config)
	if _, ok := config["hooks"]; ok {
		t.Fatalf("hooks section remains after uninstall: %#v", config["hooks"])
	}
	if features, ok := config["features"].(map[string]any); ok && features["hooks"] == true {
		t.Fatalf("hooks feature remains enabled after uninstall: %#v", features)
	}
}

func TestConcurrentCodexHookInstallsRemainValidAndDeduplicated(t *testing.T) {
	root := t.TempDir()
	const workers = 8
	var wait sync.WaitGroup
	errorsByWorker := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := InstallCodexHook(root)
			errorsByWorker <- err
		}()
	}
	wait.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		if err != nil {
			t.Fatalf("concurrent InstallCodexHook: %v", err)
		}
	}
	var config map[string]any
	readTOMLFile(t, filepath.Join(root, ".codex", "config.toml"), &config)
	hooks := config["hooks"].(map[string]any)
	for _, eventName := range []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Stop"} {
		commands := hookCommands(t, hooks[eventName])
		if countCommand(commands, codexHookCommand("turnal")) != 1 {
			t.Fatalf("%s commands after concurrent install = %#v", eventName, commands)
		}
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
