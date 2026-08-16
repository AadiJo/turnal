package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallGitHubCopilotCLIHooksLifecycleAndHealth(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".github", "hooks", "turnal.json")
	writeHealthFile(t, path, `{"version":1,"custom":"keep","hooks":{"agentStop":[{"type":"command","bash":"echo keep","powershell":"echo keep"}]}}`)
	opts := InstallOptions{HookCommand: "/opt/turnal"}
	if _, err := InstallCopilotHookWithOptions(root, opts); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	if _, err := InstallCopilotHookWithOptions(root, opts); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatal("idempotent GitHub Copilot CLI hook install rewrote config")
	}
	if !strings.Contains(string(second), `"custom": "keep"`) || !strings.Contains(string(second), `"bash": "echo keep"`) {
		t.Fatalf("third-party GitHub Copilot CLI config was not preserved: %s", second)
	}
	if health := inspectCopilotHooks(root, opts.HookCommand); !health.OK() || len(health.Events) != len(copilotHookEvents) {
		t.Fatalf("GitHub Copilot CLI health = %#v", health)
	}
	dry, err := UninstallCopilotHookWithOptions(root, UninstallOptions{DryRun: true})
	if err != nil || !dry.Changed || dry.RemovedCommands != len(copilotHookEvents) {
		t.Fatalf("GitHub Copilot CLI dry-run = %#v err=%v", dry, err)
	}
	afterDry, _ := os.ReadFile(path)
	if string(afterDry) != string(second) {
		t.Fatal("GitHub Copilot CLI dry-run changed config")
	}
	removed, err := UninstallCopilotHookWithOptions(root, UninstallOptions{})
	if err != nil || removed.RemovedCommands != len(copilotHookEvents) {
		t.Fatalf("GitHub Copilot CLI uninstall = %#v err=%v", removed, err)
	}
	after, _ := os.ReadFile(path)
	if !strings.Contains(string(after), `"bash": "echo keep"`) || strings.Contains(string(after), "adapter capture copilot-cli") {
		t.Fatalf("GitHub Copilot CLI uninstall did not preserve third-party hook: %s", after)
	}
}

func TestInstallGitHubCopilotCLIQuotesPowerShellExecutablePath(t *testing.T) {
	root := t.TempDir()
	opts := InstallOptions{HookCommand: `C:\Program Files\Turnal\turnal.exe`}
	if _, err := InstallCopilotHookWithOptions(root, opts); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".github", "hooks", "turnal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	hooks, _, err := configMapSection(config, "hooks")
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := normalizeHookArray(hooks["sessionStart"])
	command := entries[0].(map[string]any)["powershell"]
	want := `& 'C:\Program Files\Turnal\turnal.exe' adapter capture copilot-cli sessionStart`
	if command != want {
		t.Fatalf("PowerShell hook = %q, want %q", command, want)
	}
	bash := entries[0].(map[string]any)["bash"]
	wantBash := `'C:\Program Files\Turnal\turnal.exe' adapter capture copilot-cli sessionStart`
	if bash != wantBash {
		t.Fatalf("bash hook = %q, want %q", bash, wantBash)
	}
	if health := inspectCopilotHooks(root, opts.HookCommand); !health.OK() {
		t.Fatalf("quoted GitHub Copilot CLI health = %#v", health)
	}
}

