package cli

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/adapters"
	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
)

func TestClaudeAndCodexPromptHooksInjectIntentCommand(t *testing.T) {
	for _, test := range []struct {
		name    string
		args    []string
		payload func(string) string
	}{
		{
			name: "claude code",
			args: []string{"claude-hook", "user"},
			payload: func(root string) string {
				return `{"cwd":` + strconvQuote(root) + `,"session_id":"claude-intent","prompt":"fix it"}`
			},
		},
		{
			name: "codex",
			args: []string{"codex-hook"},
			payload: func(root string) string {
				return `{"cwd":` + strconvQuote(root) + `,"session_id":"codex-intent","hook_event_name":"UserPromptSubmit","prompt":"fix it"}`
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			rootPath := t.TempDir()
			root, err := primitives.ParseWorkspaceRoot(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := checkpoint.Init(root); err != nil {
				t.Fatal(err)
			}
			t.Chdir(rootPath)
			var stdout bytes.Buffer
			cmd := NewRootCmd()
			cmd.SetArgs(test.args)
			cmd.SetIn(strings.NewReader(test.payload(rootPath)))
			cmd.SetOut(&stdout)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("prompt hook: %v", err)
			}
			var output struct {
				HookSpecificOutput struct {
					HookEventName     string `json:"hookEventName"`
					AdditionalContext string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
				t.Fatalf("decode hook output: %v\n%s", err, stdout.String())
			}
			if output.HookSpecificOutput.HookEventName != "UserPromptSubmit" || !strings.Contains(output.HookSpecificOutput.AdditionalContext, "turnal intent --session") || !strings.Contains(output.HookSpecificOutput.AdditionalContext, "not edit steps or hidden reasoning") {
				t.Fatalf("hook output = %#v", output)
			}
		})
	}
}

func TestCodexHookFailsOpenButRecordsVisibleFailure(t *testing.T) {
	rootPath := t.TempDir()
	root, err := primitives.ParseWorkspaceRoot(rootPath)
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot: %v", err)
	}
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(rootPath)

	payload := `{"cwd":` + strconvQuote(rootPath) + `,"session_id":"failed-hook","hook_event_name":"after_agent"}`
	payload += strings.Repeat(" ", adapters.MaxHookPayloadBytes-len(payload)+1)
	var stderr bytes.Buffer
	cmd := NewRootCmd()
	cmd.SetArgs([]string{"codex-hook"})
	cmd.SetIn(strings.NewReader(payload))
	cmd.SetErr(&stderr)
	cmd.SetOut(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("codex-hook returned error instead of failing open: %v", err)
	}
	if !strings.Contains(stderr.String(), "hook capture failed") {
		t.Fatalf("stderr = %q, want visible hook failure", stderr.String())
	}
	failures, err := adapters.ReadHookFailures(repo.MetadataDir)
	if err != nil {
		t.Fatalf("ReadHookFailures: %v", err)
	}
	if len(failures) != 1 || failures[0].SessionID != "failed-hook" {
		t.Fatalf("failures = %#v, want durable failed-hook diagnostic", failures)
	}
}

func TestReadHookPayloadRejectsOversizeInput(t *testing.T) {
	_, err := readHookPayload(strings.NewReader(strings.Repeat("x", adapters.MaxHookPayloadBytes+1)))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("readHookPayload error = %v, want size limit", err)
	}
}

func TestClaudeSessionHookMapsToSessionStart(t *testing.T) {
	name, err := claudeHookName("session")
	if err != nil || name != "SessionStart" {
		t.Fatalf("claudeHookName(session) = %q, %v", name, err)
	}
}

func TestClaudeHookInvalidKindFailsOpenAndRecordsFailure(t *testing.T) {
	rootPath := t.TempDir()
	root, err := primitives.ParseWorkspaceRoot(rootPath)
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot: %v", err)
	}
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(rootPath)
	payload := `{"cwd":` + strconvQuote(rootPath) + `,"session_id":"bad-kind"}`
	var stderr bytes.Buffer
	cmd := NewRootCmd()
	cmd.SetArgs([]string{"claude-hook", "future-kind"})
	cmd.SetIn(strings.NewReader(payload))
	cmd.SetErr(&stderr)
	cmd.SetOut(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("claude-hook returned error instead of failing open: %v", err)
	}
	if !strings.Contains(stderr.String(), "invalid Claude hook") {
		t.Fatalf("stderr = %q, want invalid hook diagnostic", stderr.String())
	}
	failures, err := adapters.ReadHookFailures(repo.MetadataDir)
	if err != nil {
		t.Fatalf("ReadHookFailures: %v", err)
	}
	if len(failures) != 1 || failures[0].Hook != "UnknownClaudeHook" {
		t.Fatalf("failures = %#v, want UnknownClaudeHook diagnostic", failures)
	}
}

func strconvQuote(value string) string {
	return strconv.Quote(value)
}
