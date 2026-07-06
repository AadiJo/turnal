package retention

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"agent-vcs-again/internal/checkpoint"
	eventlog "agent-vcs-again/internal/events"
	"agent-vcs-again/internal/primitives"
)

func TestDropSessionDeletesEventLogAndPrivateRefs(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	writeFile(t, root, "app.txt", "content\n")
	created, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	appendCheckpointEvent(t, repo, sessionID, turnID, created)

	result, err := DropSession(repo, sessionID, false)
	if err != nil {
		t.Fatalf("DropSession: %v", err)
	}
	if len(result.DeletedRefs) != 1 || len(result.DeletedFiles) != 1 {
		t.Fatalf("drop result = %#v, want one ref and one event log", result)
	}
	refs, err := repo.ListCheckpointRefs(sessionID)
	if err != nil {
		t.Fatalf("ListCheckpointRefs: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("checkpoint refs after drop = %#v, want none", refs)
	}
	sessions, err := eventlog.Open(repo.MetadataDir).ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions after drop = %#v, want none", sessions)
	}
}

func TestPruneOrphanRefsKeepsEventReferencedRefs(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	writeFile(t, root, "app.txt", "content\n")
	created, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	appendCheckpointEvent(t, repo, sessionID, turnID, created)
	orphan, err := repo.CreateSnapshotRef("refs/agent-vcs/rollback-safety/demo/turn/000001/pre/orphan", "orphan")
	if err != nil {
		t.Fatalf("CreateSnapshotRef: %v", err)
	}

	dryRun, err := PruneOrphanRefs(repo, true)
	if err != nil {
		t.Fatalf("PruneOrphanRefs dry run: %v", err)
	}
	if len(dryRun.DeletedRefs) != 1 || dryRun.DeletedRefs[0] != orphan.Ref {
		t.Fatalf("dry-run refs = %#v, want orphan %s", dryRun.DeletedRefs, orphan.Ref)
	}
	if _, err := repo.RefCommit(orphan.Ref); err != nil {
		t.Fatalf("dry-run deleted orphan ref: %v", err)
	}

	result, err := PruneOrphanRefs(repo, false)
	if err != nil {
		t.Fatalf("PruneOrphanRefs: %v", err)
	}
	if len(result.DeletedRefs) != 1 || result.DeletedRefs[0] != orphan.Ref {
		t.Fatalf("pruned refs = %#v, want orphan %s", result.DeletedRefs, orphan.Ref)
	}
	if _, err := repo.RefCommit(orphan.Ref); err == nil {
		t.Fatal("orphan ref still resolves after prune")
	}
	if _, err := repo.CheckpointCommit(created.Ref); err != nil {
		t.Fatalf("event-referenced checkpoint ref was pruned: %v", err)
	}
}

func appendCheckpointEvent(t *testing.T, repo *checkpoint.Repo, sessionID primitives.SessionID, turnID primitives.TurnID, created checkpoint.Checkpoint) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"turn":       turnID.Uint64(),
		"phase":      primitives.CheckpointPhasePre.String(),
		"commit_sha": created.Commit.String(),
		"ref":        created.Ref.String(),
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := eventlog.Open(repo.MetadataDir).Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypeCheckpoint,
		Payload:   payload,
	}); err != nil {
		t.Fatalf("Append checkpoint event: %v", err)
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
