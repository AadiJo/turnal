package compatibility

import (
	"context"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/adapters"
)

func TestDiagnoseClassifiesClaudeSurfacesWithoutLaunchingAProcess(t *testing.T) {
	root := t.TempDir()
	if _, err := adapters.InstallClaudeHookWithOptions(root, adapters.InstallOptions{HookCommand: "turnal"}); err != nil {
		t.Fatalf("install Claude hooks: %v", err)
	}

	report := Diagnose(context.Background(), Options{
		WorkspaceRoot: root,
		HookCommand:   "turnal",
		Targets:       []adapters.Target{adapters.TargetClaude},
		ProbeCodex:    true,
		CodexProbe:    panicProbe{},
	})
	if len(report.Surfaces) != 2 {
		t.Fatalf("surfaces = %#v, want Claude Code and SDK", report.Surfaces)
	}
	code, sdk := report.Surfaces[0], report.Surfaces[1]
	if code.Surface != SurfaceClaudeCode || code.Configuration != adapters.HookConfigurationConfigured || code.Expectation != CaptureAvailable || code.Certainty != CertaintyLikely {
		t.Fatalf("Claude Code = %#v", code)
	}
	if sdk.Surface != SurfaceClaudeAgentSDK || sdk.Expectation != CaptureHostControlled || sdk.Certainty != CertaintyHostControlled || sdk.NeedsAttention() {
		t.Fatalf("Claude SDK = %#v", sdk)
	}
	joined := strings.Join(append(sdk.Limitations, sdk.Guidance...), "\n")
	for _, want := range []string{"settingSources", `include "project"`, "does not currently consume"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("SDK explanation missing %q: %s", want, joined)
		}
	}
}

func TestDiagnoseReportsMissingClaudeProjectHooks(t *testing.T) {
	report := Diagnose(context.Background(), Options{
		WorkspaceRoot: t.TempDir(),
		HookCommand:   "turnal",
		Targets:       []adapters.Target{adapters.TargetClaude},
	})
	if len(report.Surfaces) != 2 {
		t.Fatalf("surfaces = %#v", report.Surfaces)
	}
	for _, result := range report.Surfaces {
		if result.Configuration != adapters.HookConfigurationMissing || result.Expectation != CaptureUnavailable || !result.NeedsAttention() {
			t.Fatalf("missing Claude result = %#v", result)
		}
	}
}

type panicProbe struct{}

func (panicProbe) Probe(context.Context, string, string) (CodexHooksResult, error) {
	panic("Claude-only diagnostics launched Codex")
}

func configuredCodexHealth() adapters.HookHealth {
	return adapters.HookHealth{
		Target: adapters.TargetCodex,
		Status: adapters.HookConfigurationConfigured,
		Events: []adapters.HookEventHealth{
			{Name: "SessionStart", Status: adapters.HookEventConfigured},
			{Name: "UserPromptSubmit", Status: adapters.HookEventConfigured},
			{Name: "PostToolUse", Status: adapters.HookEventConfigured},
			{Name: "Stop", Status: adapters.HookEventConfigured},
		},
	}
}

func TestClassifyCodexHooks(t *testing.T) {
	root := t.TempDir()
	expected := "turnal codex-hook"
	health := configuredCodexHealth()
	all := []CodexHook{
		{CWD: root, EventName: "stop", Command: expected, Enabled: true, TrustStatus: "trusted"},
		{CWD: root, EventName: "post_tool_use", Command: expected, Enabled: true, TrustStatus: "trusted"},
		{CWD: root, EventName: "sessionStart", Command: expected, Enabled: true, TrustStatus: "trusted"},
		{CWD: root, EventName: "user-prompt-submit", Command: expected, Enabled: true, TrustStatus: "trusted"},
	}

	tests := []struct {
		name          string
		hooks         []CodexHook
		wantExecution ExecutionStatus
		wantCapture   CaptureExpectation
	}{
		{"trusted in arbitrary order", all, ExecutionConfirmed, CaptureAvailable},
		{"untrusted", replaceHook(all, 0, func(hook *CodexHook) { hook.TrustStatus = "untrusted" }), ExecutionUntrusted, CaptureUnavailable},
		{"missing", all[:3], ExecutionUnavailable, CaptureUnavailable},
		{"disabled", replaceHook(all, 1, func(hook *CodexHook) { hook.Enabled = false }), ExecutionDisabled, CaptureUnavailable},
		{"different command", replaceHook(all, 2, func(hook *CodexHook) { hook.Command = "other-tool" }), ExecutionUnavailable, CaptureUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := ClassifyCodexHooks(root, expected, health, CodexHooksResult{Hooks: test.hooks})
			if result.Execution != test.wantExecution || result.Expectation != test.wantCapture {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestClassifyCodexHooksIgnoresUnrelatedHooksAndPreservesWarnings(t *testing.T) {
	root := t.TempDir()
	health := configuredCodexHealth()
	hooks := []CodexHook{
		{CWD: root, EventName: "SessionStart", Command: "turnal codex-hook", Enabled: true, TrustStatus: "trusted"},
		{CWD: root, EventName: "UserPromptSubmit", Command: "turnal codex-hook", Enabled: true, TrustStatus: "trusted"},
		{CWD: root, EventName: "PostToolUse", Command: "turnal codex-hook", Enabled: true, TrustStatus: "trusted"},
		{CWD: root, EventName: "Stop", Command: "turnal codex-hook", Enabled: true, TrustStatus: "trusted"},
		{CWD: root, EventName: "Stop", Command: "plugin-tool", Enabled: false, TrustStatus: "untrusted"},
		{CWD: t.TempDir(), EventName: "Stop", Command: "turnal codex-hook", Enabled: false, TrustStatus: "untrusted"},
	}
	result := ClassifyCodexHooks(root, "turnal codex-hook", health, CodexHooksResult{Hooks: hooks, Warnings: []string{"project warning"}})
	if result.Expectation != CaptureAvailable || result.Discovered != 4 || result.Enabled != 4 || result.Trusted != 4 {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Warnings) != 1 || result.Warnings[0] != "project warning" {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
}

func replaceHook(hooks []CodexHook, index int, change func(*CodexHook)) []CodexHook {
	copyOfHooks := append([]CodexHook(nil), hooks...)
	change(&copyOfHooks[index])
	return copyOfHooks
}
