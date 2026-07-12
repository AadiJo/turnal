package runs

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/filelock"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/processidentity"
)

func TestLifecycleLockTakeoverDoesNotAuthorizeDescendant(t *testing.T) {
	repo := testRepo(t)
	runID, _ := primitives.NewRunID()
	command := exec.Command(os.Args[0], "-test.run=TestLifecycleOwnerHelper$")
	command.Env = append(os.Environ(), "TURNAL_LIFECYCLE_HELPER_ROOT="+repo.WorkspaceRoot.String(), "TURNAL_LIFECYCLE_HELPER_RUN="+runID.String())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("owner helper: %v\n%s", err, output)
	}
	journal, err := readLifecycleJournal(lifecycleJournalPath(repo, runID))
	if err != nil {
		t.Fatal(err)
	}
	takeover, err := filelock.Acquire(lifecycleLockPath(repo, runID), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = takeover.Release() }()
	forged, _ := json.Marshal(map[string]any{"PID": journal.OwnerPID, "AcquiredAt": "copied"})
	if err := os.WriteFile(lifecycleLockPath(repo, runID), forged, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AcceptsCapture(repo, runID); err == nil {
		t.Fatal("replacement lifecycle owner authorized capture")
	}
	projection, err := Read(repo, runID)
	if err != nil || projection.Status != StatusIncomplete {
		t.Fatalf("takeover projection = %+v, %v", projection, err)
	}
}

