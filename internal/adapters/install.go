package adapters

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type Target string

const (
	TargetAuto   Target = "auto"
	TargetClaude Target = "claude"
	TargetCodex  Target = "codex"
	TargetAll    Target = "all"
	TargetNone   Target = "none"
)

type InstallResult struct {
	Target     Target
	ConfigPath string
	BackupPath string
	Installed  bool
}

func ResolveTargets(projectRoot string, target Target) ([]Target, error) {
	switch target {
	case TargetNone:
		return nil, nil
	case TargetClaude:
		return []Target{TargetClaude}, nil
	case TargetCodex:
		return []Target{TargetCodex}, nil
	case TargetAll:
		return []Target{TargetClaude, TargetCodex}, nil
	case TargetAuto, "":
		var targets []Target
		if pathExists(filepath.Join(projectRoot, ".claude")) || commandExists("claude") {
			targets = append(targets, TargetClaude)
		}
		if pathExists(filepath.Join(projectRoot, ".codex")) || commandExists("codex") {
			targets = append(targets, TargetCodex)
		}
		if len(targets) == 0 {
			targets = append(targets, TargetClaude, TargetCodex)
		}
		return targets, nil
	default:
		return nil, fmt.Errorf("invalid --agent %q; expected auto, claude, codex, all, or none", target)
	}
}

func Install(projectRoot string, targets []Target) ([]InstallResult, error) {
	results := make([]InstallResult, 0, len(targets))
	for _, target := range targets {
		switch target {
		case TargetClaude:
			result, err := InstallClaudeHook(projectRoot)
			if err != nil {
				return nil, err
			}
			results = append(results, result)
		case TargetCodex:
			result, err := InstallCodexHook(projectRoot)
			if err != nil {
				return nil, err
			}
			results = append(results, result)
		default:
			return nil, fmt.Errorf("unsupported adapter target %q", target)
		}
	}
	return results, nil
}

func InstallClaudeHook(projectRoot string) (InstallResult, error) {
	result := InstallResult{Target: TargetClaude}
	claudeDir := filepath.Join(projectRoot, ".claude")
	settingsPath := filepath.Join(claudeDir, "settings.json")
	result.ConfigPath = settingsPath

	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return result, fmt.Errorf("create .claude directory: %w", err)
	}

	settings := map[string]any{}
	if data, err := os.ReadFile(settingsPath); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &settings); err != nil {
			backupPath, err := backupFile(settingsPath)
			if err != nil {
				return result, fmt.Errorf("backup invalid Claude settings: %w", err)
			}
			result.BackupPath = backupPath
			settings = map[string]any{}
		}
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		settings["hooks"] = hooks
	}

	command := HookCommandPrefix()
	mergeHookCommand(hooks, "UserPromptSubmit", claudeUserHook(command))
	mergeHookCommand(hooks, "Stop", claudeAssistantHook(command))
	mergeHookCommand(hooks, "PostToolUse", claudeToolUseHook(command))

	output, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return result, fmt.Errorf("marshal Claude settings: %w", err)
	}
	output = append(output, '\n')
	if err := os.WriteFile(settingsPath, output, 0o644); err != nil {
		return result, fmt.Errorf("write Claude settings: %w", err)
	}

	result.Installed = true
	return result, nil
}

func InstallCodexHook(projectRoot string) (InstallResult, error) {
	result := InstallResult{Target: TargetCodex}
	codexDir := filepath.Join(projectRoot, ".codex")
	configPath := filepath.Join(codexDir, "config.toml")
	result.ConfigPath = configPath

	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		return result, fmt.Errorf("create .codex directory: %w", err)
	}

	config := map[string]any{}
	if data, err := os.ReadFile(configPath); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		if err := toml.Unmarshal(data, &config); err != nil {
			backupPath, err := backupFile(configPath)
			if err != nil {
				return result, fmt.Errorf("backup invalid Codex config: %w", err)
			}
			result.BackupPath = backupPath
			config = map[string]any{}
		}
	}

	hooks, _ := config["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		config["hooks"] = hooks
	}

	command := HookCommandPrefix()
	for _, eventName := range []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Stop"} {
		mergeHookCommand(hooks, eventName, codexHookCommand(command))
	}
	enableCodexHooksFeature(config)

	output, err := toml.Marshal(config)
	if err != nil {
		return result, fmt.Errorf("marshal Codex config: %w", err)
	}
	if err := os.WriteFile(configPath, output, 0o644); err != nil {
		return result, fmt.Errorf("write Codex config: %w", err)
	}

	result.Installed = true
	return result, nil
}

