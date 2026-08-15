package adapters

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var cursorHookEvents = []string{
	"sessionStart",
	"beforeSubmitPrompt",
	"preToolUse",
	"postToolUse",
	"postToolUseFailure",
	"afterAgentResponse",
	"stop",
	"subagentStart",
}

const piExtensionMarker = "// Managed by Turnal. Re-run turnal init --agent pi to update."

//go:embed assets/pi/turnal.ts
var piExtensionSource []byte

func InstallCursorHook(projectRoot string) (InstallResult, error) {
	return InstallCursorHookWithOptions(projectRoot, InstallOptions{})
}

func InstallCursorHookWithOptions(projectRoot string, opts InstallOptions) (InstallResult, error) {
	result := InstallResult{Target: TargetCursor}
	cursorDir := filepath.Join(projectRoot, ".cursor")
	configPath := filepath.Join(cursorDir, "hooks.json")
	result.ConfigPath = configPath

	if err := os.MkdirAll(cursorDir, 0o755); err != nil {
		return result, fmt.Errorf("create .cursor directory: %w", err)
	}
	lock, err := acquireConfigLock(projectRoot, TargetCursor)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Release() }()

	config := map[string]any{}
	if data, readErr := os.ReadFile(configPath); readErr == nil && len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &config); err != nil {
			return result, fmt.Errorf("parse Cursor hooks %s; refusing to replace invalid or unexpected config: %w", configPath, err)
		}
	} else if readErr != nil && !os.IsNotExist(readErr) {
		return result, fmt.Errorf("read Cursor hooks: %w", readErr)
	}
	before, err := json.Marshal(config)
	if err != nil {
		return result, fmt.Errorf("marshal existing Cursor hooks: %w", err)
	}
	if err := validateCursorHookVersion(config); err != nil {
		return result, fmt.Errorf("Cursor hooks: %w", err)
	}

	hooks, exists, err := configMapSection(config, "hooks")
	if err != nil {
		return result, fmt.Errorf("Cursor hooks: %w", err)
	}
	if !exists {
		hooks = map[string]any{}
		config["hooks"] = hooks
	}
	if _, exists := config["version"]; !exists {
		config["version"] = float64(1)
	}
	command := opts.hookCommand()
	for _, eventName := range cursorHookEvents {
		mergeCursorHookCommand(hooks, eventName, cursorHookCommand(command, eventName))
	}

	after, err := json.Marshal(config)
	if err != nil {
		return result, fmt.Errorf("marshal updated Cursor hooks: %w", err)
	}
	if bytes.Equal(before, after) {
		result.Installed = true
		return result, nil
	}
	output, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return result, fmt.Errorf("format Cursor hooks: %w", err)
	}
	output = append(output, '\n')
	if err := writeConfigAtomic(configPath, output, existingFileMode(configPath, 0o644)); err != nil {
		return result, fmt.Errorf("write Cursor hooks: %w", err)
	}
	result.Installed = true
	return result, nil
}

func UninstallCursorHook(projectRoot string) (UninstallResult, error) {
	return UninstallCursorHookWithOptions(projectRoot, UninstallOptions{})
}

func UninstallCursorHookWithOptions(projectRoot string, opts UninstallOptions) (UninstallResult, error) {
	result := UninstallResult{Target: TargetCursor, DryRun: opts.DryRun}
	configPath := filepath.Join(projectRoot, ".cursor", "hooks.json")
	result.ConfigPath = configPath
	lock, err := acquireConfigLock(projectRoot, TargetCursor)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Release() }()

	info, err := os.Stat(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, fmt.Errorf("stat Cursor hooks: %w", err)
	}
	if info.IsDir() {
		return result, fmt.Errorf("Cursor hooks is a directory: %s", configPath)
	}
	result.ConfigExists = true
	data, err := os.ReadFile(configPath)
	if err != nil {
		return result, fmt.Errorf("read Cursor hooks: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return result, nil
	}
	config := map[string]any{}
	if err := json.Unmarshal(data, &config); err != nil {
		return result, fmt.Errorf("parse Cursor hooks %s: %w", configPath, err)
	}
	hooks, exists, err := configMapSection(config, "hooks")
	if err != nil {
		return result, fmt.Errorf("Cursor hooks: %w", err)
	}
	if !exists {
		return result, nil
	}
	result.RemovedCommands, result.Changed = removeTurnalCursorHookCommands(hooks)
	if !result.Changed || opts.DryRun {
		return result, nil
	}
	if len(hooks) == 0 {
		delete(config, "hooks")
	}
	output, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return result, fmt.Errorf("format Cursor hooks: %w", err)
	}
	output = append(output, '\n')
	if err := writeConfigAtomic(configPath, output, info.Mode().Perm()); err != nil {
		return result, fmt.Errorf("write Cursor hooks: %w", err)
	}
	return result, nil
}

func mergeCursorHookCommand(hooks map[string]any, eventName, command string) {
	entries, _ := normalizeHookArray(hooks[eventName])
	filtered, _ := filterTurnalCursorHookCommands(entries)
	hooks[eventName] = append(filtered, map[string]any{"command": command})
}

