package sessionhistory

import (
	"strings"
	"testing"
	"time"
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
	if candidate.ProviderSessionID != "claude-session" || candidate.WorkspaceRoot != "/workspace" || candidate.Model != "claude-opus" {
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
