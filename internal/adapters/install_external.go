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
	"afterFileEdit",
	"afterAgentResponse",
	"stop",
	"subagentStart",
}

var copilotHookEvents = []string{
	"sessionStart",
	"userPromptSubmitted",
	"preToolUse",
	"postToolUse",
	"errorOccurred",
	"agentStop",
}

var geminiHookEvents = []string{
	"SessionStart",
	"BeforeAgent",
	"BeforeTool",
	"AfterTool",
	"AfterAgent",
	"SessionEnd",
}

const piExtensionMarker = "// Managed by Turnal. Re-run turnal init --agent pi to update."
const openCodePluginMarker = "// Managed by Turnal. Re-run turnal init --agent opencode to update."

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

func InstallCopilotHookWithOptions(projectRoot string, opts InstallOptions) (InstallResult, error) {
	result := InstallResult{Target: TargetCopilot}
	hooksDir := filepath.Join(projectRoot, ".github", "hooks")
	configPath := filepath.Join(hooksDir, "turnal.json")
	result.ConfigPath = configPath
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return result, fmt.Errorf("create GitHub Copilot CLI hooks directory: %w", err)
	}
	lock, err := acquireConfigLock(projectRoot, TargetCopilot)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Release() }()
	config := map[string]any{}
	if data, readErr := os.ReadFile(configPath); readErr == nil && len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &config); err != nil {
			return result, fmt.Errorf("parse GitHub Copilot CLI hooks %s; refusing to replace invalid config: %w", configPath, err)
		}
	} else if readErr != nil && !os.IsNotExist(readErr) {
		return result, fmt.Errorf("read GitHub Copilot CLI hooks: %w", readErr)
	}
	before, err := json.Marshal(config)
	if err != nil {
		return result, fmt.Errorf("marshal existing GitHub Copilot CLI hooks: %w", err)
	}
	if err := validateCursorHookVersion(config); err != nil {
		return result, fmt.Errorf("GitHub Copilot CLI hooks: %w", err)
	}
	hooks, exists, err := configMapSection(config, "hooks")
	if err != nil {
		return result, fmt.Errorf("GitHub Copilot CLI hooks: %w", err)
	}
	if !exists {
		hooks = map[string]any{}
		config["hooks"] = hooks
	}
	if _, exists := config["version"]; !exists {
		config["version"] = float64(1)
	}
	for _, eventName := range copilotHookEvents {
		entries, _ := normalizeHookArray(hooks[eventName])
		filtered, _ := filterTurnalCopilotHookCommands(entries)
		command := copilotHookCommand(opts.hookCommand(), eventName)
		hooks[eventName] = append(filtered, map[string]any{
			"type":       "command",
			"bash":       command,
			"powershell": copilotPowerShellHookCommand(opts.hookCommand(), eventName),
		})
	}
	after, err := json.Marshal(config)
	if err != nil {
		return result, fmt.Errorf("marshal updated GitHub Copilot CLI hooks: %w", err)
	}
	if bytes.Equal(before, after) {
		result.Installed = true
		return result, nil
	}
	output, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return result, fmt.Errorf("format GitHub Copilot CLI hooks: %w", err)
	}
	output = append(output, '\n')
	if err := writeConfigAtomic(configPath, output, existingFileMode(configPath, 0o644)); err != nil {
		return result, fmt.Errorf("write GitHub Copilot CLI hooks: %w", err)
	}
	result.Installed = true
	return result, nil
}

func UninstallCopilotHookWithOptions(projectRoot string, opts UninstallOptions) (UninstallResult, error) {
	result := UninstallResult{Target: TargetCopilot, DryRun: opts.DryRun}
	configPath := filepath.Join(projectRoot, ".github", "hooks", "turnal.json")
	result.ConfigPath = configPath
	lock, err := acquireConfigLock(projectRoot, TargetCopilot)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Release() }()
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, fmt.Errorf("read GitHub Copilot CLI hooks: %w", err)
	}
	result.ConfigExists = true
	config := map[string]any{}
	if err := json.Unmarshal(data, &config); err != nil {
		return result, fmt.Errorf("parse GitHub Copilot CLI hooks %s: %w", configPath, err)
	}
	hooks, exists, err := configMapSection(config, "hooks")
	if err != nil {
		return result, fmt.Errorf("GitHub Copilot CLI hooks: %w", err)
	}
	if !exists {
		return result, nil
	}
	result.RemovedCommands, result.Changed = removeTurnalCopilotHookCommands(hooks)
	if !result.Changed || opts.DryRun {
		return result, nil
	}
	if len(hooks) == 0 {
		delete(config, "hooks")
	}
	if copilotConfigIsEmpty(config) {
		if err := os.Remove(configPath); err != nil {
			return result, fmt.Errorf("remove GitHub Copilot CLI hooks: %w", err)
		}
		_ = os.Remove(filepath.Dir(configPath))
		return result, nil
	}
	output, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return result, fmt.Errorf("format GitHub Copilot CLI hooks: %w", err)
	}
	output = append(output, '\n')
	if err := writeConfigAtomic(configPath, output, existingFileMode(configPath, 0o644)); err != nil {
		return result, fmt.Errorf("write GitHub Copilot CLI hooks: %w", err)
	}
	return result, nil
}

