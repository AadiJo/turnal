package compatibility

import (
	"context"
	"os"
	"os/exec"
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

type fixedCodexProbe struct {
	result CodexHooksResult
}

func (probe fixedCodexProbe) Probe(context.Context, string, string) (CodexHooksResult, error) {
	return probe.result, nil
}

func TestDiagnoseUsesRootCheckoutCodexConfigurationForLinkedWorktree(t *testing.T) {
	rootCheckout, linkedWorktree := createLinkedWorktree(t)
	if _, err := adapters.InstallCodexHookWithOptions(rootCheckout, adapters.InstallOptions{HookCommand: "turnal"}); err != nil {
		t.Fatalf("install root-checkout Codex hooks: %v", err)
	}
	var hooks []CodexHook
	for _, event := range expectedCodexEventNames {
		hook := projectCodexHook(linkedWorktree, event, "turnal codex-hook")
		hook.SourcePath = filepath.Join(rootCheckout, ".codex", "config.toml")
		hook.TrustStatus = "untrusted"
		hooks = append(hooks, hook)
	}
	report := Diagnose(context.Background(), Options{
		WorkspaceRoot: linkedWorktree,
		HookCommand:   "turnal",
		Targets:       []adapters.Target{adapters.TargetCodex},
		ProbeCodex:    true,
		CodexProbe:    fixedCodexProbe{result: CodexHooksResult{Hooks: hooks}},
	})
	if len(report.Surfaces) != 2 {
		t.Fatalf("surfaces = %#v", report.Surfaces)
	}
	cli, appServer := report.Surfaces[0], report.Surfaces[1]
	if cli.Configuration != adapters.HookConfigurationConfigured || cli.Expectation != CaptureAvailable {
		t.Fatalf("Codex CLI = %#v", cli)
	}
	if appServer.Configuration != adapters.HookConfigurationConfigured || appServer.Discovered != 5 || appServer.Enabled != 5 || appServer.Trusted != 0 || appServer.Execution != ExecutionUntrusted || appServer.Certainty != CertaintyConfirmed {
		t.Fatalf("Codex app-server = %#v", appServer)
	}
}

func configuredCodexHealth() adapters.HookHealth {
	return adapters.HookHealth{
		Target: adapters.TargetCodex,
		Status: adapters.HookConfigurationConfigured,
		Events: []adapters.HookEventHealth{
			{Name: "SessionStart", Status: adapters.HookEventConfigured},
			{Name: "UserPromptSubmit", Status: adapters.HookEventConfigured},
			{Name: "PreToolUse", Status: adapters.HookEventConfigured},
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
		projectCodexHook(root, "preToolUse", expected),
	}

	tests := []struct {
		name          string
		hooks         []CodexHook
		wantExecution ExecutionStatus
		wantCapture   CaptureExpectation
	}{
		{"trusted in arbitrary order", all, ExecutionConfirmed, CaptureAvailable},
		{"untrusted", replaceHook(all, 0, func(hook *CodexHook) { hook.TrustStatus = "untrusted" }), ExecutionUntrusted, CaptureUnavailable},
		{"missing", all[:4], ExecutionUnavailable, CaptureUnavailable},
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
		projectCodexHook(root, "PreToolUse", "turnal codex-hook"),
		projectCodexHook(root, "PostToolUse", "turnal codex-hook"),
		projectCodexHook(root, "Stop", "turnal codex-hook"),
		{CWD: root, EventName: "Stop", Command: "plugin-tool", Source: "plugin", Enabled: false, TrustStatus: "untrusted"},
		{CWD: root, EventName: "Stop", Command: "turnal codex-hook", Source: "plugin", SourcePath: filepath.Join(root, "plugin", "hooks.json"), Enabled: false, TrustStatus: "untrusted"},
		projectCodexHook(t.TempDir(), "Stop", "turnal codex-hook"),
	}
	result := ClassifyCodexHooks(root, "turnal codex-hook", health, CodexHooksResult{Hooks: hooks, Warnings: []string{"project warning"}})
	if result.Expectation != CaptureAvailable || result.Discovered != 5 || result.Enabled != 5 || result.Trusted != 5 {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Warnings) != 1 || result.Warnings[0] != "project warning" {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
}

func TestClassifyCodexHooksAcceptsRootCheckoutSourceForLinkedWorktree(t *testing.T) {
	rootCheckout, linkedWorktree := createLinkedWorktree(t)
	configPath := filepath.Join(rootCheckout, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("[features]\nhooks = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var hooks []CodexHook
	for _, event := range expectedCodexEventNames {
		hook := projectCodexHook(linkedWorktree, event, "turnal codex-hook")
		hook.SourcePath = configPath
		hook.TrustStatus = "untrusted"
		hooks = append(hooks, hook)
	}
	result := ClassifyCodexHooks(linkedWorktree, "turnal codex-hook", configuredCodexHealth(), CodexHooksResult{Hooks: hooks})
	if result.Discovered != 5 || result.Enabled != 5 || result.Trusted != 0 || result.Execution != ExecutionUntrusted {
		t.Fatalf("linked-worktree result = %#v", result)
	}
}

func createLinkedWorktree(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable not found")
	}
	parent := t.TempDir()
	rootCheckout := filepath.Join(parent, "main")
	linkedWorktree := filepath.Join(parent, "linked")
	if err := os.MkdirAll(rootCheckout, 0o755); err != nil {
		t.Fatal(err)
	}
	runCompatibilityTestGit(t, rootCheckout, "init")
	runCompatibilityTestGit(t, rootCheckout, "config", "user.email", "turnal@example.test")
	runCompatibilityTestGit(t, rootCheckout, "config", "user.name", "Turnal Test")
	if err := os.WriteFile(filepath.Join(rootCheckout, "tracked.txt"), []byte("tracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCompatibilityTestGit(t, rootCheckout, "add", "tracked.txt")
	runCompatibilityTestGit(t, rootCheckout, "commit", "-m", "initial")
	runCompatibilityTestGit(t, rootCheckout, "worktree", "add", "-b", "compatibility-linked-test", linkedWorktree)
	return rootCheckout, linkedWorktree
}

func runCompatibilityTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	cleanEnvironment := make([]string, 0, len(os.Environ()))
	for _, item := range os.Environ() {
		name, _, found := strings.Cut(item, "=")
		if found && !strings.HasPrefix(name, "GIT_") {
			cleanEnvironment = append(cleanEnvironment, item)
		}
	}
	command.Env = cleanEnvironment
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func TestClassifyCodexHooksStillRequiresAllEventsWhenStaticConfigIsMissing(t *testing.T) {
	root := t.TempDir()
	result := ClassifyCodexHooks(root, "turnal codex-hook", adapters.HookHealth{
		Target: adapters.TargetCodex,
		Status: adapters.HookConfigurationMissing,
	}, CodexHooksResult{})
	if result.Expected != 5 || result.Discovered != 0 || result.Expectation != CaptureUnavailable {
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
		projectCodexHook(root, "PreToolUse", "turnal codex-hook"),
		projectCodexHook(root, "PostToolUse", "turnal codex-hook"),
		projectCodexHook(root, "Stop", "turnal codex-hook"),
	}
	result := ClassifyCodexHooks(root, "turnal codex-hook", health, CodexHooksResult{Hooks: hooks})
	if result.Discovered != 5 || result.Enabled != 5 || result.Trusted != 5 {
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
