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
	if patch.Patch == "" || patch.Truncated {
		t.Fatalf("patch = %#v", patch)
	}
	origins, err := service.Blame(ctx, turnKey, "auth.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(origins.Lines) == 0 || origins.Lines[3].TurnID != started.TurnID.Uint64() {
		t.Fatalf("blame = %#v", origins)
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