func copilotHookCommand(commandPrefix, eventName string) string {
	commandPrefix = strings.TrimSpace(commandPrefix)
	quoted := len(commandPrefix) > 1 && ((commandPrefix[0] == '\'' && commandPrefix[len(commandPrefix)-1] == '\'') || (commandPrefix[0] == '"' && commandPrefix[len(commandPrefix)-1] == '"'))
	if !quoted && strings.ContainsAny(commandPrefix, " \t") {
		commandPrefix = "'" + strings.ReplaceAll(commandPrefix, "'", `'\''`) + "'"
	}
	return commandPrefix + " adapter capture copilot-cli " + eventName
}

func copilotPowerShellHookCommand(commandPrefix, eventName string) string {
	commandPrefix = strings.TrimSpace(commandPrefix)
	if len(commandPrefix) > 1 && ((commandPrefix[0] == '\'' && commandPrefix[len(commandPrefix)-1] == '\'') || (commandPrefix[0] == '"' && commandPrefix[len(commandPrefix)-1] == '"')) {
		return "& " + commandPrefix + " adapter capture copilot-cli " + eventName
	}
	if strings.ContainsAny(commandPrefix, " \t") {
		commandPrefix = "'" + strings.ReplaceAll(commandPrefix, "'", "''") + "'"
	}
	return "& " + commandPrefix + " adapter capture copilot-cli " + eventName
}

func copilotConfigIsEmpty(config map[string]any) bool {
	for key := range config {
		if key != "version" {
			return false
		}
	}
	return true
}

func removeTurnalCopilotHookCommands(hooks map[string]any) (int, bool) {
	removedTotal := 0
	for eventName, value := range hooks {
		entries, _ := normalizeHookArray(value)
		filtered, removed := filterTurnalCopilotHookCommands(entries)
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

func filterTurnalCopilotHookCommands(entries []any) ([]any, int) {
	filtered := make([]any, 0, len(entries))
	removed := 0
	for _, entry := range entries {
		entryMap, ok := entry.(map[string]any)
		if !ok {
			filtered = append(filtered, entry)
			continue
		}
		managed := false
		for _, field := range []string{"bash", "powershell", "command"} {
			command, _ := entryMap[field].(string)
			if IsTurnalHookCommand(strings.TrimSpace(strings.TrimPrefix(command, "&"))) {
				managed = true
				break
			}
		}
		if managed {
			removed++
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered, removed
}

func InstallGeminiHookWithOptions(projectRoot string, opts InstallOptions) (InstallResult, error) {
	result := InstallResult{Target: TargetGemini}
	geminiDir := filepath.Join(projectRoot, ".gemini")
	settingsPath := filepath.Join(geminiDir, "settings.json")
	result.ConfigPath = settingsPath
	if err := os.MkdirAll(geminiDir, 0o755); err != nil {
		return result, fmt.Errorf("create Gemini settings directory: %w", err)
	}
	lock, err := acquireConfigLock(projectRoot, TargetGemini)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Release() }()
	settings := map[string]any{}
	if data, readErr := os.ReadFile(settingsPath); readErr == nil && len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &settings); err != nil {
			return result, fmt.Errorf("parse Gemini settings %s; refusing to replace invalid config: %w", settingsPath, err)
		}
	} else if readErr != nil && !os.IsNotExist(readErr) {
		return result, fmt.Errorf("read Gemini settings: %w", readErr)
	}
	before, err := json.Marshal(settings)
	if err != nil {
		return result, fmt.Errorf("marshal existing Gemini settings: %w", err)
	}
	hooks, exists, err := configMapSection(settings, "hooks")
	if err != nil {
		return result, fmt.Errorf("Gemini settings: %w", err)
	}
	if !exists {
		hooks = map[string]any{}
		settings["hooks"] = hooks
	}
	for _, eventName := range geminiHookEvents {
		groups, _ := normalizeHookArray(hooks[eventName])
		filtered, _ := filterTurnalGeminiHookCommands(groups)
		hooks[eventName] = append(filtered, map[string]any{
			"matcher": "*",
			"hooks": []any{map[string]any{
				"name":    "turnal",
				"type":    "command",
				"command": geminiHookCommand(opts.hookCommand(), eventName),
			}},
		})
	}
	after, err := json.Marshal(settings)
	if err != nil {
		return result, fmt.Errorf("marshal updated Gemini settings: %w", err)
	}
	if bytes.Equal(before, after) {
		result.Installed = true
		return result, nil
	}
	output, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return result, fmt.Errorf("format Gemini settings: %w", err)
	}
	output = append(output, '\n')
	if err := writeConfigAtomic(settingsPath, output, existingFileMode(settingsPath, 0o644)); err != nil {
		return result, fmt.Errorf("write Gemini settings: %w", err)
	}
	result.Installed = true
	return result, nil
}

