package retention

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/adapters"
	caseengine "github.com/AadiJo/turnal/internal/cases"
	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	experimentengine "github.com/AadiJo/turnal/internal/experiments"
	"github.com/AadiJo/turnal/internal/primitives"
	rollbackengine "github.com/AadiJo/turnal/internal/rollback"
	"github.com/AadiJo/turnal/internal/turnevents"
	"github.com/AadiJo/turnal/internal/turns"
)

type retentionRunner func(context.Context, string, []string, []string) (int, error)

func (runner retentionRunner) Run(ctx context.Context, root string, command, environment []string) (int, error) {
	return runner(ctx, root, command, environment)
}

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
	if len(result.DeletedRefs) < 2 || len(result.DeletedFiles) < 2 {
		t.Fatalf("drop result = %#v, want friendly and canonical refs plus stream data and metadata", result)
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

func TestDropSessionRejectsInconsistentRollbackJournal(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	demoSession := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	writeFile(t, root, "app.txt", "content\n")
	created, err := repo.CreateCheckpoint(demoSession, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	appendCheckpointEvent(t, repo, demoSession, turnID, created)
	safety, err := repo.CreateSnapshotRef("refs/agent-vcs/rollback-safety/retention-test", "retention test safety")
	if err != nil {
		t.Fatalf("CreateSnapshotRef: %v", err)
	}
	target, _ := primitives.NewTargetRef(demoSession, turnID, primitives.CheckpointPhasePre)
	base := rollbackengine.Journal{
		Version: 1, State: "planned", RestorePhase: "planned",
		Target: target.String(), CheckpointRef: created.Ref.String(), TargetCommitSHA: created.Commit.String(),
		Mode: primitives.RollbackModeCheckpoint.String(), SafetyRef: safety.Ref, SafetyCommitSHA: safety.Commit.String(),
		RepoID: repo.RepoID, StoreID: repo.StoreID, WorktreeID: repo.WorktreeID, WorkspaceRoot: repo.WorkspaceRoot.String(),
	}

	tests := []struct {
		name     string
		expected string
		mutate   func(*rollbackengine.Journal)
	}{
		{
			name:     "manual flag cannot hide a session checkpoint",
			expected: "rollback journal is invalid",
			mutate: func(journal *rollbackengine.Journal) {
				journal.Manual = true
			},
		},
		{
			name:     "target must identify the checkpoint ref",
			expected: "rollback journal is invalid",
			mutate: func(journal *rollbackengine.Journal) {
				otherSession := sessionID(t, "other")
				otherTarget, _ := primitives.NewTargetRef(otherSession, turnID, primitives.CheckpointPhasePre)
				journal.Target = otherTarget.String()
			},
		},
		{
			name:     "workspace root must match the registered worktree",
			expected: "rollback journal ownership is invalid",
			mutate: func(journal *rollbackengine.Journal) {
				journal.WorkspaceRoot = t.TempDir()
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			journal := base
			test.mutate(&journal)
			data, marshalErr := json.Marshal(journal)
			if marshalErr != nil {
				t.Fatalf("Marshal journal: %v", marshalErr)
			}
			if writeErr := os.WriteFile(rollbackengine.JournalPath(repo), data, 0o600); writeErr != nil {
				t.Fatalf("write rollback journal: %v", writeErr)
			}
			if _, dropErr := DropSession(repo, demoSession, false); dropErr == nil || !strings.Contains(dropErr.Error(), test.expected) {
				t.Fatalf("DropSession error = %v, want %q", dropErr, test.expected)
			}
			if _, refErr := repo.CheckpointCommit(created.Ref); refErr != nil {
				t.Fatalf("protected checkpoint was deleted: %v", refErr)
			}
		})
	}
}

func TestDropSessionRedactsRawPayloadAndInvalidatesIndex(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sessionID := sessionID(t, "demo")
	adapterDir := filepath.Join(repo.MetadataDir, "log", "adapter")
	if err := os.MkdirAll(adapterDir, 0o700); err != nil {
		t.Fatalf("mkdir adapter log: %v", err)
	}
	raw := `{"v":1,"adapter":"codex","hook":"turn","received_at":"2026-01-01T00:00:00Z","payload":{"session_id":"demo","secret":"remove-me"}}` + "\n"
	adapterPath := filepath.Join(adapterDir, "codex.jsonl")
	if err := os.WriteFile(adapterPath, []byte(raw), 0o600); err != nil {
		t.Fatalf("write raw log: %v", err)
	}
	v2Dir := filepath.Join(repo.MetadataDir, "log", "raw", "demo")
	if err := os.MkdirAll(v2Dir, 0o700); err != nil {
		t.Fatalf("mkdir v2 raw log: %v", err)
	}
	v2Path := filepath.Join(v2Dir, "codex.jsonl")
	if err := os.WriteFile(v2Path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write v2 raw log: %v", err)
	}
	indexDir := filepath.Join(repo.MetadataDir, "index")
	if err := os.MkdirAll(indexDir, 0o700); err != nil {
		t.Fatalf("mkdir index: %v", err)
	}
	indexPath := filepath.Join(indexDir, "index.sqlite")
	if err := os.WriteFile(indexPath, []byte("stale search data"), 0o600); err != nil {
		t.Fatalf("write stale index: %v", err)
	}

	result, err := DropSession(repo, sessionID, false)
	if err != nil {
		t.Fatalf("DropSession: %v", err)
	}
	if len(result.RedactedFiles) != 1 {
		t.Fatalf("redacted files = %#v, want adapter log", result.RedactedFiles)
	}
	data, err := os.ReadFile(adapterPath)
	if err != nil {
		t.Fatalf("read adapter log: %v", err)
	}
	if bytes.Contains(data, []byte("remove-me")) || bytes.Contains(data, []byte(`"session_id":"demo"`)) {
		t.Fatalf("deleted session remains in adapter log: %s", data)
	}
	if _, err := os.Stat(indexPath); !os.IsNotExist(err) {
		t.Fatalf("index stat error = %v, want removed", err)
	}
	if _, err := os.Stat(v2Dir); !os.IsNotExist(err) {
		t.Fatalf("v2 session raw dir stat error = %v, want removed", err)
	}
}

func TestDropSessionRefusesCaseSourceAndAttemptExecutionHistory(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "app.txt", "before\n")
	source := sessionID(t, "case-source")
	turnID, _ := primitives.NewTurnID(1)
	if _, err := repo.EventLog().Append(eventlog.AppendInput{SessionID: source, Type: primitives.EventTypeSessionStart, Adapter: primitives.AdapterCodex, Payload: json.RawMessage(`{"provider_session_id":"source"}`)}); err != nil {
		t.Fatal(err)
	}
	gitSync := false
	recorder := turnevents.Recorder{Log: repo.EventLog(), Manager: turns.NewManager(repo), Adapter: primitives.AdapterCodex}
	recorder.Manager.GitSyncEnabled = &gitSync
	if _, err := recorder.Start(source, turnID); err != nil {
		t.Fatal(err)
	}
	prompt, _ := json.Marshal(map[string]string{"text": "Fix it"})
	if _, err := repo.EventLog().Append(eventlog.AppendInput{SessionID: source, TurnID: &turnID, Type: primitives.EventTypePromptUser, Adapter: primitives.AdapterCodex, Payload: prompt}); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.Finish(source, turnID); err != nil {
		t.Fatal(err)
	}
	created, err := caseengine.Create(repo, caseengine.CreateRequest{SessionID: source, TurnID: turnID})
	if err != nil {
		t.Fatal(err)
	}
	result, err := experimentengine.Execute(context.Background(), repo, experimentengine.Request{Case: created.Case, Command: []string{"runner"}, Runner: retentionRunner(func(context.Context, string, []string, []string) (int, error) { return 0, nil })})
	if err != nil {
		t.Fatal(err)
	}
	for _, protected := range []primitives.SessionID{source, result.SessionID} {
		if _, err := DropSession(repo, protected, false); err == nil || !strings.Contains(err.Error(), "cannot drop session") || !strings.Contains(err.Error(), created.Case.ID.String()) {
			t.Fatalf("drop protected session %s error = %v", protected, err)
		}
	}
	if _, err := repo.CheckpointCommit(created.Case.Readiness.Base.Ref); err != nil {
		t.Fatalf("protected base ref was removed: %v", err)
	}
	if _, err := repo.CheckpointCommit(result.PostRef); err != nil {
		t.Fatalf("protected attempt ref was removed: %v", err)
	}
	if _, err := caseengine.Delete(repo, created.Case.ID); err != nil {
		t.Fatalf("Delete case: %v", err)
	}
	if _, err := DropSession(repo, result.SessionID, false); err != nil {
		t.Fatalf("drop attempt session after Case deletion: %v", err)
	}
	if _, err := DropSession(repo, source, false); err != nil {
		t.Fatalf("drop source session after Case deletion: %v", err)
	}
	projection, err := caseengine.Rebuild(repo)
	if err != nil {
		t.Fatalf("Rebuild after linked session deletion: %v", err)
	}
	if len(projection.Cases) != 0 {
		t.Fatalf("deleted Case remains after dropping linked sessions: %#v", projection.Cases)
	}
}

func TestDropSessionSerializesWithHookCapture(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sessionID := sessionID(t, "demo")
	rawDir := filepath.Join(repo.MetadataDir, "log", "raw", sessionID.String())
	if err := os.MkdirAll(rawDir, 0o700); err != nil {
		t.Fatalf("mkdir raw dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rawDir, "codex.jsonl"), []byte("payload\n"), 0o600); err != nil {
		t.Fatalf("write raw record: %v", err)
	}
	release, err := adapters.AcquireSessionLock(repo, sessionID)
	if err != nil {
		t.Fatalf("AcquireSessionLock: %v", err)
	}
	result := make(chan error, 1)
	go func() {
		_, dropErr := DropSession(repo, sessionID, false)
		result <- dropErr
	}()
	select {
	case err := <-result:
		release()
		t.Fatalf("DropSession completed while capture lock held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("DropSession after release: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("DropSession did not resume after capture lock release")
	}
	if _, err := os.Stat(rawDir); !os.IsNotExist(err) {
		t.Fatalf("raw session directory survived serialized drop: %v", err)
	}
}

func TestDropSessionRemovesManagedReplayState(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sessionsDir := filepath.Join(repo.TmpDir, "replay", "sessions")
	worktree := filepath.Join(repo.TmpDir, "replay", "worktrees", "replay-one")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir replay sessions: %v", err)
	}
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatalf("mkdir replay worktree: %v", err)
	}
	metadata := fmt.Sprintf(`{"id":"replay-one","path":%q,"sequence":[{"session_id":"demo"}]}`, worktree)
	metadataPath := filepath.Join(sessionsDir, "replay-one.json")
	if err := os.WriteFile(metadataPath, []byte(metadata), 0o600); err != nil {
		t.Fatalf("write replay metadata: %v", err)
	}
	activePath := filepath.Join(repo.TmpDir, "replay", "active")
	if err := os.WriteFile(activePath, []byte("replay-one\n"), 0o600); err != nil {
		t.Fatalf("write active replay: %v", err)
	}

	demo := sessionID(t, "demo")
	if _, err := DropSession(repo, demo, false); err != nil {
		t.Fatalf("DropSession: %v", err)
	}
	for _, path := range []string{metadataPath, activePath, worktree} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("replay path %s remains: %v", path, err)
		}
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
	manual, err := repo.CreateManualCheckpoint()
	if err != nil {
		t.Fatalf("CreateManualCheckpoint: %v", err)
	}
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
	if _, err := repo.CheckpointCommit(manual.Ref); err != nil {
		t.Fatalf("manual checkpoint ref was pruned: %v", err)
	}
	if _, err := repo.CheckpointCommit(manual.CanonicalRef); err != nil {
		t.Fatalf("manual canonical checkpoint ref was pruned: %v", err)
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
