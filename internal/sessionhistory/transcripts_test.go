package sessionhistory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/primitives"
)

func TestClaudeTranscriptConversionPreservesPromptToolsAndAssistant(t *testing.T) {
	data := strings.Join([]string{
		`{"type":"user","uuid":"user-1","sessionId":"claude-session","cwd":"/workspace","timestamp":"2026-08-01T10:00:00Z","message":{"role":"user","content":"repair the worker"}}`,
		`{"type":"assistant","uuid":"assistant-1","sessionId":"claude-session","cwd":"/workspace","timestamp":"2026-08-01T10:00:01Z","message":{"role":"assistant","model":"claude-opus","content":[{"type":"tool_use","id":"tool-1","name":"Edit","input":{"file_path":"worker.go"}}]}}`,
		`{"type":"user","uuid":"result-1","sessionId":"claude-session","cwd":"/workspace","timestamp":"2026-08-01T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"updated"}]}}`,
		`{"type":"assistant","uuid":"assistant-2","sessionId":"claude-session","cwd":"/workspace","timestamp":"2026-08-01T10:00:03Z","message":{"role":"assistant","model":"claude-opus","content":[{"type":"text","text":"the worker now recovers"}]}}`,
	}, "\n")

	candidate, err := (claudeImportAdapter{}).Parse("/transcripts/claude-session.jsonl", []byte(data), time.Now())
	if err != nil {
		t.Fatalf("parse Claude transcript: %v", err)
	}
	if candidate.ProviderSessionID != "claude-session" || candidate.WorkspaceRoot != filepath.Clean("/workspace") || candidate.Model != "claude-opus" {
		t.Fatalf("candidate metadata = %#v", candidate)
	}
	if len(candidate.Turns) != 1 {
		t.Fatalf("turns = %#v", candidate.Turns)
	}
	turn := candidate.Turns[0]
	if turn.Prompt != "repair the worker" || turn.Assistant != "the worker now recovers" || len(turn.Tools) != 1 {
		t.Fatalf("converted turn = %#v", turn)
	}
	tool := turn.Tools[0]
	if tool.Name != "Edit" || tool.UseID != "tool-1" || string(tool.Input) != `{"file_path":"worker.go"}` || string(tool.Output) != `"updated"` {
		t.Fatalf("converted tool = %#v", tool)
	}
}

func TestCodexTranscriptConversionIgnoresDeveloperMessages(t *testing.T) {
	data := strings.Join([]string{
		`{"type":"session_meta","timestamp":"2026-08-01T10:00:00Z","payload":{"id":"codex-session","cwd":"/workspace"}}`,
		`{"type":"response_item","timestamp":"2026-08-01T10:00:01Z","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"internal instructions"}]}}`,
		`{"type":"turn_context","timestamp":"2026-08-01T10:00:02Z","payload":{"turn_id":"turn-7","model":"gpt-5.6-sol"}}`,
		`{"type":"event_msg","timestamp":"2026-08-01T10:00:03Z","payload":{"type":"user_message","message":"fix retries"}}`,
		`{"type":"response_item","timestamp":"2026-08-01T10:00:04Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fixed"}]}}`,
		`{"type":"event_msg","timestamp":"2026-08-01T10:00:05Z","payload":{"type":"agent_message","message":"fixed"}}`,
	}, "\n")

	candidate, err := (codexImportAdapter{}).Parse("/transcripts/codex-session.jsonl", []byte(data), time.Now())
	if err != nil {
		t.Fatalf("parse Codex transcript: %v", err)
	}
	if len(candidate.Turns) != 1 || candidate.Turns[0].Prompt != "fix retries" || candidate.Turns[0].Assistant != "fixed" {
		t.Fatalf("converted candidate = %#v", candidate)
	}
	if strings.Contains(candidate.Turns[0].Prompt, "internal instructions") {
		t.Fatalf("developer message leaked into imported prompt: %#v", candidate.Turns[0])
	}
}

func TestDiscoverIncludesSessionsStartedBelowWorkspaceRoot(t *testing.T) {
	workspace := t.TempDir()
	nested := filepath.Join(workspace, "src", "worker")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("create nested working directory: %v", err)
	}
	transcriptDir := t.TempDir()
	writeTranscriptFixture(t, filepath.Join(transcriptDir, "nested.jsonl"), codexTranscript("nested-session", nested, "repair the worker"))
	root, err := primitives.ParseWorkspaceRoot(workspace)
	if err != nil {
		t.Fatal(err)
	}

	candidates, warnings, err := Discover(DiscoverOptions{
		Adapter: primitives.AdapterCodex, WorkspaceRoot: root, Path: transcriptDir,
		Now: time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("discover nested session: %v", err)
	}
	if len(warnings) != 0 || len(candidates) != 1 || candidates[0].ProviderSessionID != "nested-session" {
		t.Fatalf("nested discovery candidates=%#v warnings=%#v", candidates, warnings)
	}
}

func TestDiscoverConsolidatesIdenticalSessionTranscripts(t *testing.T) {
	workspace := t.TempDir()
	transcriptDir := t.TempDir()
	data := codexTranscript("duplicate-session", workspace, "repair the worker")
	writeTranscriptFixture(t, filepath.Join(transcriptDir, "a.jsonl"), data)
	writeTranscriptFixture(t, filepath.Join(transcriptDir, "b.jsonl"), data)
	root, _ := primitives.ParseWorkspaceRoot(workspace)

	candidates, warnings, err := Discover(DiscoverOptions{
		Adapter: primitives.AdapterCodex, WorkspaceRoot: root, Path: transcriptDir,
		Now: time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("discover duplicate session: %v", err)
	}
	if len(candidates) != 1 || len(warnings) != 1 || !strings.Contains(warnings[0], "ignored duplicate") {
		t.Fatalf("duplicate discovery candidates=%#v warnings=%#v", candidates, warnings)
	}
}

func TestDiscoverRejectsDivergentSessionTranscriptsBeforePlanning(t *testing.T) {
	workspace := t.TempDir()
	transcriptDir := t.TempDir()
	writeTranscriptFixture(t, filepath.Join(transcriptDir, "a.jsonl"), codexTranscript("duplicate-session", workspace, "first copy"))
	writeTranscriptFixture(t, filepath.Join(transcriptDir, "b.jsonl"), codexTranscript("duplicate-session", workspace, "different copy"))
	root, _ := primitives.ParseWorkspaceRoot(workspace)

	candidates, _, err := Discover(DiscoverOptions{
		Adapter: primitives.AdapterCodex, WorkspaceRoot: root, Path: transcriptDir,
		Now: time.Now().UTC().Add(time.Minute),
	})
	if err == nil || !strings.Contains(err.Error(), "different contents") {
		t.Fatalf("divergent duplicate error = %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("divergent duplicate candidates = %#v", candidates)
	}
}

func codexTranscript(sessionID, cwd, prompt string) string {
	return strings.Join([]string{
		`{"type":"session_meta","timestamp":"2026-08-01T10:00:00Z","payload":{"id":` + quoted(sessionID) + `,"cwd":` + quoted(cwd) + `}}`,
		`{"type":"event_msg","timestamp":"2026-08-01T10:00:01Z","payload":{"type":"user_message","message":` + quoted(prompt) + `}}`,
	}, "\n") + "\n"
}

func quoted(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func writeTranscriptFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write transcript fixture: %v", err)
	}
}
