package integrity

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/adapters"
	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	rollbackengine "github.com/AadiJo/turnal/internal/rollback"
)

func TestInspectReportsCheckpointEventCommitMismatch(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	writeFile(t, root, "app.txt", "one\n")
	pre, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("pre checkpoint: %v", err)
	}
	writeFile(t, root, "app.txt", "two\n")
	post, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost)
	if err != nil {
		t.Fatalf("post checkpoint: %v", err)
	}
	appendCheckpointEvent(t, repo, sessionID, turnID, pre.Ref.String(), post.Commit.String())

	report := Inspect(repo)
	if !containsProblem(report.Problems, "checkpoint event commit mismatch") {
		t.Fatalf("problems = %#v, want checkpoint event commit mismatch", report.Problems)
	}
}

func TestInspectReportsMalformedRollbackJournal(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := os.WriteFile(rollbackengine.JournalPath(repo), []byte("{"), 0o644); err != nil {
		t.Fatalf("write rollback journal: %v", err)
	}

	report := Inspect(repo)
	if !containsProblem(report.Problems, "unreadable rollback journal") {
		t.Fatalf("problems = %#v, want unreadable rollback journal", report.Problems)
	}
}

func TestInspectReportsPersistedHookFailures(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	if err := adapters.RecordHookFailure(primitives.AdapterCodex, "after_agent", []byte(`{"session_id":"demo"}`), errors.New("adapter log unavailable")); err != nil {
		t.Fatalf("RecordHookFailure: %v", err)
	}
	report := Inspect(repo)
	if !containsProblem(report.Problems, "hook capture failure") || !containsProblem(report.Problems, "clear-hook-failures") {
		t.Fatalf("problems = %#v, want actionable hook failure", report.Problems)
	}
}

func TestInspectReportsPartialRawAdapterTail(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	path := filepath.Join(repo.MetadataDir, "log", "raw", "demo", "codex.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir raw log: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"partial":`), 0o600); err != nil {
		t.Fatalf("write partial raw log: %v", err)
	}
	report := Inspect(repo)
	if !containsProblem(report.Problems, "trailing partial record") {
		t.Fatalf("problems = %#v, want partial raw record", report.Problems)
	}
}

func appendCheckpointEvent(t *testing.T, repo *checkpoint.Repo, sessionID primitives.SessionID, turnID primitives.TurnID, ref string, commit string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"turn":       turnID.Uint64(),
		"phase":      primitives.CheckpointPhasePre.String(),
		"commit_sha": commit,
		"ref":        ref,
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
		t.Fatalf("Append: %v", err)
	}
}

func containsProblem(problems []string, needle string) bool {
	for _, problem := range problems {
		if strings.Contains(problem, needle) {
			return true
		}
	}
	return false
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
