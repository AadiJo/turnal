package blame

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	queryindex "github.com/AadiJo/turnal/internal/index"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/provenance"
	"github.com/AadiJo/turnal/internal/turnevents"
	"github.com/AadiJo/turnal/internal/turns"
)

func TestComputeBlameOverlappingEdits(t *testing.T) {
	root, repo := newBlameRepo(t)
	sessionID := blameSessionID(t, "demo")

	writeBlameFile(t, root, "app.txt", "alpha\nbeta\ngamma\n")
	captureBlameTurn(t, repo, root, sessionID, 1, "app.txt", "alpha\nbeta v1\ngamma\n", "first beta")
	captureBlameTurn(t, repo, root, sessionID, 2, "app.txt", "alpha\nbeta v2\ngamma\n", "second beta")

	path, err := primitives.ParseRepoPath("app.txt")
	if err != nil {
		t.Fatalf("ParseRepoPath: %v", err)
	}
	result, err := New(repo).Compute(Query{Path: path, Line: 2})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("entries len = %d, want 1", len(result.Entries))
	}
	entry := result.Entries[0]
	if entry.Text != "beta v2" {
		t.Fatalf("line text = %q, want beta v2", entry.Text)
	}
	if entry.Origin.Kind != "turn" || entry.Origin.SessionID != sessionID || entry.Origin.TurnID.Uint64() != 2 {
		t.Fatalf("origin = %#v, want demo turn 2", entry.Origin)
	}
	if entry.Origin.Prompt != "second beta" {
		t.Fatalf("prompt = %q, want second beta", entry.Origin.Prompt)
	}
}

func TestComputeBlameDeletedLineShiftsOlderOrigin(t *testing.T) {
	root, repo := newBlameRepo(t)
	sessionID := blameSessionID(t, "demo")
	turn1 := blameTurnID(t, 1)
	turn2 := blameTurnID(t, 2)

	pre1, err := repo.CreateCheckpoint(sessionID, turn1, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("turn 1 pre: %v", err)
	}
	writeBlameFile(t, root, "notes.txt", "one\ntwo\nthree\n")
	post1, err := repo.CreateCheckpoint(sessionID, turn1, primitives.CheckpointPhasePost)
	if err != nil {
		t.Fatalf("turn 1 post: %v", err)
	}
	appendBlamePrompt(t, repo, sessionID, turn1, "add notes")
	_ = pre1
	_ = post1

	if _, err := repo.CreateCheckpoint(sessionID, turn2, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("turn 2 pre: %v", err)
	}
	writeBlameFile(t, root, "notes.txt", "one\nthree\n")
	if _, err := repo.CreateCheckpoint(sessionID, turn2, primitives.CheckpointPhasePost); err != nil {
		t.Fatalf("turn 2 post: %v", err)
	}
	appendBlamePrompt(t, repo, sessionID, turn2, "delete two")

	path, _ := primitives.ParseRepoPath("notes.txt")
	result, err := New(repo).Compute(Query{Path: path, Line: 2})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	entry := result.Entries[0]
	if entry.Text != "three" {
		t.Fatalf("line 2 text = %q, want three", entry.Text)
	}
	if entry.Origin.Kind != "turn" || entry.Origin.TurnID.Uint64() != 1 {
		t.Fatalf("line 2 origin = %#v, want turn 1", entry.Origin)
	}
}

func TestComputeBlameResynchronizesUntouchedPathAtLaterPreSnapshot(t *testing.T) {
	root, repo := newBlameRepo(t)
	sessionID := blameSessionID(t, "resync-gap")

	captureBlameTurn(t, repo, root, sessionID, 1, "app.txt", "recorded\n", "write recorded value")
	writeBlameFile(t, root, "app.txt", "unrecorded\n")

	turn2 := blameTurnID(t, 2)
	pre2, err := repo.CreateCheckpoint(sessionID, turn2, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("turn 2 pre: %v", err)
	}
	writeBlameFile(t, root, "other.txt", "unrelated\n")
	if _, err := repo.CreateCheckpoint(sessionID, turn2, primitives.CheckpointPhasePost); err != nil {
		t.Fatalf("turn 2 post: %v", err)
	}
	appendBlamePrompt(t, repo, sessionID, turn2, "change another file")

	path, _ := primitives.ParseRepoPath("app.txt")
	result, err := New(repo).Compute(Query{Path: path, Line: 1})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	entry := result.Entries[0]
	if entry.Text != "unrecorded" || entry.Origin.Kind != "baseline" || entry.Origin.CheckpointRef != pre2.Ref {
		t.Fatalf("resynchronized origin = %#v, want turn 2 pre baseline", entry)
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "resynchronized") {
		t.Fatalf("warnings = %#v, want resynchronization warning", result.Warnings)
	}
}

func TestComputeBlameAppendedLineBelongsToAppendingTurn(t *testing.T) {
	root, repo := newBlameRepo(t)
	sessionID := blameSessionID(t, "demo")

	writeBlameFile(t, root, "notes.txt", "alpha\nbeta\n")
	captureBlameTurn(t, repo, root, sessionID, 1, "notes.txt", "alpha\nbeta\ngamma\n", "append gamma")

	path, _ := primitives.ParseRepoPath("notes.txt")
	result, err := New(repo).Compute(Query{Path: path, SessionID: sessionID})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(result.Entries) != 3 {
		t.Fatalf("entries len = %d, want 3", len(result.Entries))
	}

	want := []struct {
		text string
		kind string
		turn uint64
	}{
		{text: "alpha", kind: "baseline"},
		{text: "beta", kind: "baseline"},
		{text: "gamma", kind: "turn", turn: 1},
	}
	for index, wantEntry := range want {
		entry := result.Entries[index]
		if entry.Text != wantEntry.text {
			t.Fatalf("line %d text = %q, want %q", index+1, entry.Text, wantEntry.text)
		}
		if entry.Origin.Kind != wantEntry.kind {
			t.Fatalf("line %d origin kind = %q, want %q; entry=%#v", index+1, entry.Origin.Kind, wantEntry.kind, entry)
		}
		if wantEntry.turn != 0 && entry.Origin.TurnID.Uint64() != wantEntry.turn {
			t.Fatalf("line %d origin turn = %s, want %d; entry=%#v", index+1, entry.Origin.TurnID, wantEntry.turn, entry)
		}
	}
}

