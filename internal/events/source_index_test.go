package events

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/primitives"
)

func TestSourceIndexRecoversInterruptedUpdatesAndTruncation(t *testing.T) {
	repo, _ := primitives.NewRepoID()
	store, _ := primitives.NewStoreID()
	worktree, _ := primitives.NewWorktreeID()
	producer, _ := primitives.NewEventProducerID()
	log := OpenFor(t.TempDir(), "/workspace", repo, store, worktree, producer)
	session := sessionID(t, "indexed")
	appendEvent := func(source string) Event {
		t.Helper()
		payload := json.RawMessage(`{}`)
		if source == "first" {
			payload, _ = json.Marshal(map[string]string{"text": strings.Repeat("x", 1<<20)})
		}
		event, err := log.Append(AppendInput{SessionID: session, Type: primitives.EventTypeToolCall, SourceID: source, Payload: payload})
		if err != nil {
			t.Fatal(err)
		}
		return event
	}
	first := appendEvent("first")
	if _, err := log.ContainsSourceID(session, "first"); err != nil {
		t.Fatal(err)
	}
	stream := first.StreamID
	original, err := os.ReadFile(log.eventPath(first))
	if err != nil {
		t.Fatal(err)
	}
	indexBytes, err := os.ReadFile(log.sourceIndexPath(session, stream))
	if err != nil {
		t.Fatal(err)
	}
	second := appendEvent("second")
	// Simulate a durable append whose index transaction did not commit.
	if err := os.WriteFile(log.sourceIndexPath(session, stream), indexBytes, 0600); err != nil {
		t.Fatal(err)
	}
	repeated := appendEvent("second")
	if repeated.Hash != second.Hash {
		t.Fatal("duplicated event after interrupted index update")
	}
	if err := os.WriteFile(log.eventPath(first), original, 0600); err != nil {
		t.Fatal(err)
	}
	found, err := log.ContainsSourceID(session, "second")
	if err != nil || found {
		t.Fatalf("truncated event found=%v err=%v", found, err)
	}
	replacement := appendEvent("replacement")
	if replacement.Seq.Uint64() != 2 {
		t.Fatalf("sequence=%s", replacement.Seq)
	}
	// A broken disposable database must not block or change deduplication.
	if err := os.WriteFile(log.sourceIndexPath(session, stream), []byte("broken sqlite"), 0600); err != nil {
		t.Fatal(err)
	}
	repeated = appendEvent("first")
	if repeated.Hash != first.Hash {
		t.Fatal("duplicated event with damaged index")
	}
}

func TestReadLastExpandsAcrossRecordBoundaries(t *testing.T) {
	log := Open(t.TempDir())
	session := sessionID(t, "long-tail")
	for _, size := range []int{1, 4096, 8192, 100000} {
		payload, err := json.Marshal(map[string]string{"text": strings.Repeat("x", size)})
		if err != nil {
			t.Fatal(err)
		}
		appended, err := log.Append(AppendInput{SessionID: session, Type: primitives.EventTypeAssistantMessage, Payload: payload})
		if err != nil {
			t.Fatal(err)
		}
		last, found, err := log.readLast(session, log.sessionPath(session), "")
		if err != nil || !found || last.Hash != appended.Hash {
			t.Fatalf("size=%d found=%v err=%v", size, found, err)
		}
	}
}
