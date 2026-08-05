package viewer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/filelock"
	"github.com/AadiJo/turnal/internal/manualcheckpoints"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/turnevents"
	"github.com/AadiJo/turnal/internal/turns"
)

func TestServiceTraversesTurnDiffAndBlameWithoutWritingHistory(t *testing.T) {
	repo := newViewerTestRepo(t)
	path := filepath.Join(repo.WorkspaceRoot.String(), "auth.go")
	if err := os.WriteFile(path, []byte("package auth\n\nfunc Allowed() bool {\n\treturn false\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessionID, err := primitives.ParseSessionID("secure-local-viewer")
	if err != nil {
		t.Fatal(err)
	}
	recorder := turnevents.Recorder{
		Log: repo.EventLog(), Manager: turns.NewManager(repo), Adapter: primitives.AdapterManual,
	}
	started, err := recorder.Start(sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	appendViewerEvent(t, repo.EventLog(), sessionID, started.TurnID, primitives.EventTypePromptUser, map[string]any{
		"text": "Make the local viewer reject requests without a scoped session.",
	})
	appendViewerEvent(t, repo.EventLog(), sessionID, started.TurnID, primitives.EventTypeToolCall, map[string]any{
		"tool_name": "apply_patch", "path": "auth.go",
	})
	if err := os.WriteFile(path, []byte("package auth\n\nfunc Allowed(scoped bool) bool {\n\treturn scoped\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	appendViewerEvent(t, repo.EventLog(), sessionID, started.TurnID, primitives.EventTypeAssistantMessage, map[string]any{
		"text": "Added an explicit scoped-session boundary to the local viewer.",
	})
	if _, err := recorder.Finish(sessionID, started.TurnID); err != nil {
		t.Fatal(err)
	}

	service, err := NewService(repo)
	if err != nil {
		t.Fatal(err)
	}
	// Query-only viewer operations must not need scratch files in the store.
	if err := os.RemoveAll(repo.TmpDir); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sessions, err := service.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].TurnCount != 1 || sessions[0].FileCount != 1 {
		t.Fatalf("sessions = %#v", sessions)
	}
	turnList, err := service.SessionTurns(ctx, sessions[0].Key)
	if err != nil {
		t.Fatal(err)
	}
	if len(turnList.Turns) != 1 || turnList.Turns[0].Prompt == "" || !turnList.Turns[0].Checkpointed {
		t.Fatalf("turns = %#v", turnList.Turns)
	}
	turnKey := turnList.Turns[0].Key
	detail, err := service.Turn(ctx, turnKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Events) < 5 || detail.SessionID != sessionID.String() {
		t.Fatalf("turn detail = %#v", detail)
	}
	diff, err := service.Diff(ctx, turnKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Files) != 1 || diff.Files[0].Path != "auth.go" {
		t.Fatalf("diff = %#v", diff)
	}
	patch, err := service.Patch(ctx, turnKey, "auth.go")
	if err != nil {
		t.Fatal(err)
	}
	if patch.Patch == "" || patch.Truncated || patch.LimitBytes != maxPatchBytes || patch.LimitLines != maxPatchLines {
		t.Fatalf("patch = %#v", patch)
	}
	origins, err := service.Blame(ctx, turnKey, "auth.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(origins.Lines) == 0 || origins.Lines[3].TurnID != started.TurnID.Uint64() {
		t.Fatalf("blame = %#v", origins)
	}
	missingTurnID, err := primitives.NewTurnID(started.TurnID.Uint64() + 1)
	if err != nil {
		t.Fatal(err)
	}
	turnIdentity, err := service.codec.decode(turnKey, resourceTurn)
	if err != nil {
		t.Fatal(err)
	}
	streamID, err := primitives.ParseEventStreamID(turnIdentity.StreamID)
	if err != nil {
		t.Fatal(err)
	}
	missingTurnKey, err := service.codec.encode(resourceTurn, repo.WorktreeID, streamID, sessionID, missingTurnID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Blame(ctx, missingTurnKey, "auth.go", 0); err == nil {
		t.Fatal("blame accepted a canonical key for a turn that does not exist")
	}
	if _, err := os.Stat(repo.TmpDir); !os.IsNotExist(err) {
		t.Fatalf("read-only viewer recreated scratch directory %s: %v", repo.TmpDir, err)
	}
}

func TestServiceDoesNotTurnManualSaveIntoSession(t *testing.T) {
	repo := newViewerTestRepo(t)
	created, err := repo.CreateManualCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manualcheckpoints.Append(repo, created, "before refactor"); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(repo)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := service.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("manual save created viewer sessions: %#v", sessions)
	}
	workspace, err := service.Workspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if workspace.SessionCount != 0 || workspace.TurnCount != 0 {
		t.Fatalf("manual save changed workspace session counts: %#v", workspace)
	}
	saves, err := service.Saves(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(saves) != 1 || saves[0].Message != "before refactor" {
		t.Fatalf("saves = %#v", saves)
	}
}

func TestServiceListsManualSavesFromLinkedWorktrees(t *testing.T) {
	repo := newViewerTestRepo(t)
	repo.ScopedRefs = true
	linked := *repo
	var err error
	linked.WorktreeID, err = primitives.NewWorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	linked.EventProducerID, err = primitives.NewEventProducerID()
	if err != nil {
		t.Fatal(err)
	}
	linked.ScopedRefs = true
	created, err := linked.CreateManualCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manualcheckpoints.Append(&linked, created, "saved from linked folder"); err != nil {
		t.Fatal(err)
	}

	service, err := NewService(repo)
	if err != nil {
		t.Fatal(err)
	}
	saves, err := service.Saves(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(saves) != 1 || saves[0].Message != "saved from linked folder" {
		t.Fatalf("linked saves = %#v", saves)
	}
}

func TestServiceRetriesTransientPartialTailWhileWriterLockIsHeld(t *testing.T) {
	repo := newViewerTestRepo(t)
	sessionID, err := primitives.ParseSessionID("live-append")
	if err != nil {
		t.Fatal(err)
	}
	recorder := turnevents.Recorder{Log: repo.EventLog(), Manager: turns.NewManager(repo), Adapter: primitives.AdapterManual}
	started, err := recorder.Start(sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.Finish(sessionID, started.TurnID); err != nil {
		t.Fatal(err)
	}
	streamID, err := repo.StreamID(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	streamPath := eventlog.StreamPath(repo.MetadataDir, sessionID, streamID)
	data, err := os.ReadFile(streamPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatal("fixture event stream has no complete tail")
	}
	lock, err := filelock.Acquire(streamPath+".lock", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(streamPath, data[:len(data)-1], 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		time.Sleep(55 * time.Millisecond)
		file, openErr := os.OpenFile(streamPath, os.O_APPEND|os.O_WRONLY, 0)
		if openErr == nil {
			_, openErr = file.Write([]byte{'\n'})
			_ = file.Close()
		}
		if releaseErr := lock.Release(); openErr == nil {
			openErr = releaseErr
		}
		done <- openErr
	}()

	service, err := NewService(repo)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sessions, err := service.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != sessionID.String() {
		t.Fatalf("sessions after live append retry = %#v", sessions)
	}
}

func TestServiceBlameStopsAtCanonicalSelectedTurn(t *testing.T) {
	repo := newViewerTestRepo(t)
	path := filepath.Join(repo.WorkspaceRoot.String(), "version.txt")
	if err := os.WriteFile(path, []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessionID, err := primitives.ParseSessionID("bounded-viewer-blame")
	if err != nil {
		t.Fatal(err)
	}
	recorder := turnevents.Recorder{Log: repo.EventLog(), Manager: turns.NewManager(repo), Adapter: primitives.AdapterManual}
	for _, content := range []string{"version one\n", "version two\n"} {
		started, startErr := recorder.Start(sessionID, 0)
		if startErr != nil {
			t.Fatal(startErr)
		}
		if writeErr := os.WriteFile(path, []byte(content), 0o644); writeErr != nil {
			t.Fatal(writeErr)
		}
		if _, finishErr := recorder.Finish(sessionID, started.TurnID); finishErr != nil {
			t.Fatal(finishErr)
		}
	}

	service, err := NewService(repo)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := service.Sessions(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions = %#v, %v", sessions, err)
	}
	turnList, err := service.SessionTurns(context.Background(), sessions[0].Key)
	if err != nil {
		t.Fatal(err)
	}
	var firstTurnKey string
	for _, turn := range turnList.Turns {
		if turn.ID == 1 {
			firstTurnKey = turn.Key
		}
	}
	if firstTurnKey == "" {
		t.Fatalf("turns = %#v", turnList.Turns)
	}
	origins, err := service.Blame(context.Background(), firstTurnKey, "version.txt", 0)
	if err != nil {
		t.Fatal(err)
	}
	if origins.CompleteTurns != 1 || len(origins.Lines) != 1 || origins.Lines[0].Text != "version one" {
		t.Fatalf("bounded origins = %#v", origins)
	}
}

func TestServiceBlameUsesTheSelectedLinkedWorktree(t *testing.T) {
	repo := newViewerTestRepo(t)
	repo.ScopedRefs = true
	linked := *repo
	var err error
	linked.WorktreeID, err = primitives.NewWorktreeID()
	if err != nil {
		t.Fatal(err)
	}
	linked.EventProducerID, err = primitives.NewEventProducerID()
	if err != nil {
		t.Fatal(err)
	}
	linked.ScopedRefs = true

	path := filepath.Join(linked.WorkspaceRoot.String(), "linked.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessionID, err := primitives.ParseSessionID("linked-worktree")
	if err != nil {
		t.Fatal(err)
	}
	recorder := turnevents.Recorder{Log: linked.EventLog(), Manager: turns.NewManager(&linked), Adapter: primitives.AdapterManual}
	started, err := recorder.Start(sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("from linked worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.Finish(sessionID, started.TurnID); err != nil {
		t.Fatal(err)
	}

	service, err := NewService(repo)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := service.Sessions(context.Background())
	if err != nil || len(sessions) != 1 || sessions[0].WorktreeID != linked.WorktreeID.String() {
		t.Fatalf("linked sessions = %#v, %v", sessions, err)
	}
	turnList, err := service.SessionTurns(context.Background(), sessions[0].Key)
	if err != nil || len(turnList.Turns) != 1 {
		t.Fatalf("linked turns = %#v, %v", turnList, err)
	}
	origins, err := service.Blame(context.Background(), turnList.Turns[0].Key, "linked.txt", 0)
	if err != nil {
		t.Fatalf("linked worktree blame: %v", err)
	}
	if len(origins.Lines) != 1 || origins.Lines[0].Text != "from linked worktree" {
		t.Fatalf("linked worktree origins = %#v", origins)
	}
}

func appendViewerEvent(t *testing.T, log eventlog.Log, sessionID primitives.SessionID, turnID primitives.TurnID, eventType primitives.EventType, payload any) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: sessionID, TurnID: &turnID, Type: eventType, Adapter: primitives.AdapterManual,
		Time: primitives.NowTimestamp(), Payload: data,
	}); err != nil {
		t.Fatal(err)
	}
}
