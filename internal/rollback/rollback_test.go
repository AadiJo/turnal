package rollback

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"agent-vcs-again/internal/checkpoint"
	eventlog "agent-vcs-again/internal/events"
	"agent-vcs-again/internal/primitives"
)

func TestRunFinalizesRestoredJournal(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	writeFile(t, root, "app.txt", "target\n")
	targetCheckpoint, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("target checkpoint: %v", err)
	}

	writeFile(t, root, "app.txt", "before rollback\n")
	targetRef, err := primitives.NewTargetRef(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("NewTargetRef: %v", err)
	}
	resolved, err := ResolveTarget(repo, targetRef)
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	safety, err := repo.CreateSnapshotRef("refs/agent-vcs/rollback-safety/demo/turn/000001/pre/test", "test safety")
	if err != nil {
		t.Fatalf("CreateSnapshotRef: %v", err)
	}
	if err := repo.RestoreCommit(targetCheckpoint.Commit); err != nil {
		t.Fatalf("RestoreCommit: %v", err)
	}

	sourceID := rollbackEventSourceID(resolved, safety)
	journal := Journal{
		Version:         1,
		State:           "restored",
		StartedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		Target:          targetRef.String(),
		CheckpointRef:   resolved.CheckpointRef.String(),
		TargetCommitSHA: resolved.Commit.String(),
		SafetyRef:       safety.Ref,
		SafetyCommitSHA: safety.Commit.String(),
		EventSourceID:   sourceID,
		Changes: []checkpoint.RestoreChange{{
			Path:   "app.txt",
			Action: checkpoint.RestoreActionModified,
		}},
	}
	if err := writeJournal(JournalPath(repo), journal); err != nil {
		t.Fatalf("writeJournal: %v", err)
	}

	result, err := New(repo).Run(Request{Target: targetRef, DryRun: true})
	if err != nil {
		t.Fatalf("Run with restored journal: %v", err)
	}
	if !result.DryRun {
		t.Fatal("Run result DryRun=false")
	}
	if _, err := os.Stat(JournalPath(repo)); !os.IsNotExist(err) {
		t.Fatalf("journal still exists or stat failed: %v", err)
	}
	if got := countEventsWithSourceID(t, repo, sessionID, sourceID); got != 1 {
		t.Fatalf("rollback events with source id = %d, want 1", got)
	}

	if err := writeJournal(JournalPath(repo), journal); err != nil {
		t.Fatalf("writeJournal second time: %v", err)
	}
	if _, err := New(repo).Run(Request{Target: targetRef, DryRun: true}); err != nil {
		t.Fatalf("Run with already-logged restored journal: %v", err)
	}
	if got := countEventsWithSourceID(t, repo, sessionID, sourceID); got != 1 {
		t.Fatalf("rollback events with source id after retry = %d, want 1", got)
	}

	journal.EventSourceID = "missing-source-id"
	if err := writeJournal(JournalPath(repo), journal); err != nil {
		t.Fatalf("writeJournal with missing source id: %v", err)
	}
	if _, err := New(repo).Run(Request{Target: targetRef, DryRun: true}); err != nil {
		t.Fatalf("Run with payload-matched restored journal: %v", err)
	}
	if got := countRollbackEvents(t, repo, sessionID); got != 1 {
		t.Fatalf("rollback events after payload-matched retry = %d, want 1", got)
	}
}

func TestRunRestoreFailureReturnsSafetyAndKeepsExtraFiles(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	writeFile(t, root, "conflict", "target\n")
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("target checkpoint: %v", err)
	}

	if err := os.Remove(filepath.Join(root.String(), "conflict")); err != nil {
		t.Fatalf("remove conflict file: %v", err)
	}
	writeFile(t, root, "conflict/.agent-vcs/keep", "metadata\n")
	writeFile(t, root, "extra.txt", "must remain after failed restore\n")

	targetRef, err := primitives.NewTargetRef(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("NewTargetRef: %v", err)
	}
	_, err = New(repo).Run(Request{Target: targetRef})
	if err == nil {
		t.Fatal("Run succeeded, want restore failure")
	}
	var safetyErr SafetyError
	if !errors.As(err, &safetyErr) {
		t.Fatalf("error = %T %v, want SafetyError", err, err)
	}
	if safetyErr.Safety.Ref == "" || safetyErr.Safety.Commit == "" || safetyErr.JournalPath != JournalPath(repo) {
		t.Fatalf("safety error missing recovery metadata: %#v", safetyErr)
	}
	if _, err := os.Stat(filepath.Join(root.String(), "extra.txt")); err != nil {
		t.Fatalf("extra.txt was deleted before restore failure: %v", err)
	}
	journal, ok, err := readJournal(JournalPath(repo))
	if err != nil {
		t.Fatalf("readJournal: %v", err)
	}
	if !ok {
		t.Fatal("journal missing after restore failure")
	}
	if journal.State != "restoring" {
		t.Fatalf("journal state = %q, want restoring", journal.State)
	}
	if journal.SafetyRef != safetyErr.Safety.Ref || journal.SafetyCommitSHA != safetyErr.Safety.Commit.String() {
		t.Fatalf("journal safety = %s %s, want %s %s", journal.SafetyRef, journal.SafetyCommitSHA, safetyErr.Safety.Ref, safetyErr.Safety.Commit)
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

func countEventsWithSourceID(t *testing.T, repo *checkpoint.Repo, sessionID primitives.SessionID, sourceID string) int {
	t.Helper()
	events, err := eventlog.Open(repo.MetadataDir).Read(sessionID)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	count := 0
	for _, event := range events {
		if event.SourceID == sourceID {
			count++
		}
	}
	return count
}

func countRollbackEvents(t *testing.T, repo *checkpoint.Repo, sessionID primitives.SessionID) int {
	t.Helper()
	events, err := eventlog.Open(repo.MetadataDir).Read(sessionID)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	count := 0
	for _, event := range events {
		if event.Type == primitives.EventTypeRollback {
			count++
		}
	}
	return count
}
