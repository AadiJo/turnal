package viewer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
)

func TestScopedRecordsReuseUnchangedHistoryAndInvalidateAppends(t *testing.T) {
	repo := newViewerTestRepo(t)
	session, _ := primitives.ParseSessionID("scoped-records")
	turn, _ := primitives.NewTurnID(1)
	log := repo.EventLog()
	appendViewerEvent(t, log, session, turn, primitives.EventTypePromptUser, map[string]any{"text": "first prompt"})
	service, err := NewService(repo)
	if err != nil {
		t.Fatal(err)
	}
	stream, _ := primitives.DeriveEventStreamID(repo.EventProducerID, session)
	key, err := service.codec.encode(resourceSession, repo.WorktreeID, stream, session, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := service.SessionTurns(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Turns) != 1 || first.Turns[0].EventCount != 1 {
		t.Fatal(first)
	}
	if _, err := service.SessionTurns(ctx, key); err != nil {
		t.Fatal(err)
	}
	appendViewerEvent(t, log, session, turn, primitives.EventTypeAssistantMessage, map[string]any{"text": "response"})
	updated, err := service.SessionTurns(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Turns[0].EventCount != 2 || updated.Turns[0].Assistant != "response" {
		t.Fatal(updated)
	}
	// Damage an unrelated stream. A scoped read must not load that session.
	other, _ := primitives.ParseSessionID("unrelated")
	otherStream, _ := primitives.DeriveEventStreamID(repo.EventProducerID, other)
	otherPath := eventlog.StreamPath(repo.MetadataDir, other, otherStream)
	if err := os.MkdirAll(filepath.Dir(otherPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherPath, []byte("broken\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Force a cache miss in the selected session as well.
	appendViewerEvent(t, log, session, turn, primitives.EventTypeAssistantMessage, map[string]any{"text": "final"})
	if _, err := service.SessionTurns(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(eventlog.StreamPath(repo.MetadataDir, session, stream)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SessionTurns(ctx, key); err == nil {
		t.Fatal("returned removed history from cache")
	}
}

func TestScopedRecordsInvalidateWhenCheckpointRefsChange(t *testing.T) {
	repo := newViewerTestRepo(t)
	session, _ := primitives.ParseSessionID("ref-cache")
	turn, _ := primitives.NewTurnID(1)
	appendViewerEvent(t, repo.EventLog(), session, turn, primitives.EventTypePromptUser, map[string]any{"text": "change file"})
	service, err := NewService(repo)
	if err != nil {
		t.Fatal(err)
	}
	stream, _ := primitives.DeriveEventStreamID(repo.EventProducerID, session)
	key, err := service.codec.encode(resourceSession, repo.WorktreeID, stream, session, 0)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := service.SessionTurns(context.Background(), key)
	if err != nil || initial.Turns[0].Checkpointed {
		t.Fatalf("initial=%v err=%v", initial, err)
	}
	if _, err := repo.CreateCheckpoint(session, turn, primitives.CheckpointPhasePre); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo.WorkspaceRoot.String(), "changed.txt"), []byte("new\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateCheckpoint(session, turn, primitives.CheckpointPhasePost); err != nil {
		t.Fatal(err)
	}
	updated, err := service.SessionTurns(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Turns[0].Checkpointed || updated.Turns[0].Additions != 1 {
		t.Fatal(updated)
	}
}
