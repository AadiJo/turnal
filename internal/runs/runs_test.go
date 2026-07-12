package runs

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
)

func TestRecoverAbandonedRunWithoutTouchingLiveRun(t *testing.T) {
	repo := testRepo(t)
	runID, _ := primitives.NewRunID()
	wrapper := session(t, "abandoned-wrapper")
	release, err := Begin(repo, runID, wrapper, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	if err := RecoverAbandoned(repo); err != nil {
		t.Fatal(err)
	}
	projection, err := Read(repo, runID)
	if err != nil || projection.Status != StatusRunning {
		t.Fatalf("live run was recovered: %+v, %v", projection, err)
	}

	// Releasing without Finish simulates the OS releasing the lock after a hard exit.
	release()
	if _, err := AcceptsCapture(repo, runID); err == nil {
		t.Fatal("abandoned run accepted capture")
	}
	projection, err = Read(repo, runID)
	if err != nil || projection.Status != StatusIncomplete || !strings.Contains(projection.Error, "owner process exited") {
		t.Fatalf("abandoned projection = %+v, %v", projection, err)
	}
}

func TestProjectionRejectsMalformedRelationshipEnvelopes(t *testing.T) {
	tests := []struct {
		name   string
		append func(*testing.T, *checkpoint.Repo, primitives.RunID, primitives.SessionID)
		want   string
	}{
		{"capture kind", func(t *testing.T, repo *checkpoint.Repo, runID primitives.RunID, wrapper primitives.SessionID) {
			appendRelationship(t, repo, eventlog.AppendInput{SessionID: wrapper, Type: primitives.EventTypeRunCaptureLink, Adapter: primitives.AdapterCodex, Payload: relationshipJSON(map[string]any{"run_id": runID, "kind": "guessed", "session_id": wrapper, "adapter": "codex"})})
		}, "invalid capture kind"},
		{"capture session", func(t *testing.T, repo *checkpoint.Repo, runID primitives.RunID, wrapper primitives.SessionID) {
			appendRelationship(t, repo, eventlog.AppendInput{SessionID: wrapper, Type: primitives.EventTypeRunCaptureLink, Adapter: primitives.AdapterCodex, Payload: relationshipJSON(map[string]any{"run_id": runID, "kind": CaptureWrapper, "session_id": "other", "adapter": "codex"})})
		}, "capture payload does not match"},
		{"attempt id", func(t *testing.T, repo *checkpoint.Repo, runID primitives.RunID, wrapper primitives.SessionID) {
			provider := session(t, "provider")
			if err := LinkCapture(repo, runID, CaptureProvider, provider, primitives.AdapterCodex); err != nil {
				t.Fatal(err)
			}
			turn := primitives.TurnID(1)
			appendRelationship(t, repo, eventlog.AppendInput{SessionID: provider, TurnID: &turn, Type: primitives.EventTypeRunAttemptLink, Adapter: primitives.AdapterCodex, Payload: relationshipJSON(map[string]any{"run_id": runID, "attempt_id": "attempt_bad", "session_id": provider, "turn_id": 1})})
		}, "invalid attempt id"},
		{"attempt turn", func(t *testing.T, repo *checkpoint.Repo, runID primitives.RunID, wrapper primitives.SessionID) {
			provider := session(t, "provider")
			if err := LinkCapture(repo, runID, CaptureProvider, provider, primitives.AdapterCodex); err != nil {
				t.Fatal(err)
			}
			attemptID, _ := primitives.NewAttemptID()
			eventTurn := primitives.TurnID(2)
			appendRelationship(t, repo, eventlog.AppendInput{SessionID: provider, TurnID: &eventTurn, Type: primitives.EventTypeRunAttemptLink, Adapter: primitives.AdapterCodex, Payload: relationshipJSON(map[string]any{"run_id": runID, "attempt_id": attemptID, "session_id": provider, "turn_id": 1})})
		}, "attempt payload does not match"},
		{"finish status", func(t *testing.T, repo *checkpoint.Repo, runID primitives.RunID, wrapper primitives.SessionID) {
			appendRelationship(t, repo, eventlog.AppendInput{SessionID: wrapper, Type: primitives.EventTypeRunFinish, Adapter: primitives.AdapterCodex, Payload: relationshipJSON(map[string]any{"run_id": runID, "status": "running"})})
		}, "invalid terminal status"},
		{"duplicate finish", func(t *testing.T, repo *checkpoint.Repo, runID primitives.RunID, wrapper primitives.SessionID) {
			for _, source := range []string{"one", "two"} {
				appendRelationship(t, repo, eventlog.AppendInput{SessionID: wrapper, Type: primitives.EventTypeRunFinish, Adapter: primitives.AdapterCodex, SourceID: source, Payload: relationshipJSON(map[string]any{"run_id": runID, "status": StatusIncomplete})})
			}
		}, "duplicate run finish"},
		{"foreign envelope", func(t *testing.T, repo *checkpoint.Repo, runID primitives.RunID, wrapper primitives.SessionID) {
			foreign := *repo
			foreign.WorktreeID, _ = primitives.NewWorktreeID()
			foreign.EventProducerID, _ = primitives.NewEventProducerID()
			appendRelationship(t, &foreign, eventlog.AppendInput{SessionID: wrapper, Type: primitives.EventTypeRunCaptureLink, Adapter: primitives.AdapterCodex, Payload: relationshipJSON(map[string]any{"run_id": runID, "kind": CaptureWrapper, "session_id": wrapper, "adapter": "codex"})})
		}, "repository or worktree identity"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := testRepo(t)
			runID, _ := primitives.NewRunID()
			wrapper := session(t, "wrapper")
			if err := Start(repo, runID, wrapper, nil); err != nil {
				t.Fatal(err)
			}
			test.append(t, repo, runID, wrapper)
			if _, err := Read(repo, runID); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Read error = %v, want %q", err, test.want)
			}
		})
	}
}

func appendRelationship(t *testing.T, repo *checkpoint.Repo, input eventlog.AppendInput) {
	t.Helper()
	if _, err := repo.EventLog().Append(input); err != nil {
		t.Fatal(err)
	}
}
func relationshipJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

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
