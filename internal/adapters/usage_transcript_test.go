package adapters

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/AadiJo/turnal/internal/usage"
)

func TestClaudeCumulativeUsageDeduplicatesStreamingMessages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-1.jsonl")
	data := `{"type":"assistant","sessionId":"session-1","cwd":"/workspace","message":{"id":"msg-1","role":"assistant","model":"claude-sonnet-5","content":[],"usage":{"input_tokens":10,"output_tokens":2,"cache_creation_input_tokens":3,"cache_read_input_tokens":4}}}
{"type":"assistant","sessionId":"session-1","cwd":"/workspace","message":{"id":"msg-1","role":"assistant","model":"claude-sonnet-5","content":[],"usage":{"input_tokens":10,"output_tokens":7,"cache_creation_input_tokens":3,"cache_read_input_tokens":4}}}
{"type":"assistant","sessionId":"session-1","cwd":"/workspace","message":{"id":"msg-2","role":"assistant","model":"claude-sonnet-5","content":[],"usage":{"input_tokens":20,"output_tokens":5,"cache_read_input_tokens":6}}}
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got := claudeCumulativeUsage(hookPayload{SessionID: "session-1", CWD: "/workspace", TranscriptPath: path})
	want := usage.TokenUsage{InputTokens: 30, CacheReadTokens: 10, CacheWriteTokens: 3, OutputTokens: 12, APICalls: 2}
	if got == nil || *got != want {
		t.Fatalf("claudeCumulativeUsage() = %+v, want %+v", got, want)
	}
}

func TestCodexCumulativeUsageUsesLatestTotal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	data := `{"type":"session_meta","payload":{"id":"session-1"}}
{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20,"reasoning_output_tokens":8}}}}
{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":250,"cached_input_tokens":100,"output_tokens":55,"reasoning_output_tokens":21}}}}
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got := codexCumulativeUsage(hookPayload{SessionID: "session-1", TranscriptPath: path})
	want := usage.TokenUsage{InputTokens: 150, CacheReadTokens: 100, OutputTokens: 55, ReasoningTokens: 21, APICalls: 2}
	if got == nil || *got != want {
		t.Fatalf("codexCumulativeUsage() = %+v, want %+v", got, want)
	}
}

func TestCodexCumulativeUsageRejectsAnotherSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	data := `{"type":"session_meta","payload":{"id":"another-session"}}
{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"output_tokens":2}}}}
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := codexCumulativeUsage(hookPayload{SessionID: "session-1", TranscriptPath: path}); got != nil {
		t.Fatalf("codexCumulativeUsage() = %+v, want nil", got)
	}
}