func TestComputeBlameFollowsRenamedPath(t *testing.T) {
	root, repo := newBlameRepo(t)
	sessionID := blameSessionID(t, "demo")

	writeBlameFile(t, root, "old.txt", "alpha\nbeta\ngamma\n")
	captureBlameTurn(t, repo, root, sessionID, 1, "old.txt", "alpha\nbeta v1\ngamma\n", "edit beta")

	turn2 := blameTurnID(t, 2)
	if _, err := repo.CreateCheckpoint(sessionID, turn2, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("turn 2 pre: %v", err)
	}
	if err := os.Rename(filepath.Join(root.String(), "old.txt"), filepath.Join(root.String(), "new.txt")); err != nil {
		t.Fatalf("rename file: %v", err)
	}
	writeBlameFile(t, root, "new.txt", "alpha moved\nbeta v1\ngamma\n")
	if _, err := repo.CreateCheckpoint(sessionID, turn2, primitives.CheckpointPhasePost); err != nil {
		t.Fatalf("turn 2 post: %v", err)
	}
	appendBlamePrompt(t, repo, sessionID, turn2, "rename file")

	path, _ := primitives.ParseRepoPath("new.txt")
	result, err := New(repo).Compute(Query{Path: path})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(result.Entries) != 3 {
		t.Fatalf("entries len = %d, want 3", len(result.Entries))
	}
	if result.Entries[0].Text != "alpha moved" || result.Entries[0].Origin.TurnID.Uint64() != 2 {
		t.Fatalf("line 1 = %#v, want rename turn", result.Entries[0])
	}
	if result.Entries[1].Text != "beta v1" || result.Entries[1].Origin.TurnID.Uint64() != 1 {
		t.Fatalf("line 2 = %#v, want pre-rename edit turn", result.Entries[1])
	}
}

func TestComputeBlameSessionScopeUsesSessionEndpoint(t *testing.T) {
	root, repo := newBlameRepo(t)
	sessionA := blameSessionID(t, "session-a")
	sessionB := blameSessionID(t, "session-b")

	captureRecordedBlameTurn(t, repo, root, sessionA, 1, "shared.txt", "from a\n", "write from a")
	captureRecordedBlameTurn(t, repo, root, sessionB, 1, "shared.txt", "from b\n", "write from b")

	path, _ := primitives.ParseRepoPath("shared.txt")
	global, err := New(repo).Compute(Query{Path: path, Line: 1})
	if err != nil {
		t.Fatalf("global Compute: %v", err)
	}
	if global.Entries[0].Text != "from b" || global.Entries[0].Origin.SessionID != sessionB {
		t.Fatalf("global blame = %#v, want session-b", global.Entries[0])
	}

	scoped, err := New(repo).Compute(Query{Path: path, Line: 1, SessionID: sessionA})
	if err != nil {
		t.Fatalf("scoped Compute: %v", err)
	}
	if scoped.Entries[0].Text != "from a" || scoped.Entries[0].Origin.SessionID != sessionA {
		t.Fatalf("scoped blame = %#v, want session-a endpoint", scoped.Entries[0])
	}
}

