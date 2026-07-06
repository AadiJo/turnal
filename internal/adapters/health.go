package adapters

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

type HookHealth struct {
	Target     Target
	ConfigPath string
	Problems   []string
}

func (health HookHealth) OK() bool {
	return len(health.Problems) == 0
}

func InspectHooks(projectRoot string, command string) []HookHealth {
	return []HookHealth{
		inspectClaudeHooks(projectRoot, command),
		inspectCodexHooks(projectRoot, command),
	}
}

func inspectClaudeHooks(projectRoot string, command string) HookHealth {
	settingsPath := filepath.Join(projectRoot, ".claude", "settings.json")
	health := HookHealth{Target: TargetClaude, ConfigPath: settingsPath}

	data, err := os.ReadFile(settingsPath)
	if os.IsNotExist(err) {
		health.Problems = append(health.Problems, fmt.Sprintf("claude hooks missing: %s", settingsPath))
		return health
	}
	if err != nil {
		health.Problems = append(health.Problems, fmt.Sprintf("read claude hooks: %v", err))
		return health
	}

	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		health.Problems = append(health.Problems, fmt.Sprintf("parse claude hooks: %v", err))
		return health
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		health.Problems = append(health.Problems, "claude hooks missing hooks table")
		return health
	}

	for eventName, expected := range map[string]string{
		"UserPromptSubmit": claudeUserHook(command),
		"Stop":             claudeAssistantHook(command),
		"PostToolUse":      claudeToolUseHook(command),
	} {
		if !containsHookCommand(collectHookCommands(hooks[eventName]), expected) {
			health.Problems = append(health.Problems, fmt.Sprintf("claude hook %s missing command %q", eventName, expected))
		}
	}
	return health
}

func inspectCodexHooks(projectRoot string, command string) HookHealth {
	configPath := filepath.Join(projectRoot, ".codex", "config.toml")
	health := HookHealth{Target: TargetCodex, ConfigPath: configPath}

	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		health.Problems = append(health.Problems, fmt.Sprintf("codex hooks missing: %s", configPath))
		return health
	}
	if err != nil {
		health.Problems = append(health.Problems, fmt.Sprintf("read codex hooks: %v", err))
		return health
	}

	var config map[string]any
	if err := toml.Unmarshal(data, &config); err != nil {
		health.Problems = append(health.Problems, fmt.Sprintf("parse codex hooks: %v", err))
		return health
	}
	features, _ := config["features"].(map[string]any)
	if features == nil || features["hooks"] != true {
		health.Problems = append(health.Problems, "codex hooks feature flag is not enabled")
	}
	hooks, _ := config["hooks"].(map[string]any)
	if hooks == nil {
		health.Problems = append(health.Problems, "codex hooks missing hooks table")
		return health
	}

	expected := codexHookCommand(command)
	for _, eventName := range []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Stop"} {
		if !containsHookCommand(collectHookCommands(hooks[eventName]), expected) {
			health.Problems = append(health.Problems, fmt.Sprintf("codex hook %s missing command %q", eventName, expected))
		}
	}
	return health
}

func collectHookCommands(value any) []string {
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
			if command != "" {
				commands = append(commands, command)
			}
		}
	}
	return commands
}

func containsHookCommand(commands []string, expected string) bool {
	for _, command := range commands {
		if command == expected {
			return true
		}
	}
	return false
}