func enableCodexHooksFeature(config map[string]any) {
	features, _ := config["features"].(map[string]any)
	if features == nil {
		features = map[string]any{}
		config["features"] = features
	}
	features["hooks"] = true
}

func mergeHookCommand(hooks map[string]any, eventName, command string) {
	groups := filterAgentVCSHookCommands(normalizeHookGroups(hooks[eventName]))
	hooks[eventName] = append(groups, hookGroup(command))
}

func normalizeHookArray(value any) ([]any, bool) {
	switch typed := value.(type) {
	case nil:
		return nil, false
	case []any:
		return typed, true
	case []map[string]any:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		return items, true
	case map[string]any:
		return []any{typed}, true
	default:
		return []any{typed}, true
	}
}

func normalizeHookGroups(value any) []any {
	if command, ok := value.(string); ok {
		return []any{hookGroup(command)}
	}
	groups, _ := normalizeHookArray(value)
	return groups
}

func filterAgentVCSHookCommands(groups []any) []any {
	filtered := make([]any, 0, len(groups))
	for _, group := range groups {
		groupMap, ok := group.(map[string]any)
		if !ok {
			filtered = append(filtered, group)
			continue
		}

		hookEntries, hasHooks := normalizeHookArray(groupMap["hooks"])
		if !hasHooks {
			filtered = append(filtered, group)
			continue
		}

		nextHookEntries := make([]any, 0, len(hookEntries))
		for _, hookEntry := range hookEntries {
			hookMap, ok := hookEntry.(map[string]any)
			if !ok {
				nextHookEntries = append(nextHookEntries, hookEntry)
				continue
			}
			command, _ := hookMap["command"].(string)
			if IsAgentVCSHookCommand(command) {
				continue
			}
			nextHookEntries = append(nextHookEntries, hookEntry)
		}
		if len(nextHookEntries) == 0 {
			continue
		}

		nextGroup := map[string]any{}
		for key, value := range groupMap {
			nextGroup[key] = value
		}
		nextGroup["hooks"] = nextHookEntries
		filtered = append(filtered, nextGroup)
	}
	return filtered
}

func hookGroup(command string) map[string]any {
	return map[string]any{
		"matcher": "",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": command,
			},
		},
	}
}

func IsAgentVCSHookCommand(command string) bool {
	fields := strings.Fields(command)
	if len(fields) < 2 {
		return false
	}
	for index, field := range fields {
		switch field {
		case "claude-hook", "codex-hook":
			return index > 0
		}
	}
	return false
}

func HookCommandPrefix() string {
	if configured := strings.TrimSpace(os.Getenv("AGENT_VCS_HOOK_COMMAND")); configured != "" {
		return configured
	}

	executable, err := os.Executable()
	if err != nil {
		return "agent-vcs"
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "agent-vcs"
	}
	switch filepath.Base(executable) {
	case "agent-vcs", "acs":
	default:
		return "agent-vcs"
	}
	if isUnderTempDir(executable) {
		return "agent-vcs"
	}
	return shellQuote(executable)
}

func claudeUserHook(commandPrefix string) string {
	return commandPrefix + " claude-hook user"
}

func claudeAssistantHook(commandPrefix string) string {
	return commandPrefix + " claude-hook assistant"
}

func claudeToolUseHook(commandPrefix string) string {
	return commandPrefix + " claude-hook tool-use"
}

func codexHookCommand(commandPrefix string) string {
	return commandPrefix + " codex-hook"
}

func isUnderTempDir(path string) bool {
	rel, err := filepath.Rel(os.TempDir(), path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != "" && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n'\"\\$`") {
		return value
	}
	return strconv.Quote(value)
}

func backupFile(path string) (string, error) {
	backupPath := path + ".backup"
	return backupPath, os.Rename(path, backupPath)
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