func UninstallGeminiHookWithOptions(projectRoot string, opts UninstallOptions) (UninstallResult, error) {
	result := UninstallResult{Target: TargetGemini, DryRun: opts.DryRun}
	settingsPath := filepath.Join(projectRoot, ".gemini", "settings.json")
	result.ConfigPath = settingsPath
	lock, err := acquireConfigLock(projectRoot, TargetGemini)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Release() }()
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, fmt.Errorf("read Gemini settings: %w", err)
	}
	result.ConfigExists = true
	settings := map[string]any{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return result, fmt.Errorf("parse Gemini settings %s: %w", settingsPath, err)
	}
	hooks, exists, err := configMapSection(settings, "hooks")
	if err != nil {
		return result, fmt.Errorf("Gemini settings: %w", err)
	}
	if !exists {
		return result, nil
	}
	for eventName, value := range hooks {
		groups, _ := normalizeHookArray(value)
		filtered, removed := filterTurnalGeminiHookCommands(groups)
		if removed == 0 {
			continue
		}
		result.RemovedCommands += removed
		result.Changed = true
		if len(filtered) == 0 {
			delete(hooks, eventName)
		} else {
			hooks[eventName] = filtered
		}
	}
	if !result.Changed || opts.DryRun {
		return result, nil
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	}
	output, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return result, fmt.Errorf("format Gemini settings: %w", err)
	}
	output = append(output, '\n')
	if err := writeConfigAtomic(settingsPath, output, existingFileMode(settingsPath, 0o644)); err != nil {
		return result, fmt.Errorf("write Gemini settings: %w", err)
	}
	return result, nil
}

func filterTurnalGeminiHookCommands(groups []any) ([]any, int) {
	filteredGroups := make([]any, 0, len(groups))
	removed := 0
	for _, group := range groups {
		groupMap, ok := group.(map[string]any)
		if !ok {
			filteredGroups = append(filteredGroups, group)
			continue
		}
		entries, _ := normalizeHookArray(groupMap["hooks"])
		filteredEntries := make([]any, 0, len(entries))
		for _, entry := range entries {
			entryMap, ok := entry.(map[string]any)
			command, _ := entryMap["command"].(string)
			if ok && IsTurnalHookCommand(command) {
				removed++
				continue
			}
			filteredEntries = append(filteredEntries, entry)
		}
		if len(filteredEntries) == 0 {
			continue
		}
		copyGroup := make(map[string]any, len(groupMap))
		for key, value := range groupMap {
			copyGroup[key] = value
		}
		copyGroup["hooks"] = filteredEntries
		filteredGroups = append(filteredGroups, copyGroup)
	}
	return filteredGroups, removed
}

func geminiHookCommand(commandPrefix, eventName string) string {
	return commandPrefix + " adapter capture gemini-cli " + eventName
}

func InstallOpenCodePluginWithOptions(projectRoot string, opts InstallOptions) (InstallResult, error) {
	result := InstallResult{Target: TargetOpenCode}
	pluginDir := filepath.Join(projectRoot, ".opencode", "plugins")
	pluginPath := filepath.Join(pluginDir, "turnal.js")
	result.ConfigPath = pluginPath
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		return result, fmt.Errorf("create OpenCode plugins directory: %w", err)
	}
	lock, err := acquireConfigLock(projectRoot, TargetOpenCode)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Release() }()
	expected, err := openCodePluginForCommand(opts.piCommand())
	if err != nil {
		return result, err
	}
	if existing, readErr := os.ReadFile(pluginPath); readErr == nil {
		if bytes.Equal(existing, expected) {
			result.Installed = true
			return result, nil
		}
		if !bytes.HasPrefix(existing, []byte(openCodePluginMarker)) {
			return result, fmt.Errorf("OpenCode plugin %s is not managed by Turnal; refusing to replace it", pluginPath)
		}
	} else if !os.IsNotExist(readErr) {
		return result, fmt.Errorf("read OpenCode plugin: %w", readErr)
	}
	if err := writeConfigAtomic(pluginPath, expected, existingFileMode(pluginPath, 0o644)); err != nil {
		return result, fmt.Errorf("write OpenCode plugin: %w", err)
	}
	result.Installed = true
	return result, nil
}

