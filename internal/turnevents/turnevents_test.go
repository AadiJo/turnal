package turnevents

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/turns"
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
	if payload.UserGit.Exists {
		t.Fatalf("payload user_git exists = true outside workspace Git repo: %#v", payload.UserGit)
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

func TestRecoverCheckpointIntentJournalPromotesExistingRef(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	writeFile(t, root, "app.txt", "before\n")

	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	if err := repo.BeginCheckpointJournal(sessionID, turnID, primitives.CheckpointPhasePre, primitives.AdapterManual, ""); err != nil {
		t.Fatalf("BeginCheckpointJournal: %v", err)
	}
	created, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}

	log := eventlog.Open(repo.MetadataDir)
	if err := RecoverCheckpointJournals(log, repo); err != nil {
		t.Fatalf("RecoverCheckpointJournals: %v", err)
	}
	if _, ok, err := repo.ReadCheckpointJournal(sessionID, turnID, primitives.CheckpointPhasePre); err != nil || ok {
		t.Fatalf("journal ok=%t err=%v, want cleared", ok, err)
	}
	events, err := log.Read(sessionID)
	if err != nil {
		t.Fatalf("Read events: %v", err)
	}
	if len(events) != 2 || events[0].Type != primitives.EventTypeTurnStart || events[1].Type != primitives.EventTypeCheckpoint {
		t.Fatalf("events = %#v, want recovered turn.start and checkpoint", events)
	}
	var payload checkpointPayload
	if err := json.Unmarshal(events[1].Payload, &payload); err != nil {
		t.Fatalf("unmarshal checkpoint payload: %v", err)
	}
	if payload.Ref != created.Ref.String() || payload.CommitSHA != created.Commit.String() {
		t.Fatalf("payload checkpoint = %s %s, want %s %s", payload.CommitSHA, payload.Ref, created.Commit, created.Ref)
	}
}

func TestRecoverPostCheckpointJournalClearsStaleActiveTurn(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sessionID := sessionID(t, "demo")
	writeFile(t, root, "app.txt", "before\n")
	started, err := (Recorder{
		Log:     eventlog.Open(repo.MetadataDir),
		Manager: turns.NewManager(repo),
		Adapter: primitives.AdapterManual,
	}).Start(sessionID, 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	writeFile(t, root, "app.txt", "after\n")
	manager := turns.NewManager(repo).WithCheckpointEvents(primitives.AdapterManual, "")
	if _, err := manager.Finish(sessionID, started.TurnID); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	writeActiveState(t, repo, sessionID, started)

	if err := RecoverCheckpointJournals(eventlog.Open(repo.MetadataDir), repo); err != nil {
		t.Fatalf("RecoverCheckpointJournals: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo.TmpDir, "turns", sessionID.String()+".json")); !os.IsNotExist(err) {
		t.Fatalf("active state still exists or stat failed: %v", err)
	}
}

func TestAppendCheckpointRecordsUserGitContext(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	runGit(t, root.String(), "init", "-q")
	bootstrapped, err := checkpoint.Bootstrap(root)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	repo := bootstrapped.Repo
	runGit(t, root.String(), "config", "user.email", "turnal@example.test")
	runGit(t, root.String(), "config", "user.name", "turnal")
	writeFile(t, root, "README.md", "base\n")
	runGit(t, root.String(), "add", ".gitignore", "README.md")
	runGit(t, root.String(), "commit", "-q", "-m", "base")
	head := strings.TrimSpace(runGit(t, root.String(), "rev-parse", "HEAD"))

	sessionID := sessionID(t, "demo")
	started, err := (Recorder{
		Log:     eventlog.Open(repo.MetadataDir),
		Manager: turns.NewManager(repo),
		Adapter: primitives.AdapterManual,
	}).Start(sessionID, 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	events, err := eventlog.Open(repo.MetadataDir).Read(sessionID)
	if err != nil {
		t.Fatalf("Read events: %v", err)
	}
	var payload checkpointPayload
	for _, event := range events {
		if event.Type != primitives.EventTypeCheckpoint {
			continue
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("unmarshal checkpoint payload: %v", err)
		}
	}
	if payload.Turn != started.TurnID.Uint64() {
		t.Fatalf("payload turn = %d, want %s", payload.Turn, started.TurnID)
	}
	if !payload.UserGit.Exists || payload.UserGit.Head != head || payload.UserGit.Branch == "" || payload.UserGit.Detached {
		t.Fatalf("payload user_git missing head/branch: %#v", payload.UserGit)
	}
	if payload.UserGit.Dirty {
		t.Fatalf("payload user_git dirty = true, want clean: %#v", payload.UserGit)
	}
}

func writeActiveState(t *testing.T, repo *checkpoint.Repo, sessionID primitives.SessionID, started turns.StartResult) {
	t.Helper()
	dir := filepath.Join(repo.TmpDir, "turns")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir active state dir: %v", err)
	}
	state := map[string]any{
		"version":    1,
		"session_id": sessionID.String(),
		"turn_id":    started.TurnID.Uint64(),
		"pre_ref":    started.Pre.Ref.String(),
		"pre_commit": started.Pre.Commit.String(),
		"started_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("marshal active state: %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(dir, sessionID.String()+".json"), data, 0o644); err != nil {
		t.Fatalf("write active state: %v", err)
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

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
