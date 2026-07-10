package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/turns"
	adaptersdk "github.com/AadiJo/turnal/sdk/adapter"
)

type checkpointEventPayload struct {
	Turn          uint64 `json:"turn"`
	Phase         string `json:"phase"`
	CommitSHA     string `json:"commit_sha"`
	Ref           string `json:"ref"`
	EventSeqStart uint64 `json:"event_seq_start"`
	EventSeqEnd   uint64 `json:"event_seq_end"`
}

func TestHandleNormalizedEventsKeepsDurabilityInCore(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, "app.txt", "before\n")

	rawPrompt := []byte(`{"sessionId":"external-session","cwd":"` + root.String() + `","prompt":"change app.txt"}`)
	err = HandleNormalizedEvents("gemini-cli", "BeforeAgent", rawPrompt, []adaptersdk.Event{{
		Type: adaptersdk.EventPromptUser, SessionID: "external-session", CWD: root.String(), Text: "change app.txt",
	}})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	writeFile(t, root, "app.txt", "after\n")
	rawFinish := []byte(`{"session_id":"external-session","cwd":"` + root.String() + `","prompt_response":"done"}`)
	err = HandleNormalizedEvents("gemini-cli", "AfterAgent", rawFinish, []adaptersdk.Event{{
		Type: adaptersdk.EventAssistantMessage, SessionID: "external-session", CWD: root.String(), Text: "done",
	}})
	if err != nil {
		t.Fatalf("assistant: %v", err)
	}

	sessionID := sessionID(t, "external-session")
	events := readEvents(t, repo, sessionID)
	if countEvents(events, primitives.EventTypePromptUser) != 1 || countEvents(events, primitives.EventTypeAssistantMessage) != 1 || countEvents(events, primitives.EventTypeCheckpoint) != 2 {
		t.Fatalf("unexpected events: %#v", eventTypes(events))
	}
	for _, event := range events {
		if event.Adapter != "gemini-cli" || event.RawRef == "" {
			t.Fatalf("event did not retain external adapter provenance: %#v", event)
		}
	}
	turnID, _ := primitives.NewTurnID(1)
	diff, err := repo.DiffTurn(sessionID, turnID)
	if err != nil || !containsAll(string(diff), "-before", "+after") {
		t.Fatalf("DiffTurn = %q, err=%v", diff, err)
	}
}

func TestHandleClaudeHookPayloadCreatesAutomaticTurn(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	writeFile(t, root, "app.txt", "before\n")
	handlePayload(t, primitives.AdapterClaudeCode, "UserPromptSubmit", map[string]any{
		"cwd":        root.String(),
		"session_id": "Claude-Session",
		"prompt":     "change app.txt",
	})
	writeFile(t, root, "app.txt", "after\n")
	handlePayload(t, primitives.AdapterClaudeCode, "PostToolUse", map[string]any{
		"cwd":           root.String(),
		"session_id":    "Claude-Session",
		"tool_name":     "Write",
		"tool_use_id":   "tool-1",
		"tool_input":    map[string]any{"file_path": "app.txt", "content": "after\n"},
		"tool_response": map[string]any{"ok": true},
	})
	handlePayload(t, primitives.AdapterClaudeCode, "Stop", map[string]any{
		"cwd":                    root.String(),
		"session_id":             "Claude-Session",
		"last_assistant_message": "done",
	})

	sessionID := sessionID(t, "claude-session")
	turnID, _ := primitives.NewTurnID(1)
	diff, err := repo.DiffTurn(sessionID, turnID)
	if err != nil {
		t.Fatalf("DiffTurn: %v", err)
	}
	diffText := string(diff)
	if !containsAll(diffText, "diff --git a/app.txt b/app.txt", "-before", "+after") {
		t.Fatalf("unexpected diff:\n%s", diffText)
	}

	events := readEvents(t, repo, sessionID)
	wantTypes := []primitives.EventType{
		primitives.EventTypeSessionStart,
		primitives.EventTypeTurnStart,
		primitives.EventTypeCheckpoint,
		primitives.EventTypePromptUser,
		primitives.EventTypeToolCall,
		primitives.EventTypeToolResult,
		primitives.EventTypeAssistantMessage,
		primitives.EventTypeTurnFinish,
		primitives.EventTypeCheckpoint,
	}
	if got := eventTypes(events); !sameEventTypes(got, wantTypes) {
		t.Fatalf("event types = %#v, want %#v", got, wantTypes)
	}
	preCheckpoint := checkpointPayloadForPhase(t, events, primitives.CheckpointPhasePre)
	if preCheckpoint.EventSeqStart != 1 || preCheckpoint.EventSeqEnd != 3 {
		t.Fatalf("pre checkpoint event range = %d-%d, want 1-3", preCheckpoint.EventSeqStart, preCheckpoint.EventSeqEnd)
	}
	postCheckpoint := checkpointPayloadForPhase(t, events, primitives.CheckpointPhasePost)
	if postCheckpoint.EventSeqStart != 4 || postCheckpoint.EventSeqEnd != 9 {
		t.Fatalf("post checkpoint event range = %d-%d, want 4-9", postCheckpoint.EventSeqStart, postCheckpoint.EventSeqEnd)
	}
	for _, event := range events {
		if event.RawRef == "" {
			t.Fatalf("event missing raw ref: %#v", event)
		}
	}

	if _, ok, err := turns.NewManager(repo).Active(sessionID); err != nil {
		t.Fatalf("Active: %v", err)
	} else if ok {
		t.Fatal("turn still active after Stop")
	}
}