func removeTurnalCursorHookCommands(hooks map[string]any) (int, bool) {
	removedTotal := 0
	for eventName, value := range hooks {
		entries, _ := normalizeHookArray(value)
		filtered, removed := filterTurnalCursorHookCommands(entries)
		if removed == 0 {
			continue
		}
		removedTotal += removed
		if len(filtered) == 0 {
			delete(hooks, eventName)
		} else {
			hooks[eventName] = filtered
		}
	}
	return removedTotal, removedTotal > 0
}

func filterTurnalCursorHookCommands(entries []any) ([]any, int) {
	filtered := make([]any, 0, len(entries))
	removed := 0
	for _, entry := range entries {
		entryMap, ok := entry.(map[string]any)
		if !ok {
			filtered = append(filtered, entry)
			continue
		}
		command, _ := entryMap["command"].(string)
		if IsTurnalHookCommand(command) {
			removed++
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered, removed
}

func cursorHookCommand(commandPrefix, eventName string) string {
	return commandPrefix + " adapter capture cursor " + eventName
}

func validateCursorHookVersion(config map[string]any) error {
	value, exists := config["version"]
	if !exists {
		return nil
	}
	version, ok := value.(float64)
	if !ok || version != 1 {
		return fmt.Errorf("version must be 1, got %v", value)
	}
	return nil
}

func InstallPiExtension(projectRoot string) (InstallResult, error) {
	return InstallPiExtensionWithOptions(projectRoot, InstallOptions{})
}

func InstallPiExtensionWithOptions(projectRoot string, opts InstallOptions) (InstallResult, error) {
	result := InstallResult{Target: TargetPi}
	extensionDir := filepath.Join(projectRoot, ".pi", "extensions")
	extensionPath := filepath.Join(extensionDir, "turnal.ts")
	result.ConfigPath = extensionPath
	if err := os.MkdirAll(extensionDir, 0o755); err != nil {
		return result, fmt.Errorf("create Pi extensions directory: %w", err)
	}
	lock, err := acquireConfigLock(projectRoot, TargetPi)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Release() }()

	expected, err := piExtensionForCommand(opts.hookCommand())
	if err != nil {
		return result, err
	}
	if existing, readErr := os.ReadFile(extensionPath); readErr == nil {
		if bytes.Equal(existing, expected) {
			result.Installed = true
			return result, nil
		}
		if !bytes.HasPrefix(existing, []byte(piExtensionMarker)) {
			return result, fmt.Errorf("Pi extension %s is not managed by Turnal; refusing to replace it", extensionPath)
		}
	} else if !os.IsNotExist(readErr) {
		return result, fmt.Errorf("read Pi extension: %w", readErr)
	}
	if err := writeConfigAtomic(extensionPath, expected, existingFileMode(extensionPath, 0o644)); err != nil {
		return result, fmt.Errorf("write Pi extension: %w", err)
	}
	result.Installed = true
	return result, nil
}

func piExtensionForCommand(command string) ([]byte, error) {
	encoded, err := json.Marshal(command)
	if err != nil {
		return nil, fmt.Errorf("encode Pi Turnal command: %w", err)
	}
	const declaration = `const turnalCommand = "turnal";`
	if !bytes.Contains(piExtensionSource, []byte(declaration)) {
		return nil, fmt.Errorf("embedded Pi extension is missing its command declaration")
	}
	replacement := append([]byte("const turnalCommand = "), encoded...)
	replacement = append(replacement, ';')
	return bytes.Replace(piExtensionSource, []byte(declaration), replacement, 1), nil
}

func UninstallPiExtension(projectRoot string) (UninstallResult, error) {
	return UninstallPiExtensionWithOptions(projectRoot, UninstallOptions{})
}

func UninstallPiExtensionWithOptions(projectRoot string, opts UninstallOptions) (UninstallResult, error) {
	result := UninstallResult{Target: TargetPi, DryRun: opts.DryRun}
	extensionPath := filepath.Join(projectRoot, ".pi", "extensions", "turnal.ts")
	result.ConfigPath = extensionPath
	lock, err := acquireConfigLock(projectRoot, TargetPi)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Release() }()

	info, err := os.Stat(extensionPath)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, fmt.Errorf("stat Pi extension: %w", err)
	}
	if info.IsDir() {
		return result, fmt.Errorf("Pi extension is a directory: %s", extensionPath)
	}
	result.ConfigExists = true
	data, err := os.ReadFile(extensionPath)
	if err != nil {
		return result, fmt.Errorf("read Pi extension: %w", err)
	}
	if !bytes.HasPrefix(data, []byte(piExtensionMarker)) {
		return result, nil
	}
	result.RemovedCommands = 1
	result.Changed = true
	if opts.DryRun {
		return result, nil
	}
	if err := os.Remove(extensionPath); err != nil {
		return result, fmt.Errorf("remove Pi extension: %w", err)
	}
	return result, nil
}
