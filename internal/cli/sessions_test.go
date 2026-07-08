package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
)

func TestSessionsCommandShowsDurableSessionInventory(t *testing.T) {
	root, repo, demoSession, turnID := createTurnWithDiff(t)
	t.Chdir(root.String())

	log := eventlog.Open(repo.MetadataDir)
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: demoSession,
		Type:      primitives.EventTypeSessionStart,
		Adapter:   primitives.AdapterCodex,
		Time:      testTimestamp(t, time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)),
		Payload:   json.RawMessage(`{"provider_session_id":"demo","model":"gpt-5.5","permission_mode":"default"}`),
	}); err != nil {
		t.Fatalf("append session event: %v", err)
	}
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: demoSession,
		TurnID:    &turnID,
		Type:      primitives.EventTypePromptUser,
		Adapter:   primitives.AdapterCodex,
		Time:      testTimestamp(t, time.Date(2026, 7, 6, 12, 1, 0, 0, time.UTC)),
		Payload:   json.RawMessage(`{"text":"change app.txt"}`),
	}); err != nil {
		t.Fatalf("append prompt event: %v", err)
	}
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: demoSession,
		TurnID:    &turnID,
		Type:      primitives.EventTypeToolCall,
		Adapter:   primitives.AdapterCodex,
		Time:      testTimestamp(t, time.Date(2026, 7, 6, 12, 2, 0, 0, time.UTC)),
		Payload:   json.RawMessage(`{"tool_name":"apply_patch"}`),
	}); err != nil {
		t.Fatalf("append tool event: %v", err)
	}

	activeSession := sessionID(t, "active-session")
	if _, err := repo.CreateCheckpoint(activeSession, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("active pre checkpoint: %v", err)
	}
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: activeSession,
		Type:      primitives.EventTypeSessionStart,
		Adapter:   primitives.AdapterClaudeCode,
		Time:      testTimestamp(t, time.Date(2026, 7, 6, 13, 0, 0, 0, time.UTC)),
		Payload:   json.RawMessage(`{"provider_session_id":"active-session"}`),
	}); err != nil {
		t.Fatalf("append active session event: %v", err)
	}

	output := stripANSI(runRootStdout(t, "sessions"))
	for _, want := range []string{
		"sessions 2 recorded",
		"[COMPLETE] demo",
		"adapter  codex / gpt-5.5 / default",
		"turns    1 total, 1 complete",
		"events   3",
		"head     turn 1 post",
		`prompt   "change app.txt"`,
		"tools    apply_patch",
		"[ACTIVE] active-session",
		"head     turn 1 pre",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("sessions output missing %q:\n%s", want, output)
		}
	}

	jsonOutput := runRootStdout(t, "sessions", "--json")
	var got sessionsJSONOutput
	if err := json.Unmarshal([]byte(jsonOutput), &got); err != nil {
		t.Fatalf("unmarshal sessions JSON: %v\n%s", err, jsonOutput)
	}
	if got.TotalSessions != 2 {
		t.Fatalf("total sessions = %d, want 2", got.TotalSessions)
	}
	demo := findSessionSummary(t, got, "demo")
	if demo.Status != "complete" || demo.Adapter != "codex" || demo.Model != "gpt-5.5" || demo.PermissionMode != "default" {
		t.Fatalf("demo summary = %#v", demo)
	}
	if demo.TurnCount != 1 || demo.CompleteTurnCount != 1 || demo.EventCount != 3 {
		t.Fatalf("demo counts = %#v", demo)
	}
	if demo.Head == nil || demo.Head.TurnID != 1 || demo.Head.Phase != "post" {
		t.Fatalf("demo head = %#v", demo.Head)
	}
	if demo.LatestTurn == nil || demo.LatestTurn.Prompt != "change app.txt" || len(demo.LatestTurn.ToolNames) != 1 || demo.LatestTurn.ToolNames[0] != "apply_patch" {
		t.Fatalf("demo latest turn = %#v", demo.LatestTurn)
	}

	active := findSessionSummary(t, got, "active-session")
	if active.Status != "active" || active.ActiveTurnCount != 1 {
		t.Fatalf("active summary = %#v", active)
	}
}

func findSessionSummary(t *testing.T, output sessionsJSONOutput, sessionID string) sessionJSONSummary {
	t.Helper()
	for _, session := range output.Sessions {
		if session.SessionID == sessionID {
			return session
		}
	}
	t.Fatalf("session %q not found in %#v", sessionID, output.Sessions)
	return sessionJSONSummary{}
}
