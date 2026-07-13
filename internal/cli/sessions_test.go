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
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: demoSession,
		TurnID:    &turnID,
		Type:      primitives.EventTypeRollback,
		Time:      testTimestamp(t, time.Date(2027, 7, 6, 12, 3, 0, 0, time.UTC)),
		RawRef:    "demo:turn:1:pre",
		Payload: json.RawMessage(`{
			"turn":1,"phase":"pre","mode":"checkpoint","target":"demo:turn:1:pre",
			"change_summary":{"total":2,"added":0,"modified":1,"deleted":1,"mode_changed":0}
		}`),
	}); err != nil {
		t.Fatalf("append rollback event: %v", err)
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
		"events   4",
		"rollbacks 1",
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
	if demo.TurnCount != 1 || demo.CompleteTurnCount != 1 || demo.EventCount != 4 {
		t.Fatalf("demo counts = %#v", demo)
	}
	if demo.Head == nil || demo.Head.TurnID != 1 || demo.Head.Phase != "post" {
		t.Fatalf("demo head = %#v", demo.Head)
	}
	if demo.LatestTurn == nil || demo.LatestTurn.Prompt != "change app.txt" || len(demo.LatestTurn.ToolNames) != 1 || demo.LatestTurn.ToolNames[0] != "apply_patch" {
		t.Fatalf("demo latest turn = %#v", demo.LatestTurn)
	}
	if len(demo.Turns) != 1 || demo.Turns[0].TurnID != 1 || demo.Turns[0].Status != "complete" {
		t.Fatalf("demo turns = %#v", demo.Turns)
	}
	if demo.Turns[0].FirstActivity == "" || demo.Turns[0].LastActivity == "" {
		t.Fatalf("demo turn activity = %#v", demo.Turns[0])
	}
	if demo.Turns[0].LastActivity == "2027-07-06T12:03:00Z" {
		t.Fatalf("rollback changed turn activity = %#v", demo.Turns[0])
	}
	if demo.RollbackCount != 1 || len(demo.Rollbacks) != 1 {
		t.Fatalf("demo rollbacks = %#v", demo.Rollbacks)
	}
	rollback := demo.Rollbacks[0]
	if rollback.TurnID != 1 || rollback.Target != "demo:turn:1:pre" || rollback.Phase != "pre" || rollback.Mode != "checkpoint" || rollback.ChangeSummary.Total != 2 {
		t.Fatalf("demo rollback = %#v", rollback)
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

func TestSessionsJSONOrdersTurnsByLatestActivity(t *testing.T) {
	firstTurn, _ := primitives.NewTurnID(1)
	secondTurn, _ := primitives.NewTurnID(2)
	firstAt := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	secondAt := firstAt.Add(time.Minute)
	output := sessionsJSONFromViews([]sessionView{
		{
			ID: sessionID(t, "ordered"),
			Turns: map[uint64]*sessionViewTurn{
				firstTurn.Uint64(): {
					TurnID: firstTurn,
					Events: turnEventSummary{Count: 1, First: firstAt, Last: firstAt, Prompt: "first"},
				},
				secondTurn.Uint64(): {
					TurnID: secondTurn,
					Events: turnEventSummary{Count: 1, First: secondAt, Last: secondAt, Prompt: "second"},
				},
			},
		},
	})

	if len(output.Sessions) != 1 || len(output.Sessions[0].Turns) != 2 {
		t.Fatalf("sessions JSON = %#v", output)
	}
	turns := output.Sessions[0].Turns
	if turns[0].TurnID != 2 || turns[1].TurnID != 1 {
		t.Fatalf("turn order = %#v", turns)
	}
	if output.Sessions[0].LatestTurn == nil || output.Sessions[0].LatestTurn.TurnID != 2 {
		t.Fatalf("latest turn = %#v", output.Sessions[0].LatestTurn)
	}
}
