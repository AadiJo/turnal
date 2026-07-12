package runs

import (
	"os/exec"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
)

func TestProjectionKeepsCapturesDistinctAndAttemptsAtProviderTurns(t *testing.T) {
	repo := testRepo(t)
	runID, _ := primitives.NewRunID()
	wrapper := session(t, "wrapper-session")
	provider := session(t, "provider-session")
	if err := Start(repo, runID, wrapper, []string{"codex"}); err != nil {
		t.Fatal(err)
	}
	if err := LinkCapture(repo, runID, CaptureWrapper, wrapper, primitives.AdapterCodex); err != nil {
		t.Fatal(err)
	}
	if err := LinkCapture(repo, runID, CaptureProvider, provider, primitives.AdapterCodex); err != nil {
		t.Fatal(err)
	}
	// Duplicate hook delivery must not append another relationship.
	if err := LinkCapture(repo, runID, CaptureProvider, provider, primitives.AdapterCodex); err != nil {
		t.Fatal(err)
	}
	for number := uint64(1); number <= 2; number++ {
		turn, _ := primitives.NewTurnID(number)
		attempt, err := EnsureAttempt(repo, runID, provider, turn, primitives.AdapterCodex)
		if err != nil {
			t.Fatal(err)
		}
		again, err := EnsureAttempt(repo, runID, provider, turn, primitives.AdapterCodex)
		if err != nil || attempt != again {
			t.Fatalf("attempt not idempotent: %s %s %v", attempt, again, err)
		}
		appendTurnField(t, repo, provider, turn, primitives.EventTypePromptUser)
	}

	projection, err := Read(repo, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Captures) != 2 || len(projection.Attempts) != 2 || projection.Shape != "multi-attempt" {
		t.Fatalf("projection = %+v", projection)
	}
	if projection.Captures[0].SessionID == projection.Captures[1].SessionID {
		t.Fatal("capture sessions were consolidated")
	}
	for _, attempt := range projection.Attempts {
		if len(attempt.Fields) != 1 || attempt.Fields[0].EventType != primitives.EventTypePromptUser {
			t.Fatalf("field provenance = %+v", attempt.Fields)
		}
	}
}

func TestProjectionWrapperOnlyAndFailedRun(t *testing.T) {
	repo := testRepo(t)
	runID, _ := primitives.NewRunID()
	wrapper := session(t, "wrapper-only")
	if err := Start(repo, runID, wrapper, []string{"codex"}); err != nil {
		t.Fatal(err)
	}
	if err := LinkCapture(repo, runID, CaptureWrapper, wrapper, primitives.AdapterCodex); err != nil {
		t.Fatal(err)
	}
	projection, err := Read(repo, runID)
	if err != nil || projection.Shape != "wrapper-only" || projection.Status != StatusRunning {
		t.Fatalf("projection = %+v, %v", projection, err)
	}
	if err := Finish(repo, runID, wrapper, StatusFailed, "exit 2"); err != nil {
		t.Fatal(err)
	}
	projection, err = Read(repo, runID)
	if err != nil || projection.Status != StatusFailed || projection.Error != "exit 2" {
		t.Fatalf("failed projection = %+v, %v", projection, err)
	}
	if _, err := AcceptsCapture(repo, runID); err == nil {
		t.Fatal("finished run accepted capture")
	}
}

func TestRejectsFabricatedAndForeignRun(t *testing.T) {
	repo := testRepo(t)
	foreign := testRepo(t)
	runID, _ := primitives.NewRunID()
	wrapper := session(t, "wrapper")
	if err := Start(repo, runID, wrapper, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := AcceptsCapture(foreign, runID); err == nil {
		t.Fatal("foreign store accepted run")
	}
	incompatible := *repo
	incompatible.WorktreeID, _ = primitives.NewWorktreeID()
	if _, err := AcceptsCapture(&incompatible, runID); err == nil {
		t.Fatal("incompatible worktree accepted run")
	}
	fabricated, _ := primitives.NewRunID()
	if _, err := AcceptsCapture(repo, fabricated); err == nil {
		t.Fatal("fabricated run accepted")
	}
}

func TestLegacySessionRemainsUnlinked(t *testing.T) {
	repo := testRepo(t)
	legacy := session(t, "legacy")
	appendTurnField(t, repo, legacy, 1, primitives.EventTypePromptUser)
	runID, _ := primitives.NewRunID()
	if _, err := Read(repo, runID); err == nil {
		t.Fatal("legacy session was heuristically linked")
	}
	inventory, err := Inspect(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Runs) != 0 || len(inventory.UnlinkedCaptures) != 1 || inventory.UnlinkedCaptures[0].SessionID != legacy {
		t.Fatalf("legacy inventory = %+v", inventory)
	}
}

func appendTurnField(t *testing.T, repo *checkpoint.Repo, sessionID primitives.SessionID, turnID primitives.TurnID, eventType primitives.EventType) {
	t.Helper()
	if _, err := repo.EventLog().Append(eventlog.AppendInput{SessionID: sessionID, TurnID: &turnID, Type: eventType, Adapter: primitives.AdapterCodex, Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
}

func testRepo(t *testing.T) *checkpoint.Repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required")
	}
	root, err := primitives.ParseWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func session(t *testing.T, value string) primitives.SessionID {
	t.Helper()
	id, err := primitives.ParseSessionID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
