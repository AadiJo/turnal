package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/adapters"
	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
)

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
	blockedPath := filepath.Join(repo.MetadataDir, "log", "raw", "failed-hook", "codex.jsonl")
	if err := os.MkdirAll(blockedPath, 0o700); err != nil {
		t.Fatalf("create blocking adapter log directory: %v", err)
	}

	payload := `{"cwd":` + strconvQuote(rootPath) + `,"session_id":"failed-hook","hook_event_name":"after_agent"}`
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
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}
