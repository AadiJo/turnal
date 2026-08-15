package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