func TestHandleHookPayloadIsIdempotentForDuplicatePrompt(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	payload := map[string]any{
		"cwd":        root.String(),
		"session_id": "codex-session",
		"prompt":     "make change",
	}
	handlePayload(t, primitives.AdapterCodex, "UserPromptSubmit", payload)
	handlePayload(t, primitives.AdapterCodex, "UserPromptSubmit", payload)

	sessionID := sessionID(t, "codex-session")
	events := readEvents(t, repo, sessionID)
	if countEvents(events, primitives.EventTypePromptUser) != 1 {
		t.Fatalf("prompt events = %d, want 1; events=%#v", countEvents(events, primitives.EventTypePromptUser), events)
	}
	if countEvents(events, primitives.EventTypeTurnStart) != 1 {
		t.Fatalf("turn start events = %d, want 1", countEvents(events, primitives.EventTypeTurnStart))
	}
	active, ok, err := turns.NewManager(repo).Active(sessionID)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if !ok || active.TurnID.Uint64() != 1 {
		t.Fatalf("active = %#v ok=%v, want turn 1 active", active, ok)
	}
}

func TestHandleHookPayloadAppliesSecretsRedactionPolicy(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, ".turnal/config.toml", `
version = 1

[secrets]
store_prompts = false
store_tool_io = false
`)

	handlePayload(t, primitives.AdapterClaudeCode, "UserPromptSubmit", map[string]any{
		"cwd":        root.String(),
		"session_id": "secret-session",
		"prompt":     "token=secret",
	})
	handlePayload(t, primitives.AdapterClaudeCode, "PostToolUse", map[string]any{
		"cwd":           root.String(),
		"session_id":    "secret-session",
		"tool_name":     "Write",
		"tool_use_id":   "tool-1",
		"tool_input":    map[string]any{"content": "secret"},
		"tool_response": map[string]any{"output": "secret"},
	})

	sessionID := sessionID(t, "secret-session")
	events := readEvents(t, repo, sessionID)
	for _, event := range events {
		switch event.Type {
		case primitives.EventTypePromptUser:
			var payload promptPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatalf("unmarshal prompt payload: %v", err)
			}
			if strings.Contains(payload.Text, "token=secret") || !strings.Contains(payload.Text, "redacted") {
				t.Fatalf("prompt text not redacted: %#v", payload)
			}
			rawRecord, err := ReadRawHookRecord(repo.MetadataDir, event.RawRef)
			if err != nil {
				t.Fatalf("ReadRawHookRecord: %v", err)
			}
			if strings.Contains(string(rawRecord.Payload), "token=secret") {
				t.Fatalf("raw prompt payload was not redacted: %s", rawRecord.Payload)
			}
		case primitives.EventTypeToolCall:
			var payload toolCallPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatalf("unmarshal tool call payload: %v", err)
			}
			if strings.Contains(string(payload.Input), `"content":"secret"`) || !strings.Contains(string(payload.Input), "redacted") {
				t.Fatalf("tool input not redacted: %s", payload.Input)
			}
		case primitives.EventTypeToolResult:
			var payload toolResultPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatalf("unmarshal tool result payload: %v", err)
			}
			if strings.Contains(string(payload.Output), `"output":"secret"`) || !strings.Contains(string(payload.Output), "redacted") {
				t.Fatalf("tool output not redacted: %s", payload.Output)
			}
		}
	}
}