func TestConcurrentTurnGroupsHandleSubsecondAndLegacyTimestamps(t *testing.T) {
	second := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	sessionA := blameSessionID(t, "same-second-a")
	sessionB := blameSessionID(t, "same-second-b")
	turnID := blameTurnID(t, 1)
	legacyTurn := func(session primitives.SessionID, first, last time.Time) completeTurn {
		return completeTurn{
			SessionID: session,
			TurnID:    turnID,
			Pre:       checkpoint.CheckpointRefInfo{SessionID: session, TurnID: turnID, Time: second},
			Post:      checkpoint.CheckpointRefInfo{SessionID: session, TurnID: turnID, Time: second},
			Events:    queryindex.TurnEventSummary{First: first, Last: last},
		}
	}
	preciseTurn := func(session primitives.SessionID, first, last time.Time) completeTurn {
		turn := legacyTurn(session, first, last)
		turn.PreEvent = first
		turn.PostEvent = last
		turn.PreEventPrecise = true
		turn.PostEventPrecise = true
		turn.HasCheckpointEvents = true
		return turn
	}

	overlapping := []completeTurn{
		preciseTurn(sessionA, second.Add(100*time.Nanosecond), second.Add(400*time.Nanosecond)),
		preciseTurn(sessionB, second.Add(200*time.Nanosecond), second.Add(500*time.Nanosecond)),
	}
	if groups := concurrentTurnGroups(overlapping); len(groups) != 1 || len(groups[0].Members) != 2 {
		t.Fatalf("subsecond overlap groups = %#v", groups)
	}

	sequential := []completeTurn{
		preciseTurn(sessionA, second.Add(100*time.Nanosecond), second.Add(200*time.Nanosecond)),
		preciseTurn(sessionB, second.Add(300*time.Nanosecond), second.Add(400*time.Nanosecond)),
	}
	if groups := concurrentTurnGroups(sequential); len(groups) != 0 {
		t.Fatalf("subsecond sequential groups = %#v", groups)
	}

	legacy := []completeTurn{legacyTurn(sessionA, time.Time{}, time.Time{}), legacyTurn(sessionB, time.Time{}, time.Time{})}
	if groups := concurrentTurnGroups(legacy); len(groups) != 1 || groups[0].Latest.SessionID != sessionB {
		t.Fatalf("same-second legacy groups = %#v, want latest endpoint from session B", groups)
	}

	// Different post seconds do not recover the unknown order of two coarse
	// pre snapshots captured in the same second.
	coarsePre := []completeTurn{
		legacyTurn(sessionA, time.Time{}, second.Add(2*time.Second)),
		legacyTurn(sessionB, time.Time{}, second.Add(time.Second)),
	}
	coarsePre[0].Post.Time = second.Add(2 * time.Second)
	coarsePre[1].Post.Time = second.Add(time.Second)
	groups := concurrentTurnGroups(coarsePre)
	if len(groups) != 1 || groups[0].OrderKnown {
		t.Fatalf("coarse pre-order groups = %#v, want unknown snapshot order", groups)
	}

	// A legacy event can be published well after its coarse pre snapshot. The
	// checkpoint second remains part of the possible interval, so this cannot
	// be treated as safely sequential.
	delayedEvents := []completeTurn{
		legacyTurn(sessionA, second.Add(800*time.Millisecond), second.Add(900*time.Millisecond)),
		legacyTurn(sessionB, second.Add(100*time.Millisecond), second.Add(200*time.Millisecond)),
	}
	if groups := concurrentTurnGroups(delayedEvents); len(groups) != 1 || groups[0].OrderKnown {
		t.Fatalf("delayed legacy event groups = %#v, want one conservatively unordered group", groups)
	}
}

func TestSortCompleteTurnsUsesLegacyEventEndBeforeSessionID(t *testing.T) {
	second := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	earlierSession := blameSessionID(t, "z-earlier")
	laterSession := blameSessionID(t, "a-later")
	turnID := blameTurnID(t, 1)
	turns := []completeTurn{
		{SessionID: laterSession, TurnID: turnID, Post: checkpoint.CheckpointRefInfo{Time: second}, Events: queryindex.TurnEventSummary{Last: second.Add(900 * time.Millisecond)}},
		{SessionID: earlierSession, TurnID: turnID, Post: checkpoint.CheckpointRefInfo{Time: second}, Events: queryindex.TurnEventSummary{Last: second.Add(300 * time.Millisecond)}},
	}
	sortCompleteTurns(turns)
	if turns[0].SessionID != earlierSession || turns[1].SessionID != laterSession {
		t.Fatalf("legacy event-end order = %s, %s; want %s, %s", turns[0].SessionID, turns[1].SessionID, earlierSession, laterSession)
	}
}

func TestConcurrentTurnsWithSameSessionAndDifferentStreamsStayDistinct(t *testing.T) {
	second := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	session := blameSessionID(t, "shared-session")
	turnID := blameTurnID(t, 1)
	makeTurn := func(stream primitives.EventStreamID, start, end time.Time) completeTurn {
		return completeTurn{
			SessionID: session,
			TurnID:    turnID,
			Pre: checkpoint.CheckpointRefInfo{
				SessionID: session, TurnID: turnID, StreamID: stream, Time: second,
			},
			Post:                checkpoint.CheckpointRefInfo{SessionID: session, TurnID: turnID, StreamID: stream, Time: second},
			PreEvent:            start,
			PostEvent:           end,
			PreEventPrecise:     true,
			PostEventPrecise:    true,
			HasCheckpointEvents: true,
		}
	}
	turns := []completeTurn{
		makeTurn("stream-a", second.Add(100*time.Nanosecond), second.Add(400*time.Nanosecond)),
		makeTurn("stream-b", second.Add(200*time.Nanosecond), second.Add(500*time.Nanosecond)),
	}
	groups := concurrentTurnGroups(turns)
	if len(groups) != 1 || len(groups[0].Sessions) != 2 || !groups[0].OrderKnown {
		t.Fatalf("same-session stream groups = %#v, want two distinct ordered participants", groups)
	}
}

func TestConcurrentTurnStartsAtPrecisePreSnapshotBoundary(t *testing.T) {
	second := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	sessionA := blameSessionID(t, "snapshot-gap-a")
	sessionB := blameSessionID(t, "snapshot-gap-b")
	turnID := blameTurnID(t, 1)
	makeTurn := func(session primitives.SessionID, pre, firstEvent, post time.Time) completeTurn {
		return completeTurn{
			SessionID:           session,
			TurnID:              turnID,
			Pre:                 checkpoint.CheckpointRefInfo{SessionID: session, TurnID: turnID, Time: second},
			Post:                checkpoint.CheckpointRefInfo{SessionID: session, TurnID: turnID, Time: second},
			Events:              queryindex.TurnEventSummary{First: firstEvent, Last: post.Add(time.Nanosecond)},
			PreEvent:            pre,
			PostEvent:           post,
			PreEventPrecise:     true,
			PostEventPrecise:    true,
			HasCheckpointEvents: true,
		}
	}

	turns := []completeTurn{
		makeTurn(sessionA, second.Add(100*time.Nanosecond), second.Add(500*time.Nanosecond), second.Add(900*time.Nanosecond)),
		// B completes after A's pre snapshot but before A's first published event.
		makeTurn(sessionB, second.Add(200*time.Nanosecond), second.Add(250*time.Nanosecond), second.Add(400*time.Nanosecond)),
	}
	if groups := concurrentTurnGroups(turns); len(groups) != 1 || len(groups[0].Members) != 2 || !groups[0].OrderKnown {
		t.Fatalf("snapshot-publication gap groups = %#v, want one safely ordered concurrent group", groups)
	}
}

