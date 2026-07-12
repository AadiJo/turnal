package recall

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/adapters"
	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
)

func TestRecallTurnIncludesProviderEventsAndRawRecords(t *testing.T) {
	requireGit(t)

	tests := []struct {
		name       string
		adapter    primitives.AdapterName
		session    string
		hookName   string
		rawAdapter string
	}{
		{
			name:       "claude",
			adapter:    primitives.AdapterClaudeCode,
			session:    "Claude-Session",
			hookName:   "",
			rawAdapter: "claude-code",
		},
		{
			name:       "codex",
			adapter:    primitives.AdapterCodex,
			session:    "Codex-Session",
			hookName:   "CodexHook",
			rawAdapter: "codex",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := workspaceRoot(t)
			repo, err := checkpoint.Init(root)
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			t.Chdir(root.String())

			writeFile(t, root, "app.txt", "before\n")
			handleProviderPayload(t, test.adapter, test.hookName, "UserPromptSubmit", map[string]any{
				"cwd":        root.String(),
				"session_id": test.session,
				"prompt":     "change app.txt",
			})
			writeFile(t, root, "app.txt", "after\n")
			handleProviderPayload(t, test.adapter, test.hookName, "PostToolUse", map[string]any{
				"cwd":           root.String(),
				"session_id":    test.session,
				"tool_name":     "Write",
				"tool_use_id":   "tool-1",
				"tool_input":    map[string]any{"file_path": "app.txt", "content": "after\n"},
				"tool_response": map[string]any{"ok": true},
			})
			handleProviderPayload(t, test.adapter, test.hookName, "Stop", map[string]any{
				"cwd":                    root.String(),
				"session_id":             test.session,
				"last_assistant_message": "done",
			})

			sessionID := sessionID(t, test.session)
			turnID, _ := primitives.NewTurnID(1)
			turn, err := NewReader(repo.MetadataDir).RecallTurn(sessionID, turnID, Options{IncludeRaw: true})
			if err != nil {
				t.Fatalf("RecallTurn: %v", err)
			}
			if !turn.Complete {
				t.Fatalf("turn.Complete=false, want true: %#v", turn)
			}
			if eventSeqValue(turn.PreCheckpoint.EventSeqStart) != 1 || eventSeqValue(turn.PreCheckpoint.EventSeqEnd) != 3 {
				t.Fatalf("pre checkpoint event range = %v-%v, want 1-3", turn.PreCheckpoint.EventSeqStart, turn.PreCheckpoint.EventSeqEnd)
			}
			if eventSeqValue(turn.PostCheckpoint.EventSeqStart) != 4 || eventSeqValue(turn.PostCheckpoint.EventSeqEnd) != 9 {
				t.Fatalf("post checkpoint event range = %v-%v, want 4-9", turn.PostCheckpoint.EventSeqStart, turn.PostCheckpoint.EventSeqEnd)
			}
			if len(turn.SessionEvents) != 1 {
				t.Fatalf("session events len = %d, want 1", len(turn.SessionEvents))
			}
			if !hasEventTypes(turn.Events, primitives.EventTypePromptUser, primitives.EventTypeToolCall, primitives.EventTypeToolResult, primitives.EventTypeAssistantMessage, primitives.EventTypeTurnFinish) {
				t.Fatalf("turn events missing expected types: %#v", eventTypes(turn.Events))
			}
			if len(turn.RawRecords) != 3 {
				t.Fatalf("raw records len = %d, want 3: %#v", len(turn.RawRecords), turn.RawRecords)
			}
			for _, rawRecord := range turn.RawRecords {
				if !strings.Contains(rawRecord.RawRef, ":"+test.rawAdapter+":") {
					t.Fatalf("raw ref = %q, want v2 session ref for %s", rawRecord.RawRef, test.rawAdapter)
				}
				if rawRecord.Record.Adapter != test.adapter {
					t.Fatalf("raw record adapter = %s, want %s", rawRecord.Record.Adapter, test.adapter)
				}
			}
			if len(turn.RawRecordErrors) != 0 {
				t.Fatalf("raw record errors = %#v", turn.RawRecordErrors)
			}
		})
	}
}