func TestLifecycleOwnerHelper(t *testing.T) {
	rootText := os.Getenv("TURNAL_LIFECYCLE_HELPER_ROOT")
	if rootText == "" {
		return
	}
	root, err := primitives.ParseWorkspaceRoot(rootText)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := checkpoint.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	runID, err := primitives.ParseRunID(os.Getenv("TURNAL_LIFECYCLE_HELPER_RUN"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Begin(repo, runID, session(t, "wrapper"), nil); err != nil {
		t.Fatal(err)
	}
}

func TestBeginKeepsJournalWhenStartCommitIsUncertain(t *testing.T) {
	repo := testRepo(t)
	runID, _ := primitives.NewRunID()
	wrapper := session(t, "wrapper")
	release, err := begin(repo, runID, wrapper, nil, func(repo *checkpoint.Repo, runID primitives.RunID, sessionID primitives.SessionID, command []string, owner processidentity.Identity) error {
		if err := start(repo, runID, sessionID, command, owner); err != nil {
			return err
		}
		return errors.New("simulated derived-state failure after durable append")
	})
	if release != nil || err == nil {
		t.Fatalf("begin returned release=%v, error=%v", release != nil, err)
	}
	if _, err := os.Stat(lifecycleJournalPath(repo, runID)); err != nil {
		t.Fatalf("lifecycle journal was removed: %v", err)
	}
	projection, err := Read(repo, runID)
	if err != nil || projection.Status != StatusRunning {
		t.Fatalf("uncertain start projection = %+v, %v", projection, err)
	}
	if err := RecoverAbandoned(repo); err != nil {
		t.Fatal(err)
	}
	projection, err = Read(repo, runID)
	if err != nil || projection.Status != StatusIncomplete {
		t.Fatalf("recovered uncertain start = %+v, %v", projection, err)
	}
}

func TestBeginRejectsExistingRunID(t *testing.T) {
	repo := testRepo(t)
	runID, _ := primitives.NewRunID()
	wrapper := session(t, "wrapper")
	release, err := Begin(repo, runID, wrapper, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := Begin(repo, runID, session(t, "other"), nil); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate Begin error = %v", err)
	}
	projection, err := Read(repo, runID)
	if err != nil || projection.Start.SessionID != wrapper {
		t.Fatalf("duplicate Begin changed run: %+v, %v", projection, err)
	}
}

func TestRecoveryAndCaptureMutationSerialize(t *testing.T) {
	repo := testRepo(t)
	runID, _ := primitives.NewRunID()
	wrapper := session(t, "wrapper")
	provider := session(t, "provider")
	release, err := Begin(repo, runID, wrapper, nil)
	if err != nil {
		t.Fatal(err)
	}
	release()
	start := make(chan struct{})
	errs := make(chan error, 2)
	var group sync.WaitGroup
	group.Add(2)
	go func() { defer group.Done(); <-start; errs <- RecoverAbandoned(repo) }()
	go func() {
		defer group.Done()
		<-start
		errs <- LinkCapture(repo, runID, CaptureProvider, provider, primitives.AdapterCodex)
	}()
	close(start)
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !strings.Contains(err.Error(), "cannot accept capture") {
			t.Fatalf("concurrent mutation error = %v", err)
		}
	}
	projection, err := Read(repo, runID)
	if err != nil || projection.Status != StatusIncomplete || len(projection.Captures) != 0 {
		t.Fatalf("concurrent projection = %+v, %v", projection, err)
	}
	if finishes := countRunEvents(t, repo, primitives.EventTypeRunFinish); finishes != 1 {
		t.Fatalf("run.finish events = %d, want 1", finishes)
	}
}

func TestConcurrentRecoveriesAppendOneFinish(t *testing.T) {
	repo := testRepo(t)
	runID, _ := primitives.NewRunID()
	wrapper := session(t, "wrapper")
	release, err := Begin(repo, runID, wrapper, nil)
	if err != nil {
		t.Fatal(err)
	}
	release()
	var group sync.WaitGroup
	errs := make(chan error, 8)
	for index := 0; index < 8; index++ {
		group.Add(1)
		go func() { defer group.Done(); errs <- RecoverAbandoned(repo) }()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if finishes := countRunEvents(t, repo, primitives.EventTypeRunFinish); finishes != 1 {
		t.Fatalf("run.finish events = %d, want 1", finishes)
	}
}

func countRunEvents(t *testing.T, repo *checkpoint.Repo, eventType primitives.EventType) int {
	t.Helper()
	streams, err := eventlog.ListDurableStreams(repo.MetadataDir)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, stream := range streams {
		for _, event := range stream.Events {
			if event.Type == eventType {
				count++
			}
		}
	}
	return count
}

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

func TestRecoverySkipsSameStoreJournalFromAnotherWorktree(t *testing.T) {
	repo := testRepo(t)
	runID, _ := primitives.NewRunID()
	release, err := Begin(repo, runID, session(t, "wrapper"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	linked := *repo
	linked.WorktreeID, _ = primitives.NewWorktreeID()
	linked.EventProducerID, _ = primitives.NewEventProducerID()
	if err := RecoverAbandoned(&linked); err != nil {
		t.Fatalf("linked-worktree recovery: %v", err)
	}
	projection, err := Read(repo, runID)
	if err != nil || projection.Status != StatusRunning {
		t.Fatalf("foreign recovery changed run: %+v, %v", projection, err)
	}
}

func TestRecoveryDoesNotTrustTamperedJournalWorktree(t *testing.T) {
	repo := testRepo(t)
	runID, _ := primitives.NewRunID()
	release, err := Begin(repo, runID, session(t, "wrapper"), nil)
	if err != nil {
		t.Fatal(err)
	}
	release()
	journal, err := readLifecycleJournal(lifecycleJournalPath(repo, runID))
	if err != nil {
		t.Fatal(err)
	}
	journal.WorktreeID, _ = primitives.NewWorktreeID()
	if err := writeLifecycleJournal(repo, journal); err != nil {
		t.Fatal(err)
	}
	if err := RecoverAbandoned(repo); err != nil {
		t.Fatal(err)
	}
	projection, err := Read(repo, runID)
	if err != nil || projection.WorktreeID != repo.WorktreeID || projection.Status != StatusIncomplete {
		t.Fatalf("durable projection = %+v, %v", projection, err)
	}
}

func TestRecoveryUsesFilenameWhenJournalRunIDIsCorrupt(t *testing.T) {
	repo := testRepo(t)
	runID, _ := primitives.NewRunID()
	release, err := Begin(repo, runID, session(t, "wrapper"), nil)
	if err != nil {
		t.Fatal(err)
	}
	path := lifecycleJournalPath(repo, runID)
	journal, err := readLifecycleJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	journal.RunID, _ = primitives.NewRunID()
	data, _ := json.Marshal(journal)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	release()
	if err := RecoverAbandoned(repo); err != nil {
		t.Fatal(err)
	}
	projection, err := Read(repo, runID)
	if err != nil || projection.Status != StatusIncomplete {
		t.Fatalf("recovered projection = %+v, %v", projection, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("scanned journal still exists: %v", err)
	}
}

func TestRecoveryQuarantinesMalformedFilenameAndContinues(t *testing.T) {
	repo := testRepo(t)
	runID, _ := primitives.NewRunID()
	release, err := Begin(repo, runID, session(t, "wrapper"), nil)
	if err != nil {
		t.Fatal(err)
	}
	release()

	malformedPath := filepath.Join(lifecycleDir(repo), "corrupt.json")
	malformed := []byte("preserve this malformed lifecycle journal\n")
	if err := os.WriteFile(malformedPath, malformed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RecoverAbandoned(repo); err != nil {
		t.Fatal(err)
	}

	projection, err := Read(repo, runID)
	if err != nil || projection.Status != StatusIncomplete {
		t.Fatalf("valid recovery after malformed journal = %+v, %v", projection, err)
	}
	if _, err := os.Stat(malformedPath); !os.IsNotExist(err) {
		t.Fatalf("malformed journal remains active: %v", err)
	}
	quarantined, err := os.ReadDir(filepath.Join(lifecycleDir(repo), "quarantine"))
	if err != nil || len(quarantined) != 1 {
		t.Fatalf("quarantine entries = %v, %v", quarantined, err)
	}
	data, err := os.ReadFile(filepath.Join(lifecycleDir(repo), "quarantine", quarantined[0].Name()))
	if err != nil || string(data) != string(malformed) {
		t.Fatalf("quarantined journal = %q, %v", data, err)
	}
	if err := RecoverAbandoned(repo); err != nil {
		t.Fatalf("repeat recovery: %v", err)
	}
}

func TestCaptureAuthorizationRequiresLockedMatchingLifecycle(t *testing.T) {
	repo := testRepo(t)
	runID, _ := primitives.NewRunID()
	wrapper := session(t, "wrapper")
	release, err := Begin(repo, runID, wrapper, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	if err := os.Remove(lifecycleJournalPath(repo, runID)); err != nil {
		t.Fatal(err)
	}
	if _, err := AcceptsCapture(repo, runID); err == nil || !strings.Contains(err.Error(), "no active lifecycle journal") {
		t.Fatalf("missing journal authorization error = %v", err)
	}
	projection, err := Read(repo, runID)
	if err != nil || projection.Status != StatusRunning {
		t.Fatalf("durable stale run = %+v, %v", projection, err)
	}
}

func TestWritersRejectRelationshipsTheirProjectionWouldReject(t *testing.T) {
	repo := testRepo(t)
	runID, _ := primitives.NewRunID()
	wrapper := session(t, "wrapper")
	beginTestRun(t, repo, runID, wrapper, nil)
	wrong := session(t, "wrong")
	if err := LinkCapture(repo, runID, CaptureWrapper, wrong, primitives.AdapterCodex); err == nil {
		t.Fatal("wrong wrapper session accepted")
	}
	if _, err := EnsureAttempt(repo, runID, wrong, 1, primitives.AdapterCodex); err == nil {
		t.Fatal("attempt without provider capture accepted")
	}
	if err := Finish(repo, runID, wrong, StatusIncomplete, "bad"); err == nil {
		t.Fatal("finish from wrong session accepted")
	}
	projection, err := Read(repo, runID)
	if err != nil || projection.Status != StatusRunning || len(projection.Captures) != 0 || len(projection.Attempts) != 0 {
		t.Fatalf("rejected writes corrupted projection: %+v, %v", projection, err)
	}
}

func TestRecoveryRejectsJournalSessionDifferentFromRunStart(t *testing.T) {
	repo := testRepo(t)
	runID, _ := primitives.NewRunID()
	wrapper := session(t, "wrapper")
	release, err := Begin(repo, runID, wrapper, nil)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := readLifecycleJournal(lifecycleJournalPath(repo, runID))
	if err != nil {
		t.Fatal(err)
	}
	journal.SessionID = session(t, "wrong")
	if err := writeLifecycleJournal(repo, journal); err != nil {
		t.Fatal(err)
	}
	release()
	if err := RecoverAbandoned(repo); err != nil {
		t.Fatal(err)
	}
	projection, err := Read(repo, runID)
	if err != nil || projection.Status != StatusIncomplete {
		t.Fatalf("mismatched recovery mutated run: %+v, %v", projection, err)
	}
}

func TestProjectionRejectsAttemptIDBoundToTwoTurns(t *testing.T) {
	repo := testRepo(t)
	runID, _ := primitives.NewRunID()
	wrapper := session(t, "wrapper")
	provider := session(t, "provider")
	beginTestRun(t, repo, runID, wrapper, nil)
	if err := LinkCapture(repo, runID, CaptureProvider, provider, primitives.AdapterCodex); err != nil {
		t.Fatal(err)
	}
	attemptID, _ := primitives.NewAttemptID()
	for number := uint64(1); number <= 2; number++ {
		turn := primitives.TurnID(number)
		appendRelationship(t, repo, eventlog.AppendInput{SessionID: provider, TurnID: &turn, Type: primitives.EventTypeRunAttemptLink, Adapter: primitives.AdapterCodex, SourceID: "attempt-" + turn.String(), Payload: relationshipJSON(attemptPayload{RunID: runID, AttemptID: attemptID, SessionID: provider, TurnID: turn})})
	}
	if _, err := Read(repo, runID); err == nil || !strings.Contains(err.Error(), "is already bound") {
		t.Fatalf("duplicate attempt id error = %v", err)
	}
}

func TestTranscriptProvenanceStaysInAttemptStream(t *testing.T) {
	repo := testRepo(t)
	runID, _ := primitives.NewRunID()
	wrapper := session(t, "wrapper")
	provider := session(t, "shared-provider")
	beginTestRun(t, repo, runID, wrapper, nil)
	if err := LinkCapture(repo, runID, CaptureProvider, provider, primitives.AdapterCodex); err != nil {
		t.Fatal(err)
	}
	appendRelationship(t, repo, eventlog.AppendInput{SessionID: provider, Type: primitives.EventTypeSessionStart, Adapter: primitives.AdapterCodex, Payload: relationshipJSON(map[string]string{"transcript_path": "one"})})
	if _, err := EnsureAttempt(repo, runID, provider, 1, primitives.AdapterCodex); err != nil {
		t.Fatal(err)
	}

	other := *repo
	other.EventProducerID, _ = primitives.NewEventProducerID()
	otherStream, _ := other.StreamID(provider)
	appendRelationship(t, &other, eventlog.AppendInput{SessionID: provider, Type: primitives.EventTypeRunCaptureLink, Adapter: primitives.AdapterCodex, Payload: relationshipJSON(capturePayload{RunID: runID, Kind: CaptureProvider, SessionID: provider, Adapter: primitives.AdapterCodex})})
	appendRelationship(t, &other, eventlog.AppendInput{SessionID: provider, Type: primitives.EventTypeSessionStart, Adapter: primitives.AdapterCodex, Payload: relationshipJSON(map[string]string{"transcript_path": "two"})})
	turn := primitives.TurnID(2)
	attemptID, _ := primitives.NewAttemptID()
	appendRelationship(t, &other, eventlog.AppendInput{SessionID: provider, TurnID: &turn, Type: primitives.EventTypeRunAttemptLink, Adapter: primitives.AdapterCodex, Payload: relationshipJSON(attemptPayload{RunID: runID, AttemptID: attemptID, SessionID: provider, TurnID: turn})})

	projection, err := Read(repo, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Attempts) != 2 {
		t.Fatalf("attempts = %+v", projection.Attempts)
	}
	for _, attempt := range projection.Attempts {
		transcripts := 0
		for _, source := range attempt.Fields {
			if source.Field == "transcript" {
				transcripts++
				if source.StreamID != attempt.Provenance.StreamID {
					t.Fatalf("cross-stream transcript: attempt=%+v source=%+v other=%s", attempt, source, otherStream)
				}
			}
		}
		if transcripts != 1 {
			t.Fatalf("attempt transcript provenance = %+v", attempt.Fields)
		}
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
			beginTestRun(t, repo, runID, wrapper, nil)
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
	beginTestRun(t, repo, runID, wrapper, []string{"codex"})
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
	beginTestRun(t, repo, runID, wrapper, []string{"codex"})
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
	beginTestRun(t, repo, runID, wrapper, nil)
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

func beginTestRun(t *testing.T, repo *checkpoint.Repo, runID primitives.RunID, wrapper primitives.SessionID, command []string) {
	t.Helper()
	release, err := Begin(repo, runID, wrapper, command)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
}

func session(t *testing.T, value string) primitives.SessionID {
	t.Helper()
	id, err := primitives.ParseSessionID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