func openCodePluginForCommand(command string) ([]byte, error) {
	encoded, err := json.Marshal(command)
	if err != nil {
		return nil, fmt.Errorf("encode OpenCode Turnal command: %w", err)
	}
	return []byte(fmt.Sprintf(`%s
const turnalCommand = %s;

export const TurnalPlugin = async ({ directory }) => {
	const messages = new Map();
	const pendingParts = new Map();
	const assistants = new Map();
	const capture = async (hook, payload) => {
    const child = Bun.spawn(
      [turnalCommand, "adapter", "capture", "opencode", hook],
      { stdin: new Blob([JSON.stringify({ directory, ...payload })]), stdout: "ignore", stderr: "inherit" },
    );
		await child.exited;
	};
	const messageModel = (info) => info?.modelID ?? info?.model?.modelID ?? "";
	const messageText = (value) => {
		if (typeof value !== "string") return "";
		try {
			const decoded = JSON.parse(value);
			return typeof decoded === "string" ? decoded : value;
		} catch {
			return value;
		}
	};
	const handlePart = async (properties) => {
			const part = properties.part ?? {};
			const message = messages.get(part.messageID);
			if (!message) {
				const pending = pendingParts.get(part.messageID) ?? [];
				pending.push(properties);
				pendingParts.set(part.messageID, pending);
				return;
			}
			if (part.type !== "text" || part.ignored) return;
			if (message.role === "user") {
				await capture("user.completed", {
					session_id: message.sessionID,
					text: messageText(part.text),
					model: message.model,
					source_id: part.id,
				});
				return;
			}
			if (message.role === "assistant") {
				const settled = assistants.get(message.sessionID) ?? { model: message.model, parts: new Map() };
				settled.model = message.model || settled.model;
				const delta = messageText(properties.delta);
				settled.parts.set(part.id, delta ? (settled.parts.get(part.id) ?? "") + delta : messageText(part.text));
				assistants.set(message.sessionID, settled);
			}
	};
	const handleEvent = async (event) => {
		const properties = event?.properties ?? {};
		if (event?.type === "message.updated") {
			const info = properties.info ?? {};
			messages.set(info.id, {
				role: info.role,
				sessionID: info.sessionID ?? properties.sessionID,
				model: messageModel(info),
			});
			const pending = pendingParts.get(info.id) ?? [];
			pendingParts.delete(info.id);
			for (const partProperties of pending) await handlePart(partProperties);
			return;
		}
		if (event?.type === "message.part.updated") {
			await handlePart(properties);
			return;
		}
		if (event?.type === "session.idle") {
			const sessionID = properties.sessionID;
			const settled = assistants.get(sessionID);
			if (settled) {
				await capture("assistant.completed", {
					session_id: sessionID,
					text: [...settled.parts.values()].join(""),
					model: settled.model,
					source_id: sessionID + ":assistant",
				});
				assistants.delete(sessionID);
			}
			await capture("event", { event });
			return;
		}
		await capture("event", { event });
	};

	return {
		event: async ({ event }) => handleEvent(event),
		"tool.execute.before": async (input, output) =>
			capture("tool.execute.before", { ...input, args: output.args }),
		"tool.execute.after": async (input, output) =>
			capture("tool.execute.after", {
				...input,
				output: output?.output ?? output,
				is_error: output?.error != null,
			}),
	};
};
`, openCodePluginMarker, encoded)), nil
}

func UninstallOpenCodePluginWithOptions(projectRoot string, opts UninstallOptions) (UninstallResult, error) {
	result := UninstallResult{Target: TargetOpenCode, DryRun: opts.DryRun}
	pluginPath := filepath.Join(projectRoot, ".opencode", "plugins", "turnal.js")
	result.ConfigPath = pluginPath
	lock, err := acquireConfigLock(projectRoot, TargetOpenCode)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Release() }()
	data, err := os.ReadFile(pluginPath)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, fmt.Errorf("read OpenCode plugin: %w", err)
	}
	result.ConfigExists = true
	if !bytes.HasPrefix(data, []byte(openCodePluginMarker)) {
		return result, nil
	}
	result.RemovedCommands = 1
	result.Changed = true
	if opts.DryRun {
		return result, nil
	}
	if err := os.Remove(pluginPath); err != nil {
		return result, fmt.Errorf("remove OpenCode plugin: %w", err)
	}
	return result, nil
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

	expected, err := piExtensionForCommand(opts.piCommand())
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
