package adapters

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/filelock"
	"github.com/AadiJo/turnal/internal/hookcmd"
	"github.com/AadiJo/turnal/internal/safepath"
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

type InstallOptions struct {
	HookCommand string
}

type UninstallResult struct {
	Target          Target
	ConfigPath      string
	ConfigExists    bool
	RemovedCommands int
	Changed         bool
	DryRun          bool
}

type UninstallOptions struct {
	DryRun bool
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
		if pathExists(filepath.Join(EffectiveHookRoot(projectRoot, TargetCodex), ".codex")) || commandExists("codex") {
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
	return InstallWithOptions(projectRoot, targets, InstallOptions{})
}

func InstallWithOptions(projectRoot string, targets []Target, opts InstallOptions) ([]InstallResult, error) {
	results := make([]InstallResult, 0, len(targets))
	for _, target := range targets {
		switch target {
		case TargetClaude:
			result, err := InstallClaudeHookWithOptions(projectRoot, opts)
			if err != nil {
				return nil, err
			}
			results = append(results, result)
		case TargetCodex:
			result, err := InstallCodexHookWithOptions(projectRoot, opts)
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

func Uninstall(projectRoot string, targets []Target) ([]UninstallResult, error) {
	return UninstallWithOptions(projectRoot, targets, UninstallOptions{})
}

func UninstallWithOptions(projectRoot string, targets []Target, opts UninstallOptions) ([]UninstallResult, error) {
	results := make([]UninstallResult, 0, len(targets))
	for _, target := range targets {
		switch target {
		case TargetClaude:
			result, err := UninstallClaudeHookWithOptions(projectRoot, opts)
			if err != nil {
				return nil, err
			}
			results = append(results, result)
		case TargetCodex:
			result, err := UninstallCodexHookWithOptions(projectRoot, opts)
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
	return InstallClaudeHookWithOptions(projectRoot, InstallOptions{})
}

func InstallClaudeHookWithOptions(projectRoot string, opts InstallOptions) (InstallResult, error) {
	result := InstallResult{Target: TargetClaude}
	claudeDir := filepath.Join(projectRoot, ".claude")
	settingsPath := filepath.Join(claudeDir, "settings.json")
	result.ConfigPath = settingsPath

	if err := safepath.MkdirAllNoSymlinks(projectRoot, ".claude", 0o755); err != nil {
		return result, fmt.Errorf("create .claude directory: %w", err)
	}
	if err := safepath.ValidateNoSymlinks(projectRoot, filepath.Join(".claude", "settings.json")); err != nil {
		return result, fmt.Errorf("inspect Claude settings: %w", err)
	}
	lock, err := acquireConfigLock(projectRoot, TargetClaude)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Release() }()

	settings := map[string]any{}
	if data, err := os.ReadFile(settingsPath); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &settings); err != nil {
			return result, fmt.Errorf("parse Claude settings %s; refusing to replace invalid or unexpected config: %w", settingsPath, err)
		}
	}
	before, err := json.Marshal(settings)
	if err != nil {
		return result, fmt.Errorf("marshal existing Claude settings: %w", err)
	}

	hooks, exists, err := configMapSection(settings, "hooks")
	if err != nil {
		return result, fmt.Errorf("Claude settings: %w", err)
	}
	if !exists {
		hooks = map[string]any{}
		settings["hooks"] = hooks
	}

	command := opts.hookCommand()
	mergeHookCommand(hooks, "SessionStart", claudeSessionHook(command))
	mergeHookCommand(hooks, "UserPromptSubmit", claudeUserHook(command))
	mergeHookCommand(hooks, "Stop", claudeAssistantHook(command))
	mergeHookCommand(hooks, "PreToolUse", claudePreToolUseHook(command))
	mergeHookCommand(hooks, "PostToolUse", claudeToolUseHook(command))
	mergeHookCommand(hooks, "PostToolUseFailure", claudeToolFailureHook(command))

	output, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return result, fmt.Errorf("marshal Claude settings: %w", err)
	}
	output = append(output, '\n')
	after, err := json.Marshal(settings)
	if err != nil {
		return result, fmt.Errorf("marshal updated Claude settings: %w", err)
	}
	if bytes.Equal(before, after) {
		result.Installed = true
		return result, nil
	}
	mode := existingFileMode(settingsPath, 0o644)
	if err := writeConfigAtomic(settingsPath, output, mode); err != nil {
		return result, fmt.Errorf("write Claude settings: %w", err)
	}

	result.Installed = true
	return result, nil
}

func UninstallClaudeHook(projectRoot string) (UninstallResult, error) {
	return UninstallClaudeHookWithOptions(projectRoot, UninstallOptions{})
}

func UninstallClaudeHookWithOptions(projectRoot string, opts UninstallOptions) (UninstallResult, error) {
	result := UninstallResult{Target: TargetClaude, DryRun: opts.DryRun}
	settingsPath := filepath.Join(projectRoot, ".claude", "settings.json")
	result.ConfigPath = settingsPath
	lock, err := acquireConfigLock(projectRoot, TargetClaude)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Release() }()

	info, err := os.Stat(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, fmt.Errorf("stat Claude settings: %w", err)
	}
	if info.IsDir() {
		return result, fmt.Errorf("Claude settings is a directory: %s", settingsPath)
	}
	result.ConfigExists = true

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return result, fmt.Errorf("read Claude settings: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return result, nil
	}

	settings := map[string]any{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return result, fmt.Errorf("parse Claude settings %s: %w", settingsPath, err)
	}

	hooks, exists, err := configMapSection(settings, "hooks")
	if err != nil {
		return result, fmt.Errorf("Claude settings: %w", err)
	}
	if !exists {
		return result, nil
	}
	result.RemovedCommands, result.Changed = removeTurnalHookCommands(hooks)
	if !result.Changed {
		return result, nil
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	}
	if opts.DryRun {
		return result, nil
	}

	output, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return result, fmt.Errorf("marshal Claude settings: %w", err)
	}
	output = append(output, '\n')
	if err := writeConfigAtomic(settingsPath, output, info.Mode().Perm()); err != nil {
		return result, fmt.Errorf("write Claude settings: %w", err)
	}
	return result, nil
}

