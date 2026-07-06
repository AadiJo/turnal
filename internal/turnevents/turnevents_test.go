package turnevents

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"agent-vcs-again/internal/checkpoint"
	eventlog "agent-vcs-again/internal/events"
	"agent-vcs-again/internal/primitives"
	"agent-vcs-again/internal/turns"
)

func TestRecoverCheckpointJournalsAppendsMissingCheckpointEvent(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	writeFile(t, root, "app.txt", "before\n")

	sessionID := sessionID(t, "demo")
	manager := turns.NewManager(repo).WithCheckpointEvents(primitives.AdapterManual, "")
	started, err := manager.Start(sessionID, 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, ok, err := repo.ReadCheckpointJournal(sessionID, started.TurnID, primitives.CheckpointPhasePre); err != nil || !ok {
		t.Fatalf("ReadCheckpointJournal ok=%t err=%v, want committed journal", ok, err)
	}

	log := eventlog.Open(repo.MetadataDir)
	if err := RecoverCheckpointJournals(log, repo); err != nil {
		t.Fatalf("RecoverCheckpointJournals: %v", err)
	}
	if _, ok, err := repo.ReadCheckpointJournal(sessionID, started.TurnID, primitives.CheckpointPhasePre); err != nil || ok {
		t.Fatalf("journal ok=%t err=%v, want cleared", ok, err)
	}

	events, err := log.Read(sessionID)
	if err != nil {
		t.Fatalf("Read events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want turn.start and checkpoint: %#v", len(events), events)
	}
	if events[0].Type != primitives.EventTypeTurnStart || events[1].Type != primitives.EventTypeCheckpoint {
		t.Fatalf("event types = %s, %s", events[0].Type, events[1].Type)
	}
	var payload checkpointPayload
	if err := json.Unmarshal(events[1].Payload, &payload); err != nil {
		t.Fatalf("unmarshal checkpoint payload: %v", err)
	}
	if payload.Ref != started.Pre.Ref.String() || payload.CommitSHA != started.Pre.Commit.String() {
		t.Fatalf("payload checkpoint = %s %s, want %s %s", payload.CommitSHA, payload.Ref, started.Pre.Commit, started.Pre.Ref)
	}

	if err := RecoverCheckpointJournals(log, repo); err != nil {
		t.Fatalf("second RecoverCheckpointJournals: %v", err)
	}
	events, err = log.Read(sessionID)
	if err != nil {
		t.Fatalf("Read events after second recovery: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len after second recovery = %d, want 2", len(events))
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
