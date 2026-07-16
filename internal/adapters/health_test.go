package adapters

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectClaudeHookStates(t *testing.T) {
	tests := []struct {
		name       string
		settings   string
		wantStatus HookConfigurationStatus
		wantEvent  HookEventStatus
		wantText   string
	}{
		{
			name:       "configured",
			settings:   `{"hooks":{"UserPromptSubmit":[{"hooks":[{"command":"turnal claude-hook user"}]}],"PostToolUse":[{"hooks":[{"command":"turnal claude-hook tool-use"}]}],"Stop":[{"hooks":[{"command":"turnal claude-hook assistant"}]}]}}`,
			wantStatus: HookConfigurationConfigured,
			wantEvent:  HookEventConfigured,
		},
		{
			name:       "different command",
			settings:   `{"hooks":{"UserPromptSubmit":[{"hooks":[{"command":"other-tool"}]}],"PostToolUse":[{"hooks":[{"command":"turnal claude-hook tool-use"}]}],"Stop":[{"hooks":[{"command":"turnal claude-hook assistant"}]}]}}`,
			wantStatus: HookConfigurationIncomplete,
			wantEvent:  HookEventDifferentCommand,
			wantText:   "uses a different command",
		},
		{
			name:       "missing event",
			settings:   `{"hooks":{"PostToolUse":[{"hooks":[{"command":"turnal claude-hook tool-use"}]}],"Stop":[{"hooks":[{"command":"turnal claude-hook assistant"}]}]}}`,
			wantStatus: HookConfigurationIncomplete,
			wantEvent:  HookEventMissing,
			wantText:   "has no hook definition",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeHealthFile(t, filepath.Join(root, ".claude", "settings.json"), test.settings)
			health := inspectClaudeHooks(root, "turnal")
			if health.Status != test.wantStatus {
				t.Fatalf("status = %q, want %q; problems=%v", health.Status, test.wantStatus, health.Problems)
			}
			if len(health.Events) != 3 || health.Events[0].Status != test.wantEvent {
				t.Fatalf("events = %#v, want first status %q", health.Events, test.wantEvent)
			}
			if test.wantText != "" && !strings.Contains(strings.Join(health.Problems, "\n"), test.wantText) {
				t.Fatalf("problems = %v, want %q", health.Problems, test.wantText)
			}
		})
	}
}

func TestInspectClaudeHooksReportsMalformedSettings(t *testing.T) {
	root := t.TempDir()
	writeHealthFile(t, filepath.Join(root, ".claude", "settings.json"), "{broken")
	health := inspectClaudeHooks(root, "turnal")
	if health.Status != HookConfigurationMalformed || health.OK() {
		t.Fatalf("health = %#v, want malformed", health)
	}
}

func TestInspectClaudeHooksTreatsEmptyEventAsMissing(t *testing.T) {
	root := t.TempDir()
	writeHealthFile(t, filepath.Join(root, ".claude", "settings.json"), `{"hooks":{"UserPromptSubmit":[],"PostToolUse":[{"hooks":[{"command":"turnal claude-hook tool-use"}]}],"Stop":[{"hooks":[{"command":"turnal claude-hook assistant"}]}]}}`)
	health := inspectClaudeHooks(root, "turnal")
	if health.Status != HookConfigurationIncomplete || len(health.Events) != 3 || health.Events[0].Status != HookEventMissing {
		t.Fatalf("health = %#v, want missing UserPromptSubmit", health)
	}
	if strings.Contains(strings.Join(health.Problems, "\n"), "uses a different command") {
		t.Fatalf("empty event was mislabeled as a different command: %v", health.Problems)
	}
}

