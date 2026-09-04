package adapters

import (
	"encoding/json"
	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
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
	if got == nil || !got.HasUsage || got.Total != want {
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
	want := usage.TokenUsage{InputTokens: 150, CacheReadTokens: 100, OutputTokens: 55, ReasoningTokens: 21}
	if got == nil || !got.HasUsage || got.Total != want {
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

func TestClaudeUsagePreservesMissingReadings(t *testing.T) {
	for _, field := range []string{"", `,"usage":null`} {
		t.Run(field, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session-1.jsonl")
			data := `{"type":"assistant","sessionId":"session-1","cwd":"/workspace","message":{"id":"msg-1","role":"assistant"` + field + `}}` + "\n"
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			got := claudeCumulativeUsage(hookPayload{SessionID: "session-1", CWD: "/workspace", TranscriptPath: path})
			if got == nil || got.HasUsage || got.Total != (usage.TokenUsage{}) {
				t.Fatalf("reading = %+v, want readable with no usage", got)
			}
		})
	}
}

func TestCodexUsageDoesNotInferCallsFromNotifications(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	notification := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"output_tokens":20}}}}` + "\n"
	data := `{"type":"session_meta","payload":{"id":"session-1"}}` + "\n" + notification + notification
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got := codexCumulativeUsage(hookPayload{SessionID: "session-1", TranscriptPath: path})
	if got == nil || !got.HasUsage || got.Total.APICalls != 0 || got.Total.Tokens() != 120 {
		t.Fatalf("reading = %+v, want 120 tokens and unavailable call count", got)
	}
}

func TestUsageCaptureUsesCurrentPromptBaseline(t *testing.T) {
	requireGit(t)
	for _, adapter := range []primitives.AdapterName{primitives.AdapterClaudeCode, primitives.AdapterCodex} {
		for _, scenario := range []struct {
			name        string
			historical  int64
			interrupted bool
			unreadable  bool
			unchanged   bool
			reset       bool
		}{
			{name: "fresh"},
			{name: "resumed", historical: 100},
			{name: "interrupted", historical: 100, interrupted: true},
			{name: "unreadable baseline", historical: 100, unreadable: true},
			{name: "no new reading", historical: 100, unchanged: true},
			{name: "counter reset", historical: 100, reset: true},
		} {
			t.Run(adapter.String()+"/"+scenario.name, func(t *testing.T) {
				root := workspaceRoot(t)
				repo, err := checkpoint.Init(root)
				if err != nil {
					t.Fatal(err)
				}
				t.Chdir(root.String())
				session := sessionID(t, "usage-session")
				path := filepath.Join(t.TempDir(), session.String()+".jsonl")
				var transcript string
				if adapter == primitives.AdapterCodex {
					transcript = `{"type":"session_meta","payload":{"id":"usage-session"}}` + "\n"
				}
				total := int64(0)
				appendReading := func(input int64, id string) {
					t.Helper()
					total += input
					var record any
					if adapter == primitives.AdapterClaudeCode {
						record = map[string]any{"type": "assistant", "sessionId": session.String(), "cwd": root.String(), "message": map[string]any{"id": id, "role": "assistant", "usage": map[string]int64{"input_tokens": input}}}
					} else {
						record = map[string]any{"type": "event_msg", "payload": map[string]any{"type": "token_count", "info": map[string]any{"total_token_usage": map[string]int64{"input_tokens": total}}}}
					}
					encoded, err := json.Marshal(record)
					if err != nil {
						t.Fatal(err)
					}
					transcript += string(encoded) + "\n"
					if err := os.WriteFile(path, []byte(transcript), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if scenario.historical > 0 {
					appendReading(scenario.historical, "historical")
				}
				if scenario.unreadable {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				payload := map[string]any{"session_id": session.String(), "cwd": root.String(), "transcript_path": path, "prompt": "first", "model": "gpt-5.4"}
				handlePayload(t, adapter, "UserPromptSubmit", payload)
				if scenario.unreadable {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
				if scenario.interrupted {
					appendReading(30, "interrupted")
					payload["prompt"] = "second"
					handlePayload(t, adapter, "UserPromptSubmit", payload)
				}
				if scenario.reset {
					total = 0
					transcript = ""
					if adapter == primitives.AdapterCodex {
						transcript = `{"type":"session_meta","payload":{"id":"usage-session"}}` + "\n"
					}
				}
				if !scenario.unchanged {
					appendReading(50, "new")
				}
				payload["last_assistant_message"] = "done"
				handlePayload(t, adapter, "Stop", payload)
				// Duplicate delivery must not add usage twice.
				handlePayload(t, adapter, "Stop", payload)
				events := readEvents(t, repo, session)
				assistants := 0
				for _, event := range events {
					if event.Type != primitives.EventTypeAssistantMessage {
						continue
					}
					assistants++
					var got assistantPayload
					if err := json.Unmarshal(event.Payload, &got); err != nil {
						t.Fatal(err)
					}
					if scenario.unreadable || scenario.unchanged || scenario.reset {
						if got.Usage != nil {
							t.Fatalf("usage = %+v, want missing", got.Usage)
						}
					} else if got.Usage == nil || got.Usage.InputTokens != 50 {
						t.Fatalf("usage = %+v, want 50 new input tokens", got.Usage)
					}
				}
				if assistants != 1 {
					t.Fatalf("assistant events = %d, want 1", assistants)
				}
			})
		}
	}
}