func TestHandleHookPayloadSerializesConcurrentDuplicatePrompt(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	raw, err := json.Marshal(map[string]any{
		"cwd":             root.String(),
		"session_id":      "codex-session",
		"hook_event_name": "UserPromptSubmit",
		"turn_id":         "turn-1",
		"prompt":          "make change",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- HandleHookPayload(primitives.AdapterCodex, "CodexHook", raw)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("HandleHookPayload: %v", err)
		}
	}

	sessionID := sessionID(t, "codex-session")
	events := readEvents(t, repo, sessionID)
	for _, event := range events {
		if event.Type == primitives.EventTypeError {
			t.Fatalf("unexpected error event: %#v", event)
		}
	}
	if countEvents(events, primitives.EventTypeTurnStart) != 1 {
		t.Fatalf("turn starts = %d, want 1; events=%#v", countEvents(events, primitives.EventTypeTurnStart), events)
	}
	if countEvents(events, primitives.EventTypePromptUser) != 1 {
		t.Fatalf("prompt events = %d, want 1; events=%#v", countEvents(events, primitives.EventTypePromptUser), events)
	}
	if countEvents(events, primitives.EventTypeCheckpoint) != 1 {
		t.Fatalf("checkpoint events = %d, want 1; events=%#v", countEvents(events, primitives.EventTypeCheckpoint), events)
	}
}

func TestHandleCodexDocumentedHookPayloads(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	writeFile(t, root, "app.txt", "before\n")
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd":             root.String(),
		"session_id":      "codex-session",
		"hook_event_name": "SessionStart",
		"transcript_path": nil,
		"model":           "gpt-5.5",
		"permission_mode": "default",
		"source":          "startup",
	})
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd":             root.String(),
		"session_id":      "codex-session",
		"hook_event_name": "UserPromptSubmit",
		"turn_id":         "turn-1",
		"prompt":          "change app.txt",
		"permission_mode": "default",
	})
	writeFile(t, root, "app.txt", "after\n")
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd":             root.String(),
		"session_id":      "codex-session",
		"hook_event_name": "PostToolUse",
		"turn_id":         "turn-1",
		"tool_name":       "Bash",
		"tool_use_id":     "call-1",
		"tool_input":      map[string]any{"command": "printf after > app.txt"},
		"tool_response":   map[string]any{"exit_code": 0, "stdout": ""},
		"permission_mode": "default",
	})
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd":                    root.String(),
		"session_id":             "codex-session",
		"hook_event_name":        "Stop",
		"turn_id":                "turn-1",
		"stop_hook_active":       false,
		"last_assistant_message": "done",
		"permission_mode":        "default",
	})

	sessionID := sessionID(t, "codex-session")
	events := readEvents(t, repo, sessionID)
	wantTypes := []primitives.EventType{
		primitives.EventTypeSessionStart,
		primitives.EventTypeTurnStart,
		primitives.EventTypeCheckpoint,
		primitives.EventTypePromptUser,
		primitives.EventTypeToolCall,
		primitives.EventTypeToolResult,
		primitives.EventTypeAssistantMessage,
		primitives.EventTypeTurnFinish,
		primitives.EventTypeCheckpoint,
	}
	if got := eventTypes(events); !sameEventTypes(got, wantTypes) {
		t.Fatalf("event types = %#v, want %#v", got, wantTypes)
	}
	if countEvents(events, primitives.EventTypeSessionStart) != 1 {
		t.Fatalf("session starts = %d, want 1", countEvents(events, primitives.EventTypeSessionStart))
	}
}

func TestHandleCodexUnsupportedDocumentedHookFallsBackToRawEvent(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd":             root.String(),
		"session_id":      "codex-session",
		"hook_event_name": "PreCompact",
		"turn_id":         "turn-1",
		"trigger":         "manual",
	})

	sessionID := sessionID(t, "codex-session")
	events := readEvents(t, repo, sessionID)
	if len(events) != 1 || events[0].Type != primitives.EventTypeAdapterRaw {
		t.Fatalf("events = %#v, want one adapter.raw event", events)
	}
}

