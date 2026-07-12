package compatibility

import (
	"context"
	"os"
	"path/filepath"
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
		projectCodexHook(root, "stop", expected),
		projectCodexHook(root, "post_tool_use", expected),
		projectCodexHook(root, "sessionStart", expected),
		projectCodexHook(root, "user-prompt-submit", expected),
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
		projectCodexHook(root, "SessionStart", "turnal codex-hook"),
		projectCodexHook(root, "UserPromptSubmit", "turnal codex-hook"),
		projectCodexHook(root, "PostToolUse", "turnal codex-hook"),
		projectCodexHook(root, "Stop", "turnal codex-hook"),
		{CWD: root, EventName: "Stop", Command: "plugin-tool", Source: "plugin", Enabled: false, TrustStatus: "untrusted"},
		{CWD: root, EventName: "Stop", Command: "turnal codex-hook", Source: "plugin", SourcePath: filepath.Join(root, "plugin", "hooks.json"), Enabled: false, TrustStatus: "untrusted"},
		projectCodexHook(t.TempDir(), "Stop", "turnal codex-hook"),
	}
	result := ClassifyCodexHooks(root, "turnal codex-hook", health, CodexHooksResult{Hooks: hooks, Warnings: []string{"project warning"}})
	if result.Expectation != CaptureAvailable || result.Discovered != 4 || result.Enabled != 4 || result.Trusted != 4 {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Warnings) != 1 || result.Warnings[0] != "project warning" {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
}

func TestClassifyCodexHooksAcceptsRootCheckoutSourceForLinkedWorktree(t *testing.T) {
	rootCheckout := t.TempDir()
	linkedWorktree := filepath.Join(t.TempDir(), "linked")
	gitDir := filepath.Join(rootCheckout, ".git", "worktrees", "linked")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(linkedWorktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linkedWorktree, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var hooks []CodexHook
	for _, event := range expectedCodexEventNames {
		hook := projectCodexHook(linkedWorktree, event, "turnal codex-hook")
		hook.SourcePath = filepath.Join(rootCheckout, ".codex", "config.toml")
		hook.TrustStatus = "untrusted"
		hooks = append(hooks, hook)
	}
	result := ClassifyCodexHooks(linkedWorktree, "turnal codex-hook", configuredCodexHealth(), CodexHooksResult{Hooks: hooks})
	if result.Discovered != 4 || result.Enabled != 4 || result.Trusted != 0 || result.Execution != ExecutionUntrusted {
		t.Fatalf("linked-worktree result = %#v", result)
	}
}

func TestClassifyCodexHooksStillRequiresAllEventsWhenStaticConfigIsMissing(t *testing.T) {
	root := t.TempDir()
	result := ClassifyCodexHooks(root, "turnal codex-hook", adapters.HookHealth{
		Target: adapters.TargetCodex,
		Status: adapters.HookConfigurationMissing,
	}, CodexHooksResult{})
	if result.Expected != 4 || result.Discovered != 0 || result.Expectation != CaptureUnavailable {
		t.Fatalf("result = %#v", result)
	}
}

func TestClassifyCodexHooksKeepsStaticDisabledStateAuthoritative(t *testing.T) {
	root := t.TempDir()
	health := configuredCodexHealth()
	health.Status = adapters.HookConfigurationDisabled
	health.Problems = []string{"codex hooks feature flag is not enabled"}
	hooks := []CodexHook{
		projectCodexHook(root, "SessionStart", "turnal codex-hook"),
		projectCodexHook(root, "UserPromptSubmit", "turnal codex-hook"),
		projectCodexHook(root, "PostToolUse", "turnal codex-hook"),
		projectCodexHook(root, "Stop", "turnal codex-hook"),
	}
	result := ClassifyCodexHooks(root, "turnal codex-hook", health, CodexHooksResult{Hooks: hooks})
	if result.Discovered != 4 || result.Enabled != 4 || result.Trusted != 4 {
		t.Fatalf("live discovery details were lost: %#v", result)
	}
	if result.Execution != ExecutionDisabled || result.Expectation != CaptureUnavailable || result.Certainty != CertaintyIncompatible {
		t.Fatalf("static disabled state was not authoritative: %#v", result)
	}
}

func replaceHook(hooks []CodexHook, index int, change func(*CodexHook)) []CodexHook {
	copyOfHooks := append([]CodexHook(nil), hooks...)
	change(&copyOfHooks[index])
	return copyOfHooks
}

func projectCodexHook(root, event, command string) CodexHook {
	return CodexHook{
		CWD: root, EventName: event, Command: command, Source: "project",
		SourcePath: filepath.Join(root, ".codex", "config.toml"), Enabled: true, TrustStatus: "trusted",
	}
}