func TestSortCompleteTurnsUsesDurableSnapshotBoundary(t *testing.T) {
	second := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	sessionA := blameSessionID(t, "post-order-a")
	sessionB := blameSessionID(t, "post-order-b")
	turnID := blameTurnID(t, 1)
	turns := []completeTurn{
		{SessionID: sessionB, TurnID: turnID, Post: checkpoint.CheckpointRefInfo{Time: second}, PostEvent: second.Add(300 * time.Nanosecond), PostEventPrecise: true},
		{SessionID: sessionA, TurnID: turnID, Post: checkpoint.CheckpointRefInfo{Time: second}, PostEvent: second.Add(200 * time.Nanosecond), PostEventPrecise: true},
	}
	sortCompleteTurns(turns)
	if turns[0].SessionID != sessionA || turns[1].SessionID != sessionB {
		t.Fatalf("snapshot order = %s, %s; want %s, %s", turns[0].SessionID, turns[1].SessionID, sessionA, sessionB)
	}
}

func TestCheckpointEventTimeUsesCapturedBoundary(t *testing.T) {
	capturedAt := time.Date(2026, 8, 3, 12, 0, 0, 123, time.UTC)
	deliveredAt := capturedAt.Add(5 * time.Second)
	timestamp, err := primitives.NewTimestamp(deliveredAt)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := primitives.ParseCommitSHA("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	records := []eventlog.Event{{
		Type:    primitives.EventTypeCheckpoint,
		Time:    timestamp,
		Payload: json.RawMessage(`{"phase":"pre","commit_sha":"` + commit.String() + `","captured_at":"` + capturedAt.Format(time.RFC3339Nano) + `"}`),
	}}
	got, found, precise := checkpointEventTime(records, checkpoint.CheckpointRefInfo{Phase: primitives.CheckpointPhasePre, Commit: commit})
	if !found || !precise || !got.Equal(capturedAt) {
		t.Fatalf("checkpoint boundary = %s, found=%v precise=%v; want %s", got, found, precise, capturedAt)
	}
}

func TestObserveHistoryTreatsUnpublishedPostCheckpointAsIncomplete(t *testing.T) {
	root, repo := newBlameRepo(t)
	writeBlameFile(t, root, "app.txt", "before\n")
	session := blameSessionID(t, "post-publication-gap")
	turnID := blameTurnID(t, 1)
	gitSync := false
	recorder := turnevents.Recorder{
		Log:     repo.EventLog(),
		Manager: turns.NewManager(repo),
		Adapter: primitives.AdapterCodex,
	}
	recorder.Manager.GitSyncEnabled = &gitSync
	if _, err := recorder.Start(session, turnID); err != nil {
		t.Fatalf("start turn: %v", err)
	}
	writeBlameFile(t, root, "app.txt", "after\n")
	manager := turns.NewManager(repo).WithCheckpointEvents(primitives.AdapterCodex, "")
	manager.GitSyncEnabled = &gitSync
	finished, err := manager.Finish(session, turnID)
	if err != nil {
		t.Fatalf("create unpublished post checkpoint: %v", err)
	}
	if _, err := repo.CheckpointCommit(finished.Post.Ref); err != nil {
		t.Fatalf("unpublished post ref does not resolve: %v", err)
	}

	history, err := New(repo).observeHistory("", "", 0)
	if err != nil {
		t.Fatalf("observe history: %v", err)
	}
	if len(history.Complete) != 0 || len(history.Incomplete) != 1 || history.Incomplete[0].SessionID != session {
		t.Fatalf("history = %#v, want one incomplete publication-gap turn", history)
	}
}

func TestComputeBlameScopesCanonicalStream(t *testing.T) {
	root, repo := newBlameRepo(t)
	repo.ScopedRefs = true
	sessionID := blameSessionID(t, "shared-session")

	captureBlameTurn(t, repo, root, sessionID, 1, "shared.txt", "from first stream\n", "first stream")
	firstStream, err := repo.StreamID(sessionID)
	if err != nil {
		t.Fatal(err)
	}

	secondRepo := *repo
	secondRepo.EventProducerID, err = primitives.NewEventProducerID()
	if err != nil {
		t.Fatal(err)
	}
	captureBlameTurn(t, &secondRepo, root, sessionID, 1, "shared.txt", "from second stream\n", "second stream")
	secondStream, err := secondRepo.StreamID(sessionID)
	if err != nil {
		t.Fatal(err)
	}

	path, _ := primitives.ParseRepoPath("shared.txt")
	first, err := New(repo).Compute(Query{Path: path, SessionID: sessionID, StreamID: firstStream})
	if err != nil {
		t.Fatalf("first stream Compute: %v", err)
	}
	second, err := New(repo).Compute(Query{Path: path, SessionID: sessionID, StreamID: secondStream})
	if err != nil {
		t.Fatalf("second stream Compute: %v", err)
	}
	if first.Entries[0].Text != "from first stream" {
		t.Fatalf("first stream blame = %#v", first.Entries[0])
	}
	if second.Entries[0].Text != "from second stream" {
		t.Fatalf("second stream blame = %#v", second.Entries[0])
	}
}

