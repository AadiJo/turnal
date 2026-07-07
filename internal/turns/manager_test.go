package turns

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable not found")
	}
}

func TestStartFinishCreatesTurnDiffAndAdvancesTurn(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	manager := NewManager(repo)
	sessionID := sessionID(t, "demo")

	writeFile(t, root, "app.txt", "before\n")
	started, err := manager.Start(sessionID, 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if started.TurnID.Uint64() != 1 {
		t.Fatalf("started turn = %s, want 1", started.TurnID)
	}

	writeFile(t, root, "app.txt", "after\n")
	finished, err := manager.Finish(sessionID, 0)
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.TurnID != started.TurnID {
		t.Fatalf("finished turn = %s, want %s", finished.TurnID, started.TurnID)
	}

	diff, err := repo.DiffTurn(sessionID, started.TurnID)
	if err != nil {
		t.Fatalf("DiffTurn: %v", err)
	}
	diffText := string(diff)
	for _, want := range []string{"diff --git a/app.txt b/app.txt", "-before", "+after"} {
		if !strings.Contains(diffText, want) {
			t.Fatalf("diff missing %q:\n%s", want, diffText)
		}
	}
	if strings.Contains(diffText, ".turnal") {
		t.Fatalf("turn diff includes turnal metadata:\n%s", diffText)
	}

	next, err := manager.Start(sessionID, 0)
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if next.TurnID.Uint64() != 2 {
		t.Fatalf("next turn = %s, want 2", next.TurnID)
	}
}

func TestStartFailsWhenTurnAlreadyActive(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	manager := NewManager(repo)
	sessionID := sessionID(t, "demo")

	if _, err := manager.Start(sessionID, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := manager.Start(sessionID, 0); err == nil {
		t.Fatal("second Start succeeded, want active turn error")
	}
}

func TestFinishFailsWithoutActiveTurn(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	manager := NewManager(repo)
	sessionID := sessionID(t, "demo")

	_, err = manager.Finish(sessionID, 0)
	if err == nil {
		t.Fatal("Finish succeeded without active turn")
	}
	if !strings.Contains(err.Error(), "no active turn") {
		t.Fatalf("Finish error = %v, want no active turn", err)
	}
}

func TestFinishWithExplicitTurnRecoversWithoutActiveState(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	manager := NewManager(repo)
	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)

	writeFile(t, root, "app.txt", "before\n")
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("pre checkpoint: %v", err)
	}

	writeFile(t, root, "app.txt", "after\n")
	finished, err := manager.Finish(sessionID, turnID)
	if err != nil {
		t.Fatalf("Finish explicit turn: %v", err)
	}
	if finished.TurnID != turnID {
		t.Fatalf("finished turn = %s, want %s", finished.TurnID, turnID)
	}
}

func TestFinishReportsMalformedActiveState(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	manager := NewManager(repo)
	sessionID := sessionID(t, "demo")

	if err := os.MkdirAll(filepath.Dir(manager.activeStatePath(sessionID)), 0o755); err != nil {
		t.Fatalf("mkdir active state dir: %v", err)
	}
	if err := os.WriteFile(manager.activeStatePath(sessionID), []byte("{"), 0o644); err != nil {
		t.Fatalf("write malformed active state: %v", err)
	}

	_, err = manager.Finish(sessionID, 0)
	if err == nil {
		t.Fatal("Finish succeeded with malformed active state")
	}
	if !strings.Contains(err.Error(), "active turn state invariant failed") {
		t.Fatalf("Finish error = %v, want active turn state invariant", err)
	}
}

func TestStartFailsWhenExplicitTurnHasCheckpointRefs(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	manager := NewManager(repo)
	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)

	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("pre checkpoint: %v", err)
	}

	_, err = manager.Start(sessionID, turnID)
	if err == nil {
		t.Fatal("Start succeeded for turn with existing checkpoint refs")
	}
	if !strings.Contains(err.Error(), "already has checkpoint refs") {
		t.Fatalf("Start error = %v, want checkpoint refs error", err)
	}
}

func TestFinishFailsWhenPostCheckpointAlreadyExists(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	manager := NewManager(repo)
	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)

	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("pre checkpoint: %v", err)
	}
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost); err != nil {
		t.Fatalf("post checkpoint: %v", err)
	}

	_, err = manager.Finish(sessionID, turnID)
	if err == nil {
		t.Fatal("Finish succeeded with existing post checkpoint")
	}
	if !strings.Contains(err.Error(), "post checkpoint already exists") {
		t.Fatalf("Finish error = %v, want post checkpoint exists", err)
	}
}

func TestFinishFailsOnActiveTurnMismatch(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	manager := NewManager(repo)
	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(2)

	if _, err := manager.Start(sessionID, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	_, err = manager.Finish(sessionID, turnID)
	if err == nil {
		t.Fatal("Finish succeeded with mismatched active turn")
	}
	if !strings.Contains(err.Error(), "active turn mismatch") {
		t.Fatalf("Finish error = %v, want active turn mismatch", err)
	}
}

func TestStartCollisionDoesNotOverwriteActiveState(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	manager := NewManager(repo)
	sessionID := sessionID(t, "demo")

	started, err := manager.Start(sessionID, 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	before, err := os.ReadFile(manager.activeStatePath(sessionID))
	if err != nil {
		t.Fatalf("read active state before collision: %v", err)
	}

	if err := manager.writeActive(sessionID, started.TurnID, started.Pre); err == nil {
		t.Fatal("writeActive overwrote existing active state")
	}
	after, err := os.ReadFile(manager.activeStatePath(sessionID))
	if err != nil {
		t.Fatalf("read active state after collision: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("active state changed after collision:\nbefore=%s\nafter=%s", before, after)
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