func TestRecallTurnReportsMalformedRawRecordWithoutDroppingEvents(t *testing.T) {
	metadataDir := t.TempDir()
	log := eventlog.Open(metadataDir)
	sessionID := sessionID(t, "codex-session")
	turnID, _ := primitives.NewTurnID(1)

	if _, err := log.Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypePromptUser,
		Adapter:   primitives.AdapterCodex,
		RawRef:    "codex:1",
		Payload:   json.RawMessage(`{"text":"hello"}`),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	adapterLogDir := filepath.Join(metadataDir, "log", "adapter")
	if err := os.MkdirAll(adapterLogDir, 0o755); err != nil {
		t.Fatalf("mkdir adapter log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(adapterLogDir, "codex.jsonl"), []byte("{broken}\n"), 0o644); err != nil {
		t.Fatalf("write malformed raw record: %v", err)
	}

	turn, err := NewReader(metadataDir).RecallTurn(sessionID, turnID, Options{IncludeRaw: true})
	if err != nil {
		t.Fatalf("RecallTurn: %v", err)
	}
	if len(turn.Events) != 1 {
		t.Fatalf("events len = %d, want 1", len(turn.Events))
	}
	if len(turn.RawRecords) != 0 {
		t.Fatalf("raw records len = %d, want 0", len(turn.RawRecords))
	}
	if len(turn.RawRecordErrors) != 1 || !strings.Contains(turn.RawRecordErrors[0].Error, "malformed JSON") {
		t.Fatalf("raw errors = %#v, want malformed JSON", turn.RawRecordErrors)
	}
}

func TestRecallTurnIncludesClaudeTranscriptOutput(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	t.Setenv("CLAUDE_CONFIG_DIR", root.String())

	transcriptPath := filepath.Join(root.String(), "claude-transcript.jsonl")
	writeFile(t, root, "claude-transcript.jsonl", strings.Join([]string{
		`{"type":"user","uuid":"u1","timestamp":"2026-07-06T12:00:00Z","message":{"role":"user","content":"change app.txt"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","timestamp":"2026-07-06T12:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"First Claude output line."},{"type":"tool_use","name":"Write"}]}}`,
		`{"type":"assistant","uuid":"a2","parentUuid":"a1","timestamp":"2026-07-06T12:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"Final Claude output line."}]}}`,
		`{"type":"user","uuid":"u2","timestamp":"2026-07-06T12:01:00Z","message":{"role":"user","content":"next prompt"}}`,
	}, "\n")+"\n")

	writeFile(t, root, "app.txt", "before\n")
	handleProviderPayload(t, primitives.AdapterClaudeCode, "", "UserPromptSubmit", map[string]any{
		"cwd":             root.String(),
		"session_id":      "Claude-Session",
		"transcript_path": transcriptPath,
		"prompt":          "change app.txt",
	})
	writeFile(t, root, "app.txt", "after\n")
	handleProviderPayload(t, primitives.AdapterClaudeCode, "", "Stop", map[string]any{
		"cwd":                    root.String(),
		"session_id":             "Claude-Session",
		"transcript_path":        transcriptPath,
		"last_assistant_message": "short hook output",
	})

	sessionID := sessionID(t, "claude-session")
	turnID, _ := primitives.NewTurnID(1)
	turn, err := NewReader(repo.MetadataDir).RecallTurn(sessionID, turnID, Options{IncludeTranscript: true})
	if err != nil {
		t.Fatalf("RecallTurn: %v", err)
	}
	if turn.Transcript == nil {
		t.Fatal("Transcript=nil")
	}
	if len(turn.Transcript.Errors) != 0 {
		t.Fatalf("transcript errors = %#v", turn.Transcript.Errors)
	}
	if len(turn.Transcript.Messages) != 3 {
		t.Fatalf("transcript messages len = %d, want user plus two assistant messages: %#v", len(turn.Transcript.Messages), turn.Transcript.Messages)
	}
	if turn.Transcript.Messages[0].Role != "user" || turn.Transcript.Messages[0].Text != "change app.txt" {
		t.Fatalf("first transcript message = %#v, want user prompt", turn.Transcript.Messages[0])
	}
	got := transcriptText(turn.Transcript)
	for _, want := range []string{"change app.txt", "First Claude output line.", "Final Claude output line."} {
		if !strings.Contains(got, want) {
			t.Fatalf("transcript text = %q, want %q", got, want)
		}
	}
	if strings.Contains(transcriptText(turn.Transcript), "next prompt") {
		t.Fatalf("transcript leaked next prompt: %#v", turn.Transcript.Messages)
	}
}

func TestRecallTurnIncludesCodexTranscriptOutput(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	t.Setenv("CODEX_HOME", root.String())

	transcriptPath := filepath.Join(root.String(), "codex-transcript.json")
	writeFile(t, root, "codex-transcript.json", `{
  "items": [
    {"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"change codex"}]}},
    {"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Codex full model output."}]}},
    {"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"another turn"}]}},
    {"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Do not include this."}]}}
  ]
}`)

	writeFile(t, root, "codex.txt", "one\n")
	handleProviderPayload(t, primitives.AdapterCodex, "CodexHook", "UserPromptSubmit", map[string]any{
		"cwd":             root.String(),
		"session_id":      "Codex-Session",
		"transcript_path": transcriptPath,
		"prompt":          "change codex",
	})
	writeFile(t, root, "codex.txt", "two\n")
	handleProviderPayload(t, primitives.AdapterCodex, "CodexHook", "Stop", map[string]any{
		"cwd":                    root.String(),
		"session_id":             "Codex-Session",
		"transcript_path":        transcriptPath,
		"last_assistant_message": "short codex hook output",
	})

	sessionID := sessionID(t, "codex-session")
	turnID, _ := primitives.NewTurnID(1)
	turn, err := NewReader(repo.MetadataDir).RecallTurn(sessionID, turnID, Options{IncludeTranscript: true})
	if err != nil {
		t.Fatalf("RecallTurn: %v", err)
	}
	if turn.Transcript == nil {
		t.Fatal("Transcript=nil")
	}
	if len(turn.Transcript.Errors) != 0 {
		t.Fatalf("transcript errors = %#v", turn.Transcript.Errors)
	}
	if len(turn.Transcript.Messages) != 2 || turn.Transcript.Messages[0].Role != "user" || turn.Transcript.Messages[1].Role != "assistant" {
		t.Fatalf("transcript messages = %#v, want user plus assistant", turn.Transcript.Messages)
	}
	got := transcriptText(turn.Transcript)
	for _, want := range []string{"change codex", "Codex full model output."} {
		if !strings.Contains(got, want) {
			t.Fatalf("transcript text = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "Do not include this.") {
		t.Fatalf("transcript leaked next turn: %#v", turn.Transcript.Messages)
	}
}

func TestRecallTurnTranscriptReportsInvalidPathWithoutFailingRecall(t *testing.T) {
	metadataDir := t.TempDir()
	log := eventlog.Open(metadataDir)
	sessionID := sessionID(t, "claude-session")
	turnID, _ := primitives.NewTurnID(1)

	if _, err := log.Append(eventlog.AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypeSessionStart,
		Adapter:   primitives.AdapterClaudeCode,
		Payload:   json.RawMessage(`{"provider_session_id":"claude-session","transcript_path":"relative.jsonl"}`),
	}); err != nil {
		t.Fatalf("Append session: %v", err)
	}
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypePromptUser,
		Adapter:   primitives.AdapterClaudeCode,
		Payload:   json.RawMessage(`{"text":"hello"}`),
	}); err != nil {
		t.Fatalf("Append prompt: %v", err)
	}

	turn, err := NewReader(metadataDir).RecallTurn(sessionID, turnID, Options{IncludeTranscript: true})
	if err != nil {
		t.Fatalf("RecallTurn: %v", err)
	}
	if turn.Transcript == nil {
		t.Fatal("Transcript=nil")
	}
	if len(turn.Transcript.Errors) != 1 || !strings.Contains(turn.Transcript.Errors[0], "must be absolute") {
		t.Fatalf("transcript errors = %#v, want absolute path error", turn.Transcript.Errors)
	}
}

