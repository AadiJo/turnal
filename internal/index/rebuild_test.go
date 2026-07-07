package index

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"agent-vcs-again/internal/checkpoint"
	eventlog "agent-vcs-again/internal/events"
	"agent-vcs-again/internal/primitives"
)

func TestRebuildPopulatesGraphRows(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)

	writeFile(t, root, "app.txt", "before\n")
	writeFile(t, root, "notes.txt", "old\n")
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("pre checkpoint: %v", err)
	}
	writeFile(t, root, "app.txt", "after\n")
	writeFile(t, root, "notes.txt", "old\nnew\n")
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost); err != nil {
		t.Fatalf("post checkpoint: %v", err)
	}

	appendEvent(t, repo.MetadataDir, eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypePromptUser,
		Adapter:   primitives.AdapterCodex,
		Time:      timestamp(t, time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)),
		Payload:   json.RawMessage(`{"text":"change files"}`),
	})
	appendEvent(t, repo.MetadataDir, eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypeToolCall,
		Adapter:   primitives.AdapterCodex,
		Time:      timestamp(t, time.Date(2026, 7, 6, 12, 1, 0, 0, time.UTC)),
		Payload:   json.RawMessage(`{"tool_name":"apply_patch"}`),
	})
	appendEvent(t, repo.MetadataDir, eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypeAssistantMessage,
		Adapter:   primitives.AdapterCodex,
		Time:      timestamp(t, time.Date(2026, 7, 6, 12, 2, 0, 0, time.UTC)),
		Payload:   json.RawMessage(`{"text":"done"}`),
	})

	stats, err := Rebuild(repo)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if stats.Sessions != 1 || stats.Turns != 1 || stats.Events != 3 || stats.Checkpoints != 2 || stats.FileTouches != 2 || stats.SearchDocuments != 1 {
		t.Fatalf("stats = %#v, want 1 session, 1 turn, 3 events, 2 checkpoints, 2 file touches, 1 search doc", stats)
	}

	store, err := Open(repo.MetadataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	healthy, err := store.Healthy()
	if err != nil {
		t.Fatalf("Healthy: %v", err)
	}
	if !healthy {
		t.Fatal("index is not healthy after rebuild")
	}

	var eventRows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&eventRows); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if eventRows != 3 {
		t.Fatalf("event rows = %d, want 3", eventRows)
	}

	sessions, err := store.LoadGraph(GraphQuery{})
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	if len(sessions) != 1 || len(sessions[0].Turns) != 1 {
		t.Fatalf("graph sessions = %#v, want one session with one turn", sessions)
	}
	turn := sessions[0].Turns[0]
	if turn.Pre == nil || turn.Post == nil || !turn.DiffLoaded {
		t.Fatalf("turn checkpoints/diff not loaded: %#v", turn)
	}
	if len(turn.Diff.Files) != 2 || turn.Diff.Additions != 2 || turn.Diff.Deletions != 1 {
		t.Fatalf("diff = %#v, want two files +2 -1", turn.Diff)
	}
	if turn.Events.Count != 3 || turn.Events.Adapter != "codex" || turn.Events.Prompt != "change files" || turn.Events.Assistant != "done" {
		t.Fatalf("event summary = %#v", turn.Events)
	}
	if len(turn.Events.ToolNames) != 1 || turn.Events.ToolNames[0] != "apply_patch" {
		t.Fatalf("tool names = %#v, want apply_patch", turn.Events.ToolNames)
	}
	if turn.Events.TypeCounts[primitives.EventTypePromptUser] != 1 || turn.Events.TypeCounts[primitives.EventTypeToolCall] != 1 {
		t.Fatalf("type counts = %#v", turn.Events.TypeCounts)
	}
}

func TestRebuildIncludesEventOnlySessionsButGraphIsRefDriven(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	refSession := sessionID(t, "refs")
	eventSession := sessionID(t, "events")
	turnID, _ := primitives.NewTurnID(1)

	writeFile(t, root, "app.txt", "before\n")
	if _, err := repo.CreateCheckpoint(refSession, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("pre checkpoint: %v", err)
	}
	appendEvent(t, repo.MetadataDir, eventlog.AppendInput{
		SessionID: eventSession,
		TurnID:    &turnID,
		Type:      primitives.EventTypePromptUser,
		Adapter:   primitives.AdapterCodex,
		Payload:   json.RawMessage(`{"text":"event only"}`),
	})

	if _, err := Rebuild(repo); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	store, err := Open(repo.MetadataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	var sessionRows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessionRows); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionRows != 2 {
		t.Fatalf("session rows = %d, want refs + events", sessionRows)
	}
	sessions, err := store.LoadGraph(GraphQuery{})
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != refSession {
		t.Fatalf("graph sessions = %#v, want only ref-driven session %s", sessions, refSession)
	}
}

func TestRebuildFailureLeavesPreviousIndex(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	writeFile(t, root, "app.txt", "before\n")
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("pre checkpoint: %v", err)
	}
	appendEvent(t, repo.MetadataDir, eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypePromptUser,
		Adapter:   primitives.AdapterCodex,
		Payload:   json.RawMessage(`{"text":"valid"}`),
	})
	if _, err := Rebuild(repo); err != nil {
		t.Fatalf("initial Rebuild: %v", err)
	}

	logPath := filepath.Join(eventlog.Open(repo.MetadataDir).Dir, sessionID.String()+".jsonl")
	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	if _, err := file.WriteString("not-json\n"); err != nil {
		_ = file.Close()
		t.Fatalf("corrupt event log: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close event log: %v", err)
	}

	if _, err := Rebuild(repo); err == nil {
		t.Fatal("Rebuild succeeded with corrupt event log, want error")
	}
	store, err := Open(repo.MetadataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	healthy, err := store.Healthy()
	if err != nil {
		t.Fatalf("Healthy: %v", err)
	}
	if !healthy {
		t.Fatal("previous index is not healthy after failed rebuild")
	}
	sessions, err := store.LoadGraph(GraphQuery{})
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	if len(sessions) != 1 || len(sessions[0].Turns) != 1 {
		t.Fatalf("previous graph = %#v, want one preserved turn", sessions)
	}
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

func appendEvent(t *testing.T, metadataDir string, input eventlog.AppendInput) {
	t.Helper()
	if _, err := eventlog.Open(metadataDir).Append(input); err != nil {
		t.Fatalf("Append: %v", err)
	}
}

func timestamp(t *testing.T, value time.Time) primitives.Timestamp {
	t.Helper()
	timestamp, err := primitives.NewTimestamp(value)
	if err != nil {
		t.Fatalf("NewTimestamp: %v", err)
	}
	return timestamp
}