func TestComputeBlameStopsAtSelectedTurn(t *testing.T) {
	root, repo := newBlameRepo(t)
	sessionID := blameSessionID(t, "bounded-session")
	captureBlameTurn(t, repo, root, sessionID, 1, "app.txt", "version one\n", "first version")
	captureBlameTurn(t, repo, root, sessionID, 2, "app.txt", "version two\n", "second version")

	streamID, err := repo.StreamID(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	turnID := blameTurnID(t, 1)
	path, _ := primitives.ParseRepoPath("app.txt")
	result, err := New(repo).Compute(Query{
		Path: path, SessionID: sessionID, StreamID: streamID, ThroughTurnID: turnID,
	})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if result.CompleteTurns != 1 || result.Entries[0].Text != "version one" || result.LatestCommit == "" {
		t.Fatalf("bounded blame = %#v", result)
	}
}

func TestComputeBlameFileDeletedAtLatest(t *testing.T) {
	root, repo := newBlameRepo(t)
	sessionID := blameSessionID(t, "demo")
	captureBlameTurn(t, repo, root, sessionID, 1, "gone.txt", "temporary\n", "add file")

	turn2 := blameTurnID(t, 2)
	if _, err := repo.CreateCheckpoint(sessionID, turn2, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("turn 2 pre: %v", err)
	}
	if err := os.Remove(filepath.Join(root.String(), "gone.txt")); err != nil {
		t.Fatalf("remove gone.txt: %v", err)
	}
	if _, err := repo.CreateCheckpoint(sessionID, turn2, primitives.CheckpointPhasePost); err != nil {
		t.Fatalf("turn 2 post: %v", err)
	}

	path, _ := primitives.ParseRepoPath("gone.txt")
	_, err := New(repo).Compute(Query{Path: path, Line: 1})
	if !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("Compute error = %v, want ErrFileNotFound", err)
	}
}

func TestComputeBlameMovedLinePreservesPriorOrigin(t *testing.T) {
	root, repo := newBlameRepo(t)
	sessionID := blameSessionID(t, "demo")

	captureBlameTurn(t, repo, root, sessionID, 1, "move.txt", "one\ntwo\nthree\n", "seed file")
	captureBlameTurn(t, repo, root, sessionID, 2, "move.txt", "two\none\nthree\n", "swap first two")

	path, _ := primitives.ParseRepoPath("move.txt")
	result, err := New(repo).Compute(Query{Path: path, Line: 2})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	entry := result.Entries[0]
	if entry.Text != "one" {
		t.Fatalf("line 2 text = %q, want one", entry.Text)
	}
	if entry.Origin.Kind != "turn" || entry.Origin.TurnID.Uint64() != 1 {
		t.Fatalf("moved unchanged line origin = %#v, want original write turn", entry.Origin)
	}
}

func TestComputeBlameRejectsBinaryFile(t *testing.T) {
	root, repo := newBlameRepo(t)
	sessionID := blameSessionID(t, "demo")
	turnID := blameTurnID(t, 1)

	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("pre checkpoint: %v", err)
	}
	writeBlameBytes(t, root, "raw.bin", []byte{'a', 0, 'b'})
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost); err != nil {
		t.Fatalf("post checkpoint: %v", err)
	}

	path, _ := primitives.ParseRepoPath("raw.bin")
	_, err := New(repo).Compute(Query{Path: path, Line: 1})
	if !errors.Is(err, ErrBinaryFile) {
		t.Fatalf("Compute error = %v, want ErrBinaryFile", err)
	}
}

func TestComputeBlameLineNumberEdges(t *testing.T) {
	root, repo := newBlameRepo(t)
	sessionID := blameSessionID(t, "demo")
	captureBlameTurn(t, repo, root, sessionID, 1, "single.txt", "hello", "write single")

	path, _ := primitives.ParseRepoPath("single.txt")
	result, err := New(repo).Compute(Query{Path: path, Line: 1})
	if err != nil {
		t.Fatalf("Compute line 1: %v", err)
	}
	if result.Entries[0].Text != "hello" {
		t.Fatalf("line 1 text = %q, want hello", result.Entries[0].Text)
	}

	_, err = New(repo).Compute(Query{Path: path, Line: 2})
	if !errors.Is(err, ErrLineNotFound) {
		t.Fatalf("line 2 error = %v, want ErrLineNotFound", err)
	}

	if _, _, err := ParsePathLine("single.txt:0"); !errors.Is(err, ErrInvalidLine) {
		t.Fatalf("ParsePathLine line 0 error = %v, want ErrInvalidLine", err)
	}
}

func TestComputeBlameEmptyFileHasNoEntries(t *testing.T) {
	root, repo := newBlameRepo(t)
	sessionID := blameSessionID(t, "demo")
	captureBlameTurn(t, repo, root, sessionID, 1, "empty.txt", "", "write empty")

	path, _ := primitives.ParseRepoPath("empty.txt")
	result, err := New(repo).Compute(Query{Path: path})
	if err != nil {
		t.Fatalf("Compute all: %v", err)
	}
	if len(result.Entries) != 0 {
		t.Fatalf("entries len = %d, want 0", len(result.Entries))
	}

	_, err = New(repo).Compute(Query{Path: path, Line: 1})
	if !errors.Is(err, ErrLineNotFound) {
		t.Fatalf("line 1 error = %v, want ErrLineNotFound", err)
	}
}

