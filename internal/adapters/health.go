package adapters

import (
	"bytes"
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
		case TargetCursor:
			health = append(health, inspectCursorHooks(projectRoot, command))
		case TargetPi:
			health = append(health, inspectPiExtension(projectRoot, command))
		}
	}
	return health
}

func inspectAllHooks(projectRoot string, command string) []HookHealth {
	return []HookHealth{
		inspectClaudeHooks(projectRoot, command),
		inspectCodexHooks(projectRoot, command),
		inspectCursorHooks(projectRoot, command),
		inspectPiExtension(projectRoot, command),
	}
}

func inspectCursorHooks(projectRoot string, command string) HookHealth {
	configPath := filepath.Join(projectRoot, ".cursor", "hooks.json")
	health := HookHealth{Target: TargetCursor, ConfigPath: configPath, Status: HookConfigurationConfigured}
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		health.Status = HookConfigurationMissing
		health.Problems = append(health.Problems, fmt.Sprintf("cursor hooks missing: %s", configPath))
		return health
	}
	if err != nil {
		health.Status = HookConfigurationMalformed
		health.Problems = append(health.Problems, fmt.Sprintf("read cursor hooks: %v", err))
		return health
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		health.Status = HookConfigurationMalformed
		health.Problems = append(health.Problems, fmt.Sprintf("parse cursor hooks: %v", err))
		return health
	}
	if err := validateCursorHookVersion(config); err != nil {
		health.Status = HookConfigurationMalformed
		health.Problems = append(health.Problems, fmt.Sprintf("Cursor hooks: %v", err))
		return health
	}
	hooks, exists, err := configMapSection(config, "hooks")
	if err != nil {
		health.Status = HookConfigurationMalformed
		health.Problems = append(health.Problems, fmt.Sprintf("Cursor hooks: %v", err))
		return health
	}
	if !exists {
		health.Status = HookConfigurationIncomplete
		health.Problems = append(health.Problems, "cursor hooks missing hooks table")
		return health
	}
	for _, eventName := range cursorHookEvents {
		inspectCursorHookEvent(&health, hooks, eventName, cursorHookCommand(command, eventName))
	}
	return health
}

func inspectCursorHookEvent(health *HookHealth, hooks map[string]any, eventName, expected string) {
	value, exists := hooks[eventName]
	entries, _ := normalizeHookArray(value)
	commands := make([]string, 0, len(entries))
	for _, entry := range entries {
		entryMap, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if command, _ := entryMap["command"].(string); command != "" {
			commands = append(commands, command)
		}
	}
	event := HookEventHealth{Name: eventName, Commands: commands}
	switch {
	case !exists || len(commands) == 0:
		event.Status = HookEventMissing
		health.Problems = append(health.Problems, fmt.Sprintf("cursor hook %s has no hook definition", eventName))
	case !containsHookCommand(commands, expected):
		event.Status = HookEventDifferentCommand
		health.Problems = append(health.Problems, fmt.Sprintf("cursor hook %s uses a different command; expected %q", eventName, expected))
	default:
		event.Status = HookEventConfigured
	}
	if event.Status != HookEventConfigured && health.Status == HookConfigurationConfigured {
		health.Status = HookConfigurationIncomplete
	}
	health.Events = append(health.Events, event)
}

func inspectPiExtension(projectRoot, command string) HookHealth {
	extensionPath := filepath.Join(projectRoot, ".pi", "extensions", "turnal.ts")
	health := HookHealth{Target: TargetPi, ConfigPath: extensionPath, Status: HookConfigurationConfigured}
	data, err := os.ReadFile(extensionPath)
	if os.IsNotExist(err) {
		health.Status = HookConfigurationMissing
		health.Problems = append(health.Problems, fmt.Sprintf("pi extension missing: %s", extensionPath))
		return health
	}
	if err != nil {
		health.Status = HookConfigurationMalformed
		health.Problems = append(health.Problems, fmt.Sprintf("read pi extension: %v", err))
		return health
	}
	event := HookEventHealth{Name: "extension", Commands: []string{extensionPath}}
	expected, expectedErr := piExtensionForCommand(InstallOptions{HookCommand: command}.hookCommand())
	switch {
	case expectedErr != nil:
		health.Status = HookConfigurationMalformed
		event.Status = HookEventDifferentCommand
		health.Problems = append(health.Problems, expectedErr.Error())
	case !bytes.HasPrefix(data, []byte(piExtensionMarker)):
		health.Status = HookConfigurationMalformed
		event.Status = HookEventDifferentCommand
		health.Problems = append(health.Problems, "pi extension at the Turnal path is not managed by Turnal")
	case !bytes.Equal(data, expected):
		health.Status = HookConfigurationIncomplete
		event.Status = HookEventDifferentCommand
		health.Problems = append(health.Problems, "pi extension is stale; run turnal init --agent pi")
	default:
		event.Status = HookEventConfigured
	}
	health.Events = append(health.Events, event)
	return health
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
		{"SessionStart", claudeSessionHook(command)},
		{"UserPromptSubmit", claudeUserHook(command)},
		{"PreToolUse", claudePreToolUseHook(command)},
		{"PostToolUse", claudeToolUseHook(command)},
		{"PostToolUseFailure", claudeToolFailureHook(command)},
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
	for _, eventName := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"} {
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