func TestNextPromptFinishesStaleActiveTurn(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	writeFile(t, root, "app.txt", "first\n")
	handlePayload(t, primitives.AdapterCodex, "UserPromptSubmit", map[string]any{
		"cwd":        root.String(),
		"session_id": "codex-session",
		"prompt":     "first prompt",
	})
	writeFile(t, root, "app.txt", "second\n")
	handlePayload(t, primitives.AdapterCodex, "UserPromptSubmit", map[string]any{
		"cwd":        root.String(),
		"session_id": "codex-session",
		"prompt":     "second prompt",
	})

	sessionID := sessionID(t, "codex-session")
	events := readEvents(t, repo, sessionID)
	if countEvents(events, primitives.EventTypeTurnStart) != 2 {
		t.Fatalf("turn starts = %d, want 2; events=%#v", countEvents(events, primitives.EventTypeTurnStart), events)
	}
	if countEvents(events, primitives.EventTypeTurnFinish) != 1 {
		t.Fatalf("turn finishes = %d, want 1; events=%#v", countEvents(events, primitives.EventTypeTurnFinish), events)
	}

	turn1, _ := primitives.NewTurnID(1)
	if _, err := repo.DiffTurn(sessionID, turn1); err != nil {
		t.Fatalf("turn 1 should have pre/post checkpoints: %v", err)
	}
	turn2, _ := primitives.NewTurnID(2)
	if preTurn1 := checkpointPayloadForTurnPhase(t, events, turn1, primitives.CheckpointPhasePre); preTurn1.EventSeqStart != 1 || preTurn1.EventSeqEnd != 3 {
		t.Fatalf("turn 1 pre checkpoint event range = %d-%d, want 1-3", preTurn1.EventSeqStart, preTurn1.EventSeqEnd)
	}
	if postTurn1 := checkpointPayloadForTurnPhase(t, events, turn1, primitives.CheckpointPhasePost); postTurn1.EventSeqStart != 4 || postTurn1.EventSeqEnd != 6 {
		t.Fatalf("turn 1 post checkpoint event range = %d-%d, want 4-6", postTurn1.EventSeqStart, postTurn1.EventSeqEnd)
	}
	if preTurn2 := checkpointPayloadForTurnPhase(t, events, turn2, primitives.CheckpointPhasePre); preTurn2.EventSeqStart != 7 || preTurn2.EventSeqEnd != 8 {
		t.Fatalf("turn 2 pre checkpoint event range = %d-%d, want 7-8", preTurn2.EventSeqStart, preTurn2.EventSeqEnd)
	}
	active, ok, err := turns.NewManager(repo).Active(sessionID)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if !ok || active.TurnID.Uint64() != 2 {
		t.Fatalf("active = %#v ok=%v, want turn 2 active", active, ok)
	}
}

func handlePayload(t *testing.T, adapter primitives.AdapterName, hookName string, payload map[string]any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := HandleHookPayload(adapter, hookName, raw); err != nil {
		t.Fatalf("HandleHookPayload %s: %v", hookName, err)
	}
}

func readEvents(t *testing.T, repo *checkpoint.Repo, sessionID primitives.SessionID) []eventlog.Event {
	t.Helper()
	events, err := eventlog.Open(repo.MetadataDir).Read(sessionID)
	if err != nil {
		t.Fatalf("Read events: %v", err)
	}
	return events
}

func eventTypes(events []eventlog.Event) []primitives.EventType {
	types := make([]primitives.EventType, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}

func sameEventTypes(left, right []primitives.EventType) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func countEvents(events []eventlog.Event, eventType primitives.EventType) int {
	var count int
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func checkpointPayloadForPhase(t *testing.T, events []eventlog.Event, phase primitives.CheckpointPhase) checkpointEventPayload {
	t.Helper()
	for _, event := range events {
		if event.Type != primitives.EventTypeCheckpoint {
			continue
		}
		var payload checkpointEventPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("unmarshal checkpoint payload: %v", err)
		}
		if payload.Phase == phase.String() {
			return payload
		}
	}
	t.Fatalf("missing %s checkpoint event", phase)
	return checkpointEventPayload{}
}

func checkpointPayloadForTurnPhase(t *testing.T, events []eventlog.Event, turnID primitives.TurnID, phase primitives.CheckpointPhase) checkpointEventPayload {
	t.Helper()
	for _, event := range events {
		if event.Type != primitives.EventTypeCheckpoint {
			continue
		}
		var payload checkpointEventPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("unmarshal checkpoint payload: %v", err)
		}
		if payload.Turn == turnID.Uint64() && payload.Phase == phase.String() {
			return payload
		}
	}
	t.Fatalf("missing turn %s %s checkpoint event", turnID, phase)
	return checkpointEventPayload{}
}

func containsAll(value string, wants ...string) bool {
	for _, want := range wants {
		if !strings.Contains(value, want) {
			return false
		}
	}
	return true
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