func TestRecallTurnMissingTurnErrors(t *testing.T) {
	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)

	_, err := NewReader(t.TempDir()).RecallTurn(sessionID, turnID, Options{})
	if err == nil {
		t.Fatal("RecallTurn succeeded for missing turn")
	}
	if !strings.Contains(err.Error(), "no events found") {
		t.Fatalf("RecallTurn error = %v, want no events found", err)
	}
}

func TestParseCheckpointPayloadRejectsMismatchedScopedIdentity(t *testing.T) {
	sessionID, _ := primitives.ParseSessionID("scoped-mismatch")
	turnID, _ := primitives.NewTurnID(1)
	worktreeID, err := primitives.NewWorktreeID()
	if err != nil {
		t.Fatalf("NewWorktreeID: %v", err)
	}
	otherWorktreeID, err := primitives.NewWorktreeID()
	if err != nil {
		t.Fatalf("NewWorktreeID: %v", err)
	}
	producerID, err := primitives.NewEventProducerID()
	if err != nil {
		t.Fatalf("NewEventProducerID: %v", err)
	}
	streamID, err := primitives.DeriveEventStreamID(producerID, sessionID)
	if err != nil {
		t.Fatalf("DeriveEventStreamID: %v", err)
	}
	ref, err := primitives.NewScopedCheckpointRef(otherWorktreeID, streamID, sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("NewScopedCheckpointRef: %v", err)
	}
	payload, err := json.Marshal(map[string]any{
		"turn":        1,
		"phase":       "pre",
		"worktree_id": worktreeID,
		"stream_id":   streamID,
		"commit_sha":  strings.Repeat("a", 40),
		"ref":         ref,
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	_, err = parseCheckpointPayload(sessionID, turnID, eventlog.Event{
		WorktreeID: worktreeID,
		StreamID:   streamID,
		Payload:    payload,
	})
	if err == nil || !strings.Contains(err.Error(), "checkpoint ref worktree") {
		t.Fatalf("parseCheckpointPayload error = %v", err)
	}
}

func TestValidateSelectedWorktreeRejectsForeignEvent(t *testing.T) {
	sessionID, _ := primitives.ParseSessionID("foreign-event")
	turnID, _ := primitives.NewTurnID(1)
	selected, err := primitives.NewWorktreeID()
	if err != nil {
		t.Fatalf("NewWorktreeID: %v", err)
	}
	foreign, err := primitives.NewWorktreeID()
	if err != nil {
		t.Fatalf("NewWorktreeID: %v", err)
	}

	err = validateSelectedWorktree(sessionID, turnID, selected, eventlog.Event{Seq: 3, WorktreeID: foreign})
	if err == nil || !strings.Contains(err.Error(), "does not match selected worktree") {
		t.Fatalf("validateSelectedWorktree error = %v", err)
	}
}

func TestValidateSelectedWorktreeRejectsMissingV2Identity(t *testing.T) {
	sessionID, _ := primitives.ParseSessionID("missing-v2-worktree")
	turnID, _ := primitives.NewTurnID(1)
	selected, err := primitives.NewWorktreeID()
	if err != nil {
		t.Fatalf("NewWorktreeID: %v", err)
	}

	err = validateSelectedWorktree(sessionID, turnID, selected, eventlog.Event{Version: 2, Seq: 4})
	if err == nil || !strings.Contains(err.Error(), "does not match selected worktree") {
		t.Fatalf("validateSelectedWorktree error = %v", err)
	}
}

func transcriptText(transcript *Transcript) string {
	var values []string
	for _, message := range transcript.Messages {
		values = append(values, message.Text)
	}
	return strings.Join(values, "\n")
}

func handleProviderPayload(t *testing.T, adapter primitives.AdapterName, hookName, eventName string, payload map[string]any) {
	t.Helper()
	if hookName == "" {
		hookName = eventName
	} else {
		payload["hook_event_name"] = eventName
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := adapters.HandleHookPayload(adapter, hookName, raw); err != nil {
		t.Fatalf("HandleHookPayload %s/%s: %v", adapter, eventName, err)
	}
}

func hasEventTypes(events []eventlog.Event, types ...primitives.EventType) bool {
	seen := map[primitives.EventType]bool{}
	for _, event := range events {
		seen[event.Type] = true
	}
	for _, eventType := range types {
		if !seen[eventType] {
			return false
		}
	}
	return true
}

func eventTypes(events []eventlog.Event) []primitives.EventType {
	types := make([]primitives.EventType, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}

func eventSeqValue(seq *primitives.EventSeq) uint64 {
	if seq == nil {
		return 0
	}
	return seq.Uint64()
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable not found")
	}
}

func workspaceRoot(t *testing.T) primitives.WorkspaceRoot {
	t.Helper()
	root, err := primitives.ParseWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot: %v", err)
	}
	return root
}

func sessionID(t *testing.T, value string) primitives.SessionID {
	t.Helper()
	sessionID, err := primitives.ParseSessionID(value)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	return sessionID
}

func writeFile(t *testing.T, root primitives.WorkspaceRoot, relPath, content string) {
	t.Helper()
	path := filepath.Join(root.String(), filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
}