func TestUninstallGitHubCopilotCLIRemovesManagedOnlyFile(t *testing.T) {
	root := t.TempDir()
	if _, err := InstallCopilotHookWithOptions(root, InstallOptions{HookCommand: "turnal"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".github", "hooks", "turnal.json")
	if _, err := UninstallCopilotHookWithOptions(root, UninstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("managed-only GitHub Copilot CLI hook remains: %v", err)
	}
}

func TestInstallOpenCodePluginLifecycleAndHealth(t *testing.T) {
	root := t.TempDir()
	opts := InstallOptions{HookCommand: "/opt/turnal"}
	installed, err := InstallOpenCodePluginWithOptions(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(installed.ConfigPath)
	if _, err := InstallOpenCodePluginWithOptions(root, opts); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(installed.ConfigPath)
	if string(first) != string(second) {
		t.Fatal("idempotent OpenCode plugin install rewrote plugin")
	}
	if health := inspectOpenCodePlugin(root, opts.HookCommand); !health.OK() {
		t.Fatalf("OpenCode health = %#v", health)
	}
	dry, err := UninstallOpenCodePluginWithOptions(root, UninstallOptions{DryRun: true})
	if err != nil || !dry.Changed || dry.RemovedCommands != 1 {
		t.Fatalf("OpenCode dry-run = %#v err=%v", dry, err)
	}
	if _, err := os.Stat(installed.ConfigPath); err != nil {
		t.Fatalf("OpenCode plugin removed during dry-run: %v", err)
	}
	if _, err := UninstallOpenCodePluginWithOptions(root, UninstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(installed.ConfigPath); !os.IsNotExist(err) {
		t.Fatalf("OpenCode plugin still exists: %v", err)
	}

	writeHealthFile(t, installed.ConfigPath, "export const custom = true;\n")
	if _, err := InstallOpenCodePluginWithOptions(root, opts); err == nil || !strings.Contains(err.Error(), "not managed by Turnal") {
		t.Fatalf("unmanaged OpenCode install error = %v", err)
	}
	removed, err := UninstallOpenCodePluginWithOptions(root, UninstallOptions{})
	if err != nil || removed.Changed || removed.RemovedCommands != 0 {
		t.Fatalf("unmanaged OpenCode uninstall = %#v err=%v", removed, err)
	}
}

func TestInstallCursorHookPreservesOtherHooksAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".cursor", "hooks.json")
	writeHealthFile(t, path, `{"version":1,"custom":"keep","hooks":{"stop":[{"command":"echo keep"},{"command":"old-turnal adapter capture cursor stop"}]}}`)
	opts := InstallOptions{HookCommand: "/opt/turnal"}
	if _, err := InstallCursorHookWithOptions(root, opts); err != nil {
		t.Fatalf("install Cursor hooks: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InstallCursorHookWithOptions(root, opts); err != nil {
		t.Fatalf("reinstall Cursor hooks: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("idempotent install rewrote Cursor hooks:\nfirst=%s\nsecond=%s", first, second)
	}

	var config map[string]any
	if err := json.Unmarshal(second, &config); err != nil {
		t.Fatal(err)
	}
	if config["custom"] != "keep" {
		t.Fatalf("custom config not preserved: %#v", config)
	}
	hooks := config["hooks"].(map[string]any)
	for _, eventName := range cursorHookEvents {
		entries, _ := normalizeHookArray(hooks[eventName])
		commands := cursorCommands(entries)
		if countCommand(commands, cursorHookCommand(opts.HookCommand, eventName)) != 1 {
			t.Fatalf("%s commands = %#v", eventName, commands)
		}
	}
	stopEntries, _ := normalizeHookArray(hooks["stop"])
	if countCommand(cursorCommands(stopEntries), "echo keep") != 1 {
		t.Fatalf("third-party Cursor hook not preserved: %#v", stopEntries)
	}
	if health := inspectCursorHooks(root, opts.HookCommand); !health.OK() || len(health.Events) != len(cursorHookEvents) {
		t.Fatalf("Cursor health = %#v", health)
	}
}

func TestInstallCursorHookRejectsUnsupportedVersion(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".cursor", "hooks.json")
	writeHealthFile(t, path, `{"version":2,"hooks":{}}`)
	if _, err := InstallCursorHook(root); err == nil || !strings.Contains(err.Error(), "version must be 1") {
		t.Fatalf("install error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != `{"version":2,"hooks":{}}` {
		t.Fatalf("unsupported Cursor config changed: data=%q err=%v", data, err)
	}
	if health := inspectCursorHooks(root, "turnal"); health.Status != HookConfigurationMalformed {
		t.Fatalf("Cursor version health = %#v", health)
	}
}

func TestUninstallCursorHookPreservesOtherConfigAndSupportsDryRun(t *testing.T) {
	root := t.TempDir()
	opts := InstallOptions{HookCommand: "turnal"}
	if _, err := InstallCursorHookWithOptions(root, opts); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".cursor", "hooks.json")
	var config map[string]any
	readJSONFile(t, path, &config)
	hooks := config["hooks"].(map[string]any)
	hooks["stop"] = append(hooks["stop"].([]any), map[string]any{"command": "echo keep"})
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	dry, err := UninstallCursorHookWithOptions(root, UninstallOptions{DryRun: true})
	if err != nil || dry.RemovedCommands != len(cursorHookEvents) || !dry.Changed {
		t.Fatalf("dry-run result = %#v err=%v", dry, err)
	}
	afterDry, _ := os.ReadFile(path)
	if string(before) != string(afterDry) {
		t.Fatal("Cursor dry-run changed config")
	}
	removed, err := UninstallCursorHook(root)
	if err != nil || removed.RemovedCommands != len(cursorHookEvents) {
		t.Fatalf("uninstall result = %#v err=%v", removed, err)
	}
	readJSONFile(t, path, &config)
	hooks = config["hooks"].(map[string]any)
	if len(hooks) != 1 || countCommand(cursorCommands(hooks["stop"].([]any)), "echo keep") != 1 {
		t.Fatalf("Cursor hooks after uninstall = %#v", hooks)
	}
}

func TestInstallPiExtensionLifecycleAndHealth(t *testing.T) {
	root := t.TempDir()
	installed, err := InstallPiExtension(root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(installed.ConfigPath)
	if err != nil || string(data) != string(piExtensionSource) {
		t.Fatalf("installed Pi extension differs: err=%v", err)
	}
	if health := inspectPiExtension(root, "turnal"); !health.OK() {
		t.Fatalf("Pi health = %#v", health)
	}

	stale := []byte(piExtensionMarker + "\n// stale\n")
	if err := os.WriteFile(installed.ConfigPath, stale, 0o600); err != nil {
		t.Fatal(err)
	}
	if health := inspectPiExtension(root, "turnal"); health.Status != HookConfigurationIncomplete || health.OK() {
		t.Fatalf("stale Pi health = %#v", health)
	}
	if _, err := InstallPiExtension(root); err != nil {
		t.Fatalf("upgrade managed Pi extension: %v", err)
	}

	dry, err := UninstallPiExtensionWithOptions(root, UninstallOptions{DryRun: true})
	if err != nil || dry.RemovedCommands != 1 || !dry.Changed {
		t.Fatalf("Pi dry-run result = %#v err=%v", dry, err)
	}
	if _, err := os.Stat(installed.ConfigPath); err != nil {
		t.Fatalf("Pi extension removed during dry-run: %v", err)
	}
	if _, err := UninstallPiExtension(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(installed.ConfigPath); !os.IsNotExist(err) {
		t.Fatalf("Pi extension still exists: %v", err)
	}
}

func TestInstallPiExtensionRefusesUnmanagedFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".pi", "extensions", "turnal.ts")
	writeHealthFile(t, path, "export default function custom() {}\n")
	if _, err := InstallPiExtension(root); err == nil || !strings.Contains(err.Error(), "not managed by Turnal") {
		t.Fatalf("install error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "export default function custom() {}\n" {
		t.Fatalf("unmanaged Pi extension changed: data=%q err=%v", data, err)
	}
	removed, err := UninstallPiExtension(root)
	if err != nil || removed.Changed || removed.RemovedCommands != 0 {
		t.Fatalf("unmanaged Pi uninstall = %#v err=%v", removed, err)
	}
}

func TestInstallPiExtensionUsesConfiguredCommand(t *testing.T) {
	root := t.TempDir()
	opts := InstallOptions{HookCommand: "/opt/turnal-live"}
	installed, err := InstallPiExtensionWithOptions(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(installed.ConfigPath)
	if err != nil || !strings.Contains(string(data), `const turnalCommand = "/opt/turnal-live";`) {
		t.Fatalf("custom Pi command missing: data=%q err=%v", data, err)
	}
	if health := inspectPiExtension(root, opts.HookCommand); !health.OK() {
		t.Fatalf("custom Pi health = %#v", health)
	}
	if health := inspectPiExtension(root, "turnal"); health.Status != HookConfigurationIncomplete {
		t.Fatalf("mismatched Pi health = %#v", health)
	}
}

func TestInstallPiExtensionRemovesShellQuotesFromExecutable(t *testing.T) {
	for _, command := range []string{`"C:\Program Files\Turnal\turnal.exe"`, `'/opt/Turnal CLI/turnal'`} {
		t.Run(command, func(t *testing.T) {
			root := t.TempDir()
			installed, err := InstallPiExtensionWithOptions(root, InstallOptions{HookCommand: command})
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(installed.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			unquoted := command[1 : len(command)-1]
			encoded, err := json.Marshal(unquoted)
			if err != nil {
				t.Fatal(err)
			}
			want := "const turnalCommand = " + string(encoded) + ";"
			if !strings.Contains(string(data), want) {
				t.Fatalf("Pi command still contains shell quotes: want %q in extension", want)
			}
			if health := inspectPiExtension(root, command); !health.OK() {
				t.Fatalf("quoted-command Pi health = %#v", health)
			}
		})
	}
}

func TestEmbeddedPiExtensionMatchesPublishedIntegration(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "integrations", "pi", "turnal.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(piExtensionSource) {
		t.Fatal("embedded Pi extension differs from integrations/pi/turnal.ts")
	}
}

func cursorCommands(entries []any) []string {
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
	return commands
}