func InstallCodexHook(projectRoot string) (InstallResult, error) {
	return InstallCodexHookWithOptions(projectRoot, InstallOptions{})
}

func InstallCodexHookWithOptions(projectRoot string, opts InstallOptions) (InstallResult, error) {
	projectRoot = EffectiveHookRoot(projectRoot, TargetCodex)
	result := InstallResult{Target: TargetCodex}
	codexDir := filepath.Join(projectRoot, ".codex")
	configPath := filepath.Join(codexDir, "config.toml")
	result.ConfigPath = configPath

	if err := safepath.MkdirAllNoSymlinks(projectRoot, ".codex", 0o755); err != nil {
		return result, fmt.Errorf("create .codex directory: %w", err)
	}
	if err := safepath.ValidateNoSymlinks(projectRoot, filepath.Join(".codex", "config.toml")); err != nil {
		return result, fmt.Errorf("inspect Codex config: %w", err)
	}
	lock, err := acquireConfigLock(projectRoot, TargetCodex)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Release() }()

	config := map[string]any{}
	if data, err := os.ReadFile(configPath); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		if err := toml.Unmarshal(data, &config); err != nil {
			return result, fmt.Errorf("parse Codex config %s; refusing to replace invalid or unexpected config: %w", configPath, err)
		}
	}
	before, err := toml.Marshal(config)
	if err != nil {
		return result, fmt.Errorf("marshal existing Codex config: %w", err)
	}

	hooks, exists, err := configMapSection(config, "hooks")
	if err != nil {
		return result, fmt.Errorf("Codex config: %w", err)
	}
	if !exists {
		hooks = map[string]any{}
		config["hooks"] = hooks
	}

	command := opts.hookCommand()
	for _, eventName := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"} {
		mergeHookCommand(hooks, eventName, codexHookCommand(command))
	}
	if err := enableCodexHooksFeature(config); err != nil {
		return result, err
	}

	output, err := toml.Marshal(config)
	if err != nil {
		return result, fmt.Errorf("marshal Codex config: %w", err)
	}
	if bytes.Equal(before, output) {
		result.Installed = true
		return result, nil
	}
	mode := existingFileMode(configPath, 0o644)
	if err := writeConfigAtomic(configPath, output, mode); err != nil {
		return result, fmt.Errorf("write Codex config: %w", err)
	}

	result.Installed = true
	return result, nil
}

func UninstallCodexHook(projectRoot string) (UninstallResult, error) {
	return UninstallCodexHookWithOptions(projectRoot, UninstallOptions{})
}