func TestComputeBlameUsesSQLiteCacheAndInvalidatesOnHistoryChange(t *testing.T) {
	root, repo := newBlameRepo(t)
	sessionID := blameSessionID(t, "demo")
	captureBlameTurn(t, repo, root, sessionID, 1, "cache.txt", "first\nsecond\n", "cache fill")

	if _, err := queryindex.Rebuild(repo); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	path, _ := primitives.ParseRepoPath("cache.txt")
	first, err := New(repo).Compute(Query{Path: path, Line: 1})
	if err != nil {
		t.Fatalf("initial Compute: %v", err)
	}
	if first.Entries[0].Text != "first" {
		t.Fatalf("initial line text = %q, want first", first.Entries[0].Text)
	}

	db, err := sql.Open("sqlite", queryindex.PathsForMetadata(repo.MetadataDir).DBPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM blame_cache WHERE path = ?`, "cache.txt").Scan(&rows); err != nil {
		t.Fatalf("count blame cache: %v", err)
	}
	if rows != 3 {
		t.Fatalf("blame cache rows = %d, want sentinel + 2 lines", rows)
	}
	if _, err := db.Exec(`UPDATE blame_cache SET line_text = ?, origin_prompt = ?, origin_action_agent_id = ?, origin_action_agent_type = ? WHERE path = ? AND line_no = 1`, "from cache", "stale prompt", "agent-a", "worker", "cache.txt"); err != nil {
		t.Fatalf("mutate blame cache: %v", err)
	}

	cached, err := New(repo).Compute(Query{Path: path, Line: 1})
	if err != nil {
		t.Fatalf("cached Compute: %v", err)
	}
	if cached.Entries[0].Text != "from cache" {
		t.Fatalf("cached line text = %q, want from cache", cached.Entries[0].Text)
	}
	if cached.Entries[0].Origin.Prompt != "cache fill" {
		t.Fatalf("cached origin prompt = %q, want current event summary", cached.Entries[0].Origin.Prompt)
	}
	if cached.Entries[0].Origin.ActionAgentID != "agent-a" || cached.Entries[0].Origin.ActionAgentType != "worker" {
		t.Fatalf("cached action agent = %#v", cached.Entries[0].Origin)
	}

	captureBlameTurn(t, repo, root, sessionID, 2, "cache.txt", "fresh\nsecond\n", "new history")
	updated, err := New(repo).Compute(Query{Path: path, Line: 1})
	if err != nil {
		t.Fatalf("updated Compute: %v", err)
	}
	if updated.Entries[0].Text != "fresh" {
		t.Fatalf("updated line text = %q, want fresh after history change", updated.Entries[0].Text)
	}
	if updated.Entries[0].Origin.TurnID.Uint64() != 2 {
		t.Fatalf("updated origin = %#v, want turn 2", updated.Entries[0].Origin)
	}
}

func TestComputeBlameCacheRejectsMissingActionEvidence(t *testing.T) {
	root, repo := newBlameRepo(t)
	sessionID := blameSessionID(t, "cache-action-evidence")
	turnID := blameTurnID(t, 1)
	writeBlameFile(t, root, "app.txt", "before\n")
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("turn pre: %v", err)
	}
	intent, err := repo.EventLog().Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypeAgentIntent,
		Adapter:   primitives.AdapterCodex,
		Payload: mustBlameJSON(t, provenance.IntentPayload{
			Problem: "the value is stale",
			Scope:   []string{"app.txt"},
		}),
	})
	if err != nil {
		t.Fatalf("append intent: %v", err)
	}
	actionPre, err := repo.CreateSnapshotRef("refs/agent-vcs/actions/cache-action-evidence/pre", "action pre")
	if err != nil {
		t.Fatalf("action pre: %v", err)
	}
	if _, err := repo.EventLog().Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypeToolCall,
		Adapter:   primitives.AdapterCodex,
		Payload: mustBlameJSON(t, map[string]any{
			"tool_name": "apply_patch", "tool_use_id": "edit-1", "mutation_candidate": true,
			"pre_snapshot":     map[string]any{"ref": actionPre.Ref, "commit": actionPre.Commit},
			"intent_event_seq": intent.Seq,
		}),
	}); err != nil {
		t.Fatalf("append tool call: %v", err)
	}
	writeBlameFile(t, root, "app.txt", "after\n")
	actionPost, err := repo.CreateSnapshotRef("refs/agent-vcs/actions/cache-action-evidence/post", "action post")
	if err != nil {
		t.Fatalf("action post: %v", err)
	}
	if _, err := repo.EventLog().Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypeToolResult,
		Adapter:   primitives.AdapterCodex,
		Payload: mustBlameJSON(t, map[string]any{
			"tool_name": "apply_patch", "tool_use_id": "edit-1",
			"post_snapshot": map[string]any{"ref": actionPost.Ref, "commit": actionPost.Commit},
		}),
	}); err != nil {
		t.Fatalf("append tool result: %v", err)
	}
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost); err != nil {
		t.Fatalf("turn post: %v", err)
	}
	appendBlamePrompt(t, repo, sessionID, turnID, "fix app")
	if _, err := queryindex.Rebuild(repo); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	path, _ := primitives.ParseRepoPath("app.txt")
	warm, err := New(repo).Compute(Query{Path: path, Line: 1})
	if err != nil {
		t.Fatalf("warm Compute: %v", err)
	}
	if warm.Entries[0].Origin.Intent == nil || warm.Entries[0].Origin.Intent.Confidence != provenance.IntentConfidenceHigh {
		t.Fatalf("warm origin = %#v, want high-confidence action intent", warm.Entries[0].Origin)
	}
	// Simulate partial hidden-repository loss without changing the durable ref
	// namespace or event log. The query index therefore remains fresh, but its
	// disposable cache must not outrank the missing action commit.
	commitText := actionPre.Commit.String()
	objectPath := filepath.Join(repo.GitDir, "objects", commitText[:2], commitText[2:])
	if _, err := os.Stat(objectPath); err != nil {
		t.Fatalf("loose action commit object: %v", err)
	}
	if err := os.Remove(objectPath); err != nil {
		t.Fatalf("remove action commit object: %v", err)
	}
	if err := repo.ValidateCommit(actionPre.Commit); err == nil {
		t.Fatalf("action pre commit %s survived object removal", actionPre.Commit)
	}

	if _, err := New(repo).Compute(Query{Path: path, Line: 1}); err == nil || !strings.Contains(err.Error(), "validate cached action snapshot") {
		t.Fatalf("cached Compute error = %v, want missing durable action evidence", err)
	}
}

func TestComputeBlameCacheRejectsMissingConcurrentBaselineEvidence(t *testing.T) {
	root, repo := newBlameRepo(t)
	writeBlameFile(t, root, "app.txt", "zero\n")
	sessionA := blameSessionID(t, "cache-open-baseline")
	sessionB := blameSessionID(t, "cache-complete-turn")
	turnID := blameTurnID(t, 1)
	gitSync := false
	manager := turns.NewManager(repo)
	manager.GitSyncEnabled = &gitSync
	recorder := turnevents.Recorder{Log: repo.EventLog(), Manager: manager, Adapter: primitives.AdapterCodex}

	openTurn, err := recorder.Start(sessionA, turnID)
	if err != nil {
		t.Fatalf("start open turn: %v", err)
	}
	writeBlameFile(t, root, "app.txt", "one\n")
	if _, err := recorder.Start(sessionB, turnID); err != nil {
		t.Fatalf("start complete turn: %v", err)
	}
	appendBlamePrompt(t, repo, sessionB, turnID, "write two")
	writeBlameFile(t, root, "app.txt", "two\n")
	if _, err := recorder.Finish(sessionB, turnID); err != nil {
		t.Fatalf("finish complete turn: %v", err)
	}
	if _, err := queryindex.Rebuild(repo); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	path, _ := primitives.ParseRepoPath("app.txt")
	engine := New(repo)
	streamB, err := repo.StreamID(sessionB)
	if err != nil {
		t.Fatalf("stream for complete turn: %v", err)
	}
	warm, err := engine.Compute(Query{Path: path, Line: 1, SessionID: sessionB, StreamID: streamB})
	if err != nil {
		t.Fatalf("warm Compute: %v", err)
	}
	if warm.Entries[0].Origin.Kind != "concurrent" {
		t.Fatalf("warm origin = %#v, want concurrent attribution", warm.Entries[0].Origin)
	}

	history, err := engine.observeHistory("", "", 0)
	if err != nil {
		t.Fatalf("observe history: %v", err)
	}
	if len(history.Complete) != 1 || len(history.Incomplete) != 1 {
		t.Fatalf("history = %#v, want one complete and one incomplete turn", history)
	}
	concurrent := concurrentTurnAttributions(history.Complete, history.Incomplete)
	if fact := concurrent[completeTurnIdentity(history.Complete[0])]; fact.Baseline == nil || fact.Baseline.Commit != openTurn.Pre.Commit {
		t.Fatalf("concurrent fact = %#v, want open-turn pre checkpoint baseline", fact)
	}

	commitText := openTurn.Pre.Commit.String()
	objectPath := filepath.Join(repo.GitDir, "objects", commitText[:2], commitText[2:])
	if _, err := os.Stat(objectPath); err != nil {
		t.Fatalf("loose concurrent baseline commit object: %v", err)
	}
	if err := os.Remove(objectPath); err != nil {
		t.Fatalf("remove concurrent baseline commit object: %v", err)
	}
	if err := engine.validateCachedEvidence(history.Complete, concurrent); err == nil || !strings.Contains(err.Error(), "concurrent baseline checkpoint") {
		t.Fatalf("cached evidence validation error = %v, want missing concurrent baseline evidence", err)
	}
	if _, err := engine.Compute(Query{Path: path, Line: 1, SessionID: sessionB, StreamID: streamB}); err == nil {
		t.Fatal("cached Compute succeeded after concurrent baseline evidence was removed")
	}
}

func TestReadOnlyComputeDoesNotWriteBlameCache(t *testing.T) {
	root, repo := newBlameRepo(t)
	writeBlameFile(t, root, "readonly.txt", "before\n")
	sessionID := blameSessionID(t, "read-only-cache")
	captureBlameTurn(t, repo, root, sessionID, 1, "readonly.txt", "after\n", "change the file")
	if _, err := queryindex.Rebuild(repo); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	path, _ := primitives.ParseRepoPath("readonly.txt")
	if _, err := (Engine{Repo: repo, ReadOnly: true}).Compute(Query{Path: path, Line: 1}); err != nil {
		t.Fatalf("read-only Compute: %v", err)
	}

	db, err := sql.Open("sqlite", queryindex.PathsForMetadata(repo.MetadataDir).DBPath)
	if err != nil {
		t.Fatalf("open query index: %v", err)
	}
	defer db.Close()
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM blame_cache WHERE path = ?`, path.String()).Scan(&rows); err != nil {
		t.Fatalf("count blame cache rows: %v", err)
	}
	if rows != 0 {
		t.Fatalf("read-only Compute wrote %d blame cache rows", rows)
	}
}

