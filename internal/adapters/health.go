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
	Status     HookConfigurationStatus
	Events     []HookEventHealth
	Problems   []string
}

type HookConfigurationStatus string

const (
	HookConfigurationConfigured HookConfigurationStatus = "configured"
	HookConfigurationMissing    HookConfigurationStatus = "missing"
	HookConfigurationIncomplete HookConfigurationStatus = "incomplete"
	HookConfigurationMalformed  HookConfigurationStatus = "malformed"
	HookConfigurationDisabled   HookConfigurationStatus = "disabled"
)

type HookEventStatus string

const (
	HookEventConfigured       HookEventStatus = "configured"
	HookEventMissing          HookEventStatus = "missing"
	HookEventDifferentCommand HookEventStatus = "different-command"
)

type HookEventHealth struct {
	Name     string
	Status   HookEventStatus
	Commands []string
}

func (health HookHealth) OK() bool {
	return len(health.Problems) == 0
}

func InspectHooks(projectRoot string, command string) []HookHealth {
	return InspectHooksForTargets(projectRoot, command, []Target{TargetClaude, TargetCodex})
}

func InspectHooksForTargets(projectRoot string, command string, targets []Target) []HookHealth {
	var health []HookHealth
	for _, target := range targets {
		switch target {
		case TargetClaude:
			health = append(health, inspectClaudeHooks(projectRoot, command))
		case TargetCodex:
			health = append(health, inspectCodexHooks(projectRoot, command))
		}
	}
	return health
}

func inspectAllHooks(projectRoot string, command string) []HookHealth {
	return []HookHealth{
		inspectClaudeHooks(projectRoot, command),
		inspectCodexHooks(projectRoot, command),
	}
}

func inspectClaudeHooks(projectRoot string, command string) HookHealth {
	settingsPath := filepath.Join(projectRoot, ".claude", "settings.json")
	health := HookHealth{Target: TargetClaude, ConfigPath: settingsPath, Status: HookConfigurationConfigured}

	data, err := os.ReadFile(settingsPath)
	if os.IsNotExist(err) {
		health.Status = HookConfigurationMissing
		health.Problems = append(health.Problems, fmt.Sprintf("claude hooks missing: %s", settingsPath))
		return health
	}
	if err != nil {
		health.Status = HookConfigurationMalformed
		health.Problems = append(health.Problems, fmt.Sprintf("read claude hooks: %v", err))
		return health
	}

	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		health.Status = HookConfigurationMalformed
		health.Problems = append(health.Problems, fmt.Sprintf("parse claude hooks: %v", err))
		return health
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		health.Status = HookConfigurationIncomplete
		health.Problems = append(health.Problems, "claude hooks missing hooks table")
		return health
	}

	for _, expected := range []struct{ eventName, command string }{
		{"UserPromptSubmit", claudeUserHook(command)},
		{"PostToolUse", claudeToolUseHook(command)},
		{"Stop", claudeAssistantHook(command)},
	} {
		inspectHookEvent(&health, "claude", hooks, expected.eventName, expected.command)
	}
	return health
}

func inspectCodexHooks(projectRoot string, command string) HookHealth {
	projectRoot = EffectiveHookRoot(projectRoot, TargetCodex)
	configPath := filepath.Join(projectRoot, ".codex", "config.toml")
	health := HookHealth{Target: TargetCodex, ConfigPath: configPath, Status: HookConfigurationConfigured}

	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		health.Status = HookConfigurationMissing
		health.Problems = append(health.Problems, fmt.Sprintf("codex hooks missing: %s", configPath))
		return health
	}
	if err != nil {
		health.Status = HookConfigurationMalformed
		health.Problems = append(health.Problems, fmt.Sprintf("read codex hooks: %v", err))
		return health
	}

	var config map[string]any
	if err := toml.Unmarshal(data, &config); err != nil {
		health.Status = HookConfigurationMalformed
		health.Problems = append(health.Problems, fmt.Sprintf("parse codex hooks: %v", err))
		return health
	}
	features, featuresExist, err := configMapSection(config, "features")
	if err != nil {
		health.Status = HookConfigurationMalformed
		health.Problems = append(health.Problems, fmt.Sprintf("Codex config: %v", err))
		return health
	}
	if !featuresExist || features["hooks"] != true {
		health.Status = HookConfigurationDisabled
		health.Problems = append(health.Problems, "codex hooks feature flag is not enabled")
	}
	hooks, hooksExist, err := configMapSection(config, "hooks")
	if err != nil {
		health.Status = HookConfigurationMalformed
		health.Problems = append(health.Problems, fmt.Sprintf("Codex config: %v", err))
		return health
	}
	if !hooksExist {
		if health.Status != HookConfigurationDisabled {
			health.Status = HookConfigurationIncomplete
		}
		health.Problems = append(health.Problems, "codex hooks missing hooks table")
		return health
	}

	expected := codexHookCommand(command)
	for _, eventName := range []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Stop"} {
		inspectHookEvent(&health, "codex", hooks, eventName, expected)
	}
	return health
}

func inspectHookEvent(health *HookHealth, provider string, hooks map[string]any, eventName, expected string) {
	value, exists := hooks[eventName]
	commands := collectHookCommands(value)
	event := HookEventHealth{Name: eventName, Commands: commands}
	switch {
	case !exists || len(commands) == 0:
		event.Status = HookEventMissing
		health.Problems = append(health.Problems, fmt.Sprintf("%s hook %s has no hook definition", provider, eventName))
	case !containsHookCommand(commands, expected):
		event.Status = HookEventDifferentCommand
		health.Problems = append(health.Problems, fmt.Sprintf("%s hook %s uses a different command; expected %q", provider, eventName, expected))
	default:
		event.Status = HookEventConfigured
	}
	if event.Status != HookEventConfigured && health.Status == HookConfigurationConfigured {
		health.Status = HookConfigurationIncomplete
	}
	health.Events = append(health.Events, event)
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