func UninstallCodexHookWithOptions(projectRoot string, opts UninstallOptions) (UninstallResult, error) {
	projectRoot = EffectiveHookRoot(projectRoot, TargetCodex)
	result := UninstallResult{Target: TargetCodex, DryRun: opts.DryRun}
	configPath := filepath.Join(projectRoot, ".codex", "config.toml")
	result.ConfigPath = configPath
	lock, err := acquireConfigLock(projectRoot, TargetCodex)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Release() }()

	info, err := os.Stat(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, fmt.Errorf("stat Codex config: %w", err)
	}
	if info.IsDir() {
		return result, fmt.Errorf("Codex config is a directory: %s", configPath)
	}
	result.ConfigExists = true

	data, err := os.ReadFile(configPath)
	if err != nil {
		return result, fmt.Errorf("read Codex config: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return result, nil
	}

	config := map[string]any{}
	if err := toml.Unmarshal(data, &config); err != nil {
		return result, fmt.Errorf("parse Codex config %s: %w", configPath, err)
	}

	hooks, exists, err := configMapSection(config, "hooks")
	if err != nil {
		return result, fmt.Errorf("Codex config: %w", err)
	}
	if !exists {
		return result, nil
	}
	result.RemovedCommands, result.Changed = removeTurnalHookCommands(hooks)
	if !result.Changed {
		return result, nil
	}
	if len(hooks) == 0 {
		delete(config, "hooks")
		disableCodexHooksFeature(config)
	}
	if opts.DryRun {
		return result, nil
	}

	output, err := toml.Marshal(config)
	if err != nil {
		return result, fmt.Errorf("marshal Codex config: %w", err)
	}
	if err := writeConfigAtomic(configPath, output, info.Mode().Perm()); err != nil {
		return result, fmt.Errorf("write Codex config: %w", err)
	}
	return result, nil
}

func (opts InstallOptions) hookCommand() string {
	if configured := strings.TrimSpace(opts.HookCommand); configured != "" {
		return configured
	}
	return hookcmd.Default()
}

func enableCodexHooksFeature(config map[string]any) error {
	features, exists, err := configMapSection(config, "features")
	if err != nil {
		return fmt.Errorf("Codex config: %w", err)
	}
	if !exists {
		features = map[string]any{}
		config["features"] = features
	}
	features["hooks"] = true
	return nil
}

func disableCodexHooksFeature(config map[string]any) {
	features, _ := config["features"].(map[string]any)
	if features == nil {
		return
	}
	delete(features, "hooks")
	if len(features) == 0 {
		delete(config, "features")
	}
}

func configMapSection(config map[string]any, key string) (map[string]any, bool, error) {
	value, exists := config[key]
	if !exists || value == nil {
		return nil, false, nil
	}
	section, ok := value.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("section %q must be an object/table, got %T", key, value)
	}
	return section, true, nil
}

func mergeHookCommand(hooks map[string]any, eventName, command string) {
	groups := filterTurnalHookCommands(normalizeHookGroups(hooks[eventName]))
	hooks[eventName] = append(groups, hookGroup(command))
}

func removeTurnalHookCommands(hooks map[string]any) (int, bool) {
	removedTotal := 0
	for eventName, value := range hooks {
		filtered, removed := filterTurnalHookCommandsWithCount(normalizeHookGroups(value))
		if removed == 0 {
			continue
		}
		removedTotal += removed
		if len(filtered) == 0 {
			delete(hooks, eventName)
			continue
		}
		hooks[eventName] = filtered
	}
	return removedTotal, removedTotal > 0
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

func filterTurnalHookCommands(groups []any) []any {
	filtered, _ := filterTurnalHookCommandsWithCount(groups)
	return filtered
}

func filterTurnalHookCommandsWithCount(groups []any) ([]any, int) {
	filtered := make([]any, 0, len(groups))
	removed := 0
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
			if IsTurnalHookCommand(command) {
				removed++
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
	return filtered, removed
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

func IsTurnalHookCommand(command string) bool {
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

func claudeSessionHook(commandPrefix string) string {
	return commandPrefix + " claude-hook session"
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

func claudeToolFailureHook(commandPrefix string) string {
	return commandPrefix + " claude-hook tool-failure"
}

func claudePreToolUseHook(commandPrefix string) string {
	return commandPrefix + " claude-hook pre-tool"
}

func codexHookCommand(commandPrefix string) string {
	return commandPrefix + " codex-hook"
}

func acquireConfigLock(projectRoot string, target Target) (*filelock.Lock, error) {
	if err := safepath.MkdirAllNoSymlinks(projectRoot, filepath.Join(".turnal", "tmp"), 0o700); err != nil {
		return nil, fmt.Errorf("prepare %s agent config lock: %w", target, err)
	}
	path := filepath.Join(projectRoot, ".turnal", "tmp", "config-"+string(target)+".lock")
	lock, err := filelock.Acquire(path, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("lock %s agent config: %w", target, err)
	}
	return lock, nil
}

func existingFileMode(path string, fallback os.FileMode) os.FileMode {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fallback
	}
	return info.Mode().Perm()
}

func writeConfigAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".turnal-config-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(mode.Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
