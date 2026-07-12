package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/adapters"
	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/compatibility"
)

func TestOrdinaryStatusDoesNotStartAppServer(t *testing.T) {
	root := captureStatusWorkspace(t, "codex")
	t.Chdir(root)

	probe := &recordingCodexProbe{panicOnCall: true}
	output, err := executeStatus(t, probe)
	if err != nil {
		t.Fatalf("status: %v\n%s", err, output)
	}
	if probe.calls != 0 {
		t.Fatalf("ordinary status made %d probe calls", probe.calls)
	}
	if strings.Contains(output, "Agent capture compatibility") {
		t.Fatalf("ordinary status included capture probe output:\n%s", output)
	}
}

func TestStatusProbeReportsClaudeSDKAsHostControlledWithoutFailing(t *testing.T) {
	root := captureStatusWorkspace(t, "claude")
	t.Chdir(root)

	output, err := executeStatus(t, &recordingCodexProbe{panicOnCall: true}, "--probe-agent-capture")
	if err != nil {
		t.Fatalf("status probe: %v\n%s", err, output)
	}
	for _, want := range []string{
		"Claude Code", "expected capture:    available", "Claude Agent SDK",
		"expected capture:    host-controlled", "settingSources", "state:      ok",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("status output missing %q:\n%s", want, output)
		}
	}
}

func TestStatusProbeReportsUntrustedCodexHooksAndFails(t *testing.T) {
	root := captureStatusWorkspace(t, "all")
	t.Chdir(root)

	probe := &recordingCodexProbe{trustStatus: "untrusted"}
	output, err := executeStatus(t, probe, "--probe-agent-capture")
	if err == nil {
		t.Fatalf("untrusted probe succeeded:\n%s", output)
	}
	if probe.calls != 1 {
		t.Fatalf("probe calls = %d, want 1", probe.calls)
	}
	for _, want := range []string{
		"Codex app-server", "discovery:           4/4 Turnal hooks", "trusted:             0/4",
		"expected capture:    unavailable", "certainty:           confirmed",
		"Codex app-server will skip untrusted hooks", "state:      needs attention",
		"Codex app-server capture unavailable: execution is untrusted",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("status output missing %q:\n%s", want, output)
		}
	}
}

func TestStatusProbeReportsInfrastructureFailureDistinctly(t *testing.T) {
	root := captureStatusWorkspace(t, "codex")
	t.Chdir(root)

	output, err := executeStatus(t, &recordingCodexProbe{err: errors.New("fake app-server exited")}, "--probe-agent-capture")
	if err == nil {
		t.Fatalf("failed probe succeeded:\n%s", output)
	}
	for _, want := range []string{"runtime probe:       unavailable", "probe failure:       fake app-server exited", "Codex app-server probe failed:"} {
		if !strings.Contains(output, want) {
			t.Fatalf("status output missing %q:\n%s", want, output)
		}
	}
}

func TestStatusProbeChangesNoWorkspaceOrMetadataState(t *testing.T) {
	root := captureStatusWorkspace(t, "all")
	t.Chdir(root)
	before := snapshotStatusTree(t, root)

	output, err := executeStatus(t, &recordingCodexProbe{trustStatus: "trusted"}, "--probe-agent-capture")
	if err != nil {
		t.Fatalf("status probe: %v\n%s", err, output)
	}
	after := snapshotStatusTree(t, root)
	if !reflect.DeepEqual(after, before) {
		var changes []string
		for path, beforeFile := range before {
			if afterFile, ok := after[path]; !ok || afterFile != beforeFile {
				changes = append(changes, path)
			}
		}
		for path := range after {
			if _, ok := before[path]; !ok {
				changes = append(changes, path+" (created)")
			}
		}
		t.Fatalf("workspace changed during probe: %v", changes)
	}
	if !strings.Contains(output, "certainty:           confirmed") || !strings.Contains(output, "expected capture:    host-controlled") {
		t.Fatalf("output does not distinguish confirmed and host-controlled:\n%s", output)
	}
}

type recordingCodexProbe struct {
	calls       int
	panicOnCall bool
	trustStatus string
	err         error
}

func (probe *recordingCodexProbe) Probe(_ context.Context, root, command string) (compatibility.CodexHooksResult, error) {
	probe.calls++
	if probe.panicOnCall {
		panic("unexpected Codex app-server probe")
	}
	if probe.err != nil {
		return compatibility.CodexHooksResult{}, probe.err
	}
	trust := probe.trustStatus
	if trust == "" {
		trust = "trusted"
	}
	var hooks []compatibility.CodexHook
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Stop"} {
		hooks = append(hooks, compatibility.CodexHook{
			CWD: root, EventName: event, Command: command, Source: "project",
			SourcePath: filepath.Join(root, ".codex", "config.toml"), Enabled: true,
			CurrentHash: "sha256:test", TrustStatus: trust,
		})
	}
	return compatibility.CodexHooksResult{Hooks: hooks}, nil
}

func executeStatus(t *testing.T, probe compatibility.CodexProbe, args ...string) (string, error) {
	t.Helper()
	cmd := statusCmdWithProbe(probe)
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return output.String(), err
}

func captureStatusWorkspace(t *testing.T, agent string) string {
	t.Helper()
	requireGit(t)
	isolateAgentConfig(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("initialize checkpoint repo: %v", err)
	}
	var targets []adapters.Target
	switch agent {
	case "claude":
		targets = []adapters.Target{adapters.TargetClaude}
	case "codex":
		targets = []adapters.Target{adapters.TargetCodex}
	case "all":
		targets = []adapters.Target{adapters.TargetClaude, adapters.TargetCodex}
	default:
		t.Fatalf("unsupported test agent %q", agent)
	}
	if _, err := adapters.InstallWithOptions(root.String(), targets, adapters.InstallOptions{HookCommand: "turnal"}); err != nil {
		t.Fatalf("install hooks: %v", err)
	}
	config := "version = 1\n\n[init]\nagent = '" + agent + "'\ninstall_hooks = true\n\n[hooks]\ncommand = 'turnal'\n"
	if err := os.WriteFile(filepath.Join(repo.MetadataDir, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write Turnal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root.String(), ".gitignore"), []byte(checkpoint.GitignoreEntry+"\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	return root.String()
}

type statusFileSnapshot struct {
	Mode os.FileMode
	Data string
}

func snapshotStatusTree(t *testing.T, root string) map[string]statusFileSnapshot {
	t.Helper()
	snapshot := map[string]statusFileSnapshot{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item := statusFileSnapshot{Mode: info.Mode()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			item.Data = string(data)
		}
		snapshot[relative] = item
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return snapshot
}