func TestParseAndApplyUnifiedHunks(t *testing.T) {
	patch := []byte(`diff --git a/app.txt b/app.txt
--- a/app.txt
+++ b/app.txt
@@ -1,2 +1,3 @@
-old one
-old two
+new one
+new two
+new three
`)
	hunks, err := parseUnifiedHunks(patch)
	if err != nil {
		t.Fatalf("parseUnifiedHunks: %v", err)
	}
	if len(hunks) != 1 || hunks[0].OldStart != 1 || hunks[0].OldCount != 2 || hunks[0].NewStart != 1 || hunks[0].NewCount != 3 {
		t.Fatalf("hunks = %#v, want one 2-line replacement with 3 new lines", hunks)
	}

	origin := Origin{Kind: "turn"}
	applied, err := applyHunks([]Origin{{Kind: "baseline"}, {Kind: "baseline"}}, hunks, origin)
	if err != nil {
		t.Fatalf("applyHunks: %v", err)
	}
	if len(applied) != 3 {
		t.Fatalf("applied len = %d, want 3", len(applied))
	}
	for index, got := range applied {
		if got.Kind != "turn" {
			t.Fatalf("origin %d = %#v, want turn", index, got)
		}
	}
}

func TestApplyUnifiedHunksAppendPreservesExistingOrigins(t *testing.T) {
	patch := []byte(`diff --git a/app.txt b/app.txt
--- a/app.txt
+++ b/app.txt
@@ -2,0 +3 @@ beta
+gamma
`)
	hunks, err := parseUnifiedHunks(patch)
	if err != nil {
		t.Fatalf("parseUnifiedHunks: %v", err)
	}

	applied, err := applyHunks([]Origin{{Kind: "first"}, {Kind: "second"}}, hunks, Origin{Kind: "turn"})
	if err != nil {
		t.Fatalf("applyHunks: %v", err)
	}
	gotKinds := []string{applied[0].Kind, applied[1].Kind, applied[2].Kind}
	wantKinds := []string{"first", "second", "turn"}
	for index := range wantKinds {
		if gotKinds[index] != wantKinds[index] {
			t.Fatalf("origin kinds = %#v, want %#v", gotKinds, wantKinds)
		}
	}
}