func TestInspectCodexHookStates(t *testing.T) {
	configured := func(sessionStart string, hooksFeature bool) string {
		return `
[features]
hooks = ` + map[bool]string{true: "true", false: "false"}[hooksFeature] + `

[hooks]
[[hooks.SessionStart]]
[[hooks.SessionStart.hooks]]
command = "` + sessionStart + `"
[[hooks.UserPromptSubmit]]
[[hooks.UserPromptSubmit.hooks]]
command = "turnal codex-hook"
[[hooks.PostToolUse]]
[[hooks.PostToolUse.hooks]]
command = "turnal codex-hook"
[[hooks.Stop]]
[[hooks.Stop.hooks]]
command = "turnal codex-hook"
`
	}

	tests := []struct {
		name       string
		config     string
		wantStatus HookConfigurationStatus
		wantEvent  HookEventStatus
		wantText   string
	}{
		{"configured", configured("turnal codex-hook", true), HookConfigurationConfigured, HookEventConfigured, ""},
		{"different command", configured("other-tool", true), HookConfigurationIncomplete, HookEventDifferentCommand, "uses a different command"},
		{"feature disabled", configured("turnal codex-hook", false), HookConfigurationDisabled, HookEventConfigured, "feature flag is not enabled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeHealthFile(t, filepath.Join(root, ".codex", "config.toml"), test.config)
			health := inspectCodexHooks(root, "turnal")
			if health.Status != test.wantStatus || len(health.Events) != 4 || health.Events[0].Status != test.wantEvent {
				t.Fatalf("health = %#v", health)
			}
			if test.wantText != "" && !strings.Contains(strings.Join(health.Problems, "\n"), test.wantText) {
				t.Fatalf("problems = %v, want %q", health.Problems, test.wantText)
			}
		})
	}
}

func TestInspectCodexHooksReportsMissingEventAndMalformedConfig(t *testing.T) {
	root := t.TempDir()
	writeHealthFile(t, filepath.Join(root, ".codex", "config.toml"), "[features]\nhooks = true\n[hooks]\n")
	health := inspectCodexHooks(root, "turnal")
	if health.Status != HookConfigurationIncomplete || len(health.Events) != 4 || health.Events[0].Status != HookEventMissing {
		t.Fatalf("missing event health = %#v", health)
	}

	writeHealthFile(t, filepath.Join(root, ".codex", "config.toml"), "[broken")
	health = inspectCodexHooks(root, "turnal")
	if health.Status != HookConfigurationMalformed || health.OK() {
		t.Fatalf("malformed health = %#v", health)
	}
}

func TestInspectCodexHooksReportsInvalidSectionsAsMalformed(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		section  string
	}{
		{name: "features", contents: "features = \"invalid\"\n", section: "features"},
		{name: "hooks", contents: "hooks = \"invalid\"\n[features]\nhooks = true\n", section: "hooks"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeHealthFile(t, filepath.Join(root, ".codex", "config.toml"), test.contents)
			health := inspectCodexHooks(root, "turnal")
			if health.Status != HookConfigurationMalformed || health.OK() {
				t.Fatalf("health = %#v", health)
			}
			want := fmt.Sprintf("section %q must be an object/table", test.section)
			if !strings.Contains(strings.Join(health.Problems, "\n"), want) {
				t.Fatalf("problems = %v, want %q", health.Problems, want)
			}
		})
	}
}

func TestInspectHooksIgnoresOtherCommandsWithoutModifyingConfig(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".claude", "settings.json")
	settings := `{"hooks":{"UserPromptSubmit":[{"hooks":[{"command":"other-tool"},{"command":"turnal claude-hook user"}]}],"PostToolUse":[{"hooks":[{"command":"turnal claude-hook tool-use"}]}],"Stop":[{"hooks":[{"command":"turnal claude-hook assistant"}]}]}}`
	writeHealthFile(t, path, settings)
	if health := inspectClaudeHooks(root, "turnal"); !health.OK() {
		t.Fatalf("health problems = %v", health.Problems)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != settings {
		t.Fatalf("inspection modified config: data=%q err=%v", after, err)
	}
}

func writeHealthFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