func TestApplyUnifiedHunksPreservesContextOrigins(t *testing.T) {
	patch := []byte(`diff --git a/app.txt b/app.txt
--- a/app.txt
+++ b/app.txt
@@ -1,3 +1,3 @@
 keep
-old
+new
 keep too
`)
	hunks, err := parseUnifiedHunks(patch)
	if err != nil {
		t.Fatalf("parseUnifiedHunks: %v", err)
	}
	applied, err := applyHunks([]Origin{{Kind: "first"}, {Kind: "second"}, {Kind: "third"}}, hunks, Origin{Kind: "turn"})
	if err != nil {
		t.Fatalf("applyHunks: %v", err)
	}
	gotKinds := []string{applied[0].Kind, applied[1].Kind, applied[2].Kind}
	wantKinds := []string{"first", "turn", "third"}
	for index := range wantKinds {
		if gotKinds[index] != wantKinds[index] {
			t.Fatalf("origin kinds = %#v, want %#v", gotKinds, wantKinds)
		}
	}
}

func newBlameRepo(t *testing.T) (primitives.WorkspaceRoot, *checkpoint.Repo) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable not found")
	}
	root, err := primitives.ParseWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot: %v", err)
	}
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	return root, repo
}

func captureBlameTurn(t *testing.T, repo *checkpoint.Repo, root primitives.WorkspaceRoot, sessionID primitives.SessionID, turn uint64, path string, postContent string, prompt string) {
	t.Helper()
	turnID := blameTurnID(t, turn)
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("turn %d pre: %v", turn, err)
	}
	writeBlameFile(t, root, path, postContent)
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost); err != nil {
		t.Fatalf("turn %d post: %v", turn, err)
	}
	appendBlamePrompt(t, repo, sessionID, turnID, prompt)
}

func captureRecordedBlameTurn(t *testing.T, repo *checkpoint.Repo, root primitives.WorkspaceRoot, sessionID primitives.SessionID, turn uint64, path string, postContent string, prompt string) {
	t.Helper()
	turnID := blameTurnID(t, turn)
	gitSync := false
	manager := turns.NewManager(repo)
	manager.GitSyncEnabled = &gitSync
	recorder := turnevents.Recorder{Log: repo.EventLog(), Manager: manager, Adapter: primitives.AdapterCodex}
	if _, err := recorder.Start(sessionID, turnID); err != nil {
		t.Fatalf("turn %d start: %v", turn, err)
	}
	appendBlamePrompt(t, repo, sessionID, turnID, prompt)
	writeBlameFile(t, root, path, postContent)
	if _, err := recorder.Finish(sessionID, turnID); err != nil {
		t.Fatalf("turn %d finish: %v", turn, err)
	}
}

func appendBlamePrompt(t *testing.T, repo *checkpoint.Repo, sessionID primitives.SessionID, turnID primitives.TurnID, prompt string) {
	t.Helper()
	if _, err := eventlog.Open(repo.MetadataDir).Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypePromptUser,
		Adapter:   primitives.AdapterCodex,
		Payload:   mustBlameJSON(t, map[string]string{"text": prompt}),
	}); err != nil {
		t.Fatalf("append prompt: %v", err)
	}
}

func mustBlameJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return data
}

func blameSessionID(t *testing.T, value string) primitives.SessionID {
	t.Helper()
	sessionID, err := primitives.ParseSessionID(value)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	return sessionID
}

func blameTurnID(t *testing.T, value uint64) primitives.TurnID {
	t.Helper()
	turnID, err := primitives.NewTurnID(value)
	if err != nil {
		t.Fatalf("NewTurnID: %v", err)
	}
	return turnID
}

func writeBlameFile(t *testing.T, root primitives.WorkspaceRoot, relPath string, content string) {
	t.Helper()
	writeBlameBytes(t, root, relPath, []byte(content))
}

func writeBlameBytes(t *testing.T, root primitives.WorkspaceRoot, relPath string, content []byte) {
	t.Helper()
	path := filepath.Join(root.String(), filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
}
