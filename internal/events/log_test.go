package events

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/primitives"
)

func TestAppendReadAndVerifyHashChain(t *testing.T) {
	log := Open(t.TempDir())
	sessionID := sessionID(t, "Demo")
	turnID, _ := primitives.NewTurnID(1)
	now, _ := primitives.NewTimestamp(time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC))

	first, err := log.Append(AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypeTurnStart,
		Adapter:   primitives.AdapterClaudeCode,
		Time:      now,
		SourceID:  "prompt-1",
		Payload:   json.RawMessage(`{ "turn": 1, "phase": "pre" }`),
	})
	if err != nil {
		t.Fatalf("Append first: %v", err)
	}
	if first.Seq.Uint64() != 1 || first.PrevHash != GenesisHash {
		t.Fatalf("unexpected first event chain fields: %#v", first)
	}
	if string(first.Payload) != `{"turn":1,"phase":"pre"}` {
		t.Fatalf("payload was not compacted: %s", first.Payload)
	}

	second, err := log.Append(AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypePromptUser,
		Adapter:   primitives.AdapterClaudeCode,
		Time:      now,
		SourceID:  "prompt-2",
		Payload:   json.RawMessage(`{"text":"hello"}`),
	})
	if err != nil {
		t.Fatalf("Append second: %v", err)
	}
	if second.Seq.Uint64() != 2 || second.PrevHash != first.Hash {
		t.Fatalf("unexpected second event chain fields: %#v", second)
	}

	events, err := log.Read(sessionID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if events[0].Hash != first.Hash || events[1].Hash != second.Hash {
		t.Fatalf("read hashes changed: %#v", events)
	}
	if err := log.Verify(sessionID); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestCompactPayloadPreservesStringBytes(t *testing.T) {
	payload := json.RawMessage(" { \n \"text\" : \" spaces \\\" and \\\\ tabs\\t stay \" , \"number\" : 1.00e+2 } \r\n")
	compacted, err := compactPayload(payload)
	if err != nil {
		t.Fatalf("compactPayload: %v", err)
	}
	want := `{"text":" spaces \" and \\ tabs\t stay ","number":1.00e+2}`
	if string(compacted) != want {
		t.Fatalf("compacted payload = %q, want %q", compacted, want)
	}
	if _, err := compactPayload(json.RawMessage(`{"broken":`)); err == nil {
		t.Fatal("compactPayload accepted malformed JSON")
	}
}

func TestReadPreservesStreamSequenceAcrossClockRegression(t *testing.T) {
	log := Open(t.TempDir())
	sessionID := sessionID(t, "clock-regression")
	later, _ := primitives.NewTimestamp(time.Date(2026, 7, 12, 12, 0, 1, 0, time.UTC))
	earlier, _ := primitives.NewTimestamp(time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC))
	for _, input := range []AppendInput{
		{SessionID: sessionID, Type: primitives.EventTypePromptUser, Time: later, Payload: json.RawMessage(`{"text":"first"}`)},
		{SessionID: sessionID, Type: primitives.EventTypeAssistantMessage, Time: earlier, Payload: json.RawMessage(`{"text":"second"}`)},
	} {
		if _, err := log.Append(input); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	events, err := log.Read(sessionID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 2 || events[0].Seq.Uint64() != 1 || events[1].Seq.Uint64() != 2 {
		t.Fatalf("event order = %#v", events)
	}
}

func TestIndependentV2StreamsCanReuseSessionAndSequence(t *testing.T) {
	metadataDir := t.TempDir()
	repoID, err := primitives.NewRepoID()
	if err != nil {
		t.Fatalf("NewRepoID: %v", err)
	}
	storeID, err := primitives.NewStoreID()
	if err != nil {
		t.Fatalf("NewStoreID: %v", err)
	}
	worktreeOne, _ := primitives.NewWorktreeID()
	worktreeTwo, _ := primitives.NewWorktreeID()
	producerOne, _ := primitives.NewEventProducerID()
	producerTwo, _ := primitives.NewEventProducerID()
	sessionID := sessionID(t, "shared-session")

	logOne := OpenFor(metadataDir, "/workspace/one", repoID, storeID, worktreeOne, producerOne)
	logTwo := OpenFor(metadataDir, "/workspace/two", repoID, storeID, worktreeTwo, producerTwo)
	first, err := logOne.Append(AppendInput{SessionID: sessionID, Type: primitives.EventTypePromptUser, Payload: json.RawMessage(`{"text":"one"}`)})
	if err != nil {
		t.Fatalf("append stream one: %v", err)
	}
	logOneAlias := OpenFor(metadataDir, "/workspace/one-alias", repoID, storeID, worktreeOne, producerOne)
	continued, err := logOneAlias.Append(AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypeAssistantMessage,
		Payload:   json.RawMessage(`{"text":"continued"}`),
	})
	if err != nil {
		t.Fatalf("continue stream one through alias: %v", err)
	}
	second, err := logTwo.Append(AppendInput{SessionID: sessionID, Type: primitives.EventTypePromptUser, Payload: json.RawMessage(`{"text":"two"}`)})
	if err != nil {
		t.Fatalf("append stream two: %v", err)
	}
	if first.Seq.Uint64() != 1 || continued.Seq.Uint64() != 2 || second.Seq.Uint64() != 1 {
		t.Fatalf("stream sequences = %s, %s, and %s; want 1, 2, and 1", first.Seq, continued.Seq, second.Seq)
	}
	if first.StreamID == second.StreamID || first.WorktreeID == second.WorktreeID {
		t.Fatalf("stream identities collided: first=%#v second=%#v", first, second)
	}

	streams, err := ListDurableStreams(metadataDir)
	if err != nil {
		t.Fatalf("ListDurableStreams: %v", err)
	}
	if len(streams) != 2 {
		t.Fatalf("durable streams = %d, want 2: %#v", len(streams), streams)
	}
	for _, stream := range streams {
		want := "/workspace/one"
		if stream.WorktreeID == worktreeTwo {
			want = "/workspace/two"
		}
		if stream.WorkspaceRoot != want {
			t.Fatalf("stream %s workspace root = %q, want %q", stream.StreamID, stream.WorkspaceRoot, want)
		}
	}
	if err := WriteStreamMetadata(metadataDir, StreamMetadata{
		Version:       1,
		StreamID:      first.StreamID,
		ProducerID:    producerOne,
		RepoID:        repoID,
		WorktreeID:    worktreeOne,
		SessionID:     sessionID,
		WorkspaceRoot: "/workspace/conflict",
	}); err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("workspace root metadata collision error = %v", err)
	}
	events, err := Open(metadataDir).Read(sessionID)
	if err != nil {
		t.Fatalf("aggregate Read: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("aggregate events = %d, want 3", len(events))
	}
}

func TestAppendBackfillsLegacyStreamWorkspaceRoot(t *testing.T) {
	metadataDir := t.TempDir()
	repoID, _ := primitives.NewRepoID()
	storeID, _ := primitives.NewStoreID()
	worktreeID, _ := primitives.NewWorktreeID()
	producerID, _ := primitives.NewEventProducerID()
	sessionID := sessionID(t, "legacy-root")
	log := OpenFor(metadataDir, "/workspace/source", repoID, storeID, worktreeID, producerID)
	first, err := log.Append(AppendInput{SessionID: sessionID, Type: primitives.EventTypePromptUser, Payload: json.RawMessage(`{"text":"first"}`)})
	if err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(metadataDir, "log", "streams", first.StreamID.String()+".json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	var metadata StreamMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	metadata.WorkspaceRoot = ""
	legacy, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, append(legacy, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := log.Append(AppendInput{SessionID: sessionID, Type: primitives.EventTypeAssistantMessage, Payload: json.RawMessage(`{"text":"second"}`)}); err != nil {
		t.Fatal(err)
	}
	streams, err := ListDurableStreams(metadataDir)
	if err != nil || len(streams) != 1 || streams[0].WorkspaceRoot != "/workspace/source" {
		t.Fatalf("backfilled streams = %#v, %v", streams, err)
	}
}

func TestListDurableStreamsRejectsSymlink(t *testing.T) {
	metadataDir := t.TempDir()
	dir := filepath.Join(metadataDir, "log", eventLogDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir events: %v", err)
	}
	target := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(target, []byte("outside\n"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	createTestSymlink(t, target, filepath.Join(dir, "demo.jsonl"))
	if _, err := ListDurableStreams(metadataDir); err == nil || !strings.Contains(err.Error(), "symlink is not allowed") {
		t.Fatalf("ListDurableStreams error = %v, want symlink invariant", err)
	}
}

func TestVerifyDetectsCorruption(t *testing.T) {
	log := Open(t.TempDir())
	sessionID := sessionID(t, "demo")
	if _, err := log.Append(AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypePromptUser,
		Payload:   json.RawMessage(`{"text":"hello"}`),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	path := log.sessionPath(sessionID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	data = bytes.Replace(data, []byte(`"hello"`), []byte(`"HELLO"`), 1)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write corrupted log: %v", err)
	}
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("change corrupted log timestamp: %v", err)
	}

	err = log.Verify(sessionID)
	if err == nil {
		t.Fatal("Verify succeeded for corrupted payload")
	}
	if !strings.Contains(err.Error(), "hash") {
		t.Fatalf("Verify error = %v, want hash invariant", err)
	}
	if _, err := log.Append(AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypePromptUser,
		Payload:   json.RawMessage(`{"text":"must not append"}`),
	}); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("Append error = %v, want validated tail-state corruption refusal", err)
	}
}

func TestRecoverTrailingPartialLineOnly(t *testing.T) {
	log := Open(t.TempDir())
	sessionID := sessionID(t, "demo")
	first, err := log.Append(AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypePromptUser,
		Payload:   json.RawMessage(`{"text":"hello"}`),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	path := log.sessionPath(sessionID)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	if _, err := file.WriteString(`{"partial":`); err != nil {
		t.Fatalf("write partial: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close log: %v", err)
	}

	if _, err := log.Read(sessionID); err == nil {
		t.Fatal("Read succeeded with trailing partial line")
	}
	recovered, err := log.RecoverTrailingPartial(sessionID)
	if err != nil {
		t.Fatalf("RecoverTrailingPartial: %v", err)
	}
	if !recovered {
		t.Fatal("RecoverTrailingPartial recovered=false, want true")
	}

	events, err := log.Read(sessionID)
	if err != nil {
		t.Fatalf("Read after recover: %v", err)
	}
	if len(events) != 1 || events[0].Hash != first.Hash {
		t.Fatalf("events after recover = %#v", events)
	}
}

func TestRecoverDoesNotHideCompleteCorruptLine(t *testing.T) {
	log := Open(t.TempDir())
	sessionID := sessionID(t, "demo")
	if _, err := log.Append(AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypePromptUser,
		Payload:   json.RawMessage(`{"text":"hello"}`),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	path := log.sessionPath(sessionID)
	if err := os.WriteFile(path, []byte("{broken}\n"), 0o644); err != nil {
		t.Fatalf("write corrupt complete line: %v", err)
	}
	recovered, err := log.RecoverTrailingPartial(sessionID)
	if err != nil {
		t.Fatalf("RecoverTrailingPartial: %v", err)
	}
	if recovered {
		t.Fatal("RecoverTrailingPartial recovered complete corrupt line")
	}
	if err := log.Verify(sessionID); err == nil {
		t.Fatal("Verify succeeded for complete corrupt line")
	}
}

func TestContainsSourceID(t *testing.T) {
	log := Open(t.TempDir())
	sessionID := sessionID(t, "demo")
	if _, err := log.Append(AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypePromptUser,
		SourceID:  "provider-event-1",
		Payload:   json.RawMessage(`{"text":"hello"}`),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	ok, err := log.ContainsSourceID(sessionID, "provider-event-1")
	if err != nil {
		t.Fatalf("ContainsSourceID: %v", err)
	}
	if !ok {
		t.Fatal("ContainsSourceID=false, want true")
	}
	ok, err = log.ContainsSourceID(sessionID, "missing")
	if err != nil {
		t.Fatalf("ContainsSourceID missing: %v", err)
	}
	if ok {
		t.Fatal("ContainsSourceID missing=true, want false")
	}
}

func TestSourceMarkerIsIgnoredAfterEventLogTruncation(t *testing.T) {
	log := Open(t.TempDir())
	sessionID := sessionID(t, "demo")
	if _, err := log.Append(AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypePromptUser,
		SourceID:  "provider-event-1",
		Payload:   json.RawMessage(`{"text":"hello"}`),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := os.Truncate(log.sessionPath(sessionID), 0); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	if _, err := log.Append(AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypePromptUser,
		SourceID:  "replacement-event",
		Payload:   json.RawMessage(`{"text":"replacement"}`),
	}); err != nil {
		t.Fatalf("Append replacement: %v", err)
	}

	ok, err := log.ContainsSourceID(sessionID, "provider-event-1")
	if err != nil {
		t.Fatalf("ContainsSourceID after truncate: %v", err)
	}
	if ok {
		t.Fatal("ContainsSourceID=true after source event was truncated")
	}
}

func TestAppendRecoversTrailingPartialBeforeWriting(t *testing.T) {
	log := Open(t.TempDir())
	sessionID := sessionID(t, "demo")
	if _, err := log.Append(AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypePromptUser,
		Payload:   json.RawMessage(`{"text":"hello"}`),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	path := log.sessionPath(sessionID)
	if err := os.WriteFile(path, append(readFile(t, path), []byte(`{"partial":`)...), 0o644); err != nil {
		t.Fatalf("write partial: %v", err)
	}

	if _, err := log.Append(AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypePromptUser,
		Payload:   json.RawMessage(`{"text":"after"}`),
	}); err != nil {
		t.Fatalf("Append after partial: %v", err)
	}
	events, err := log.Read(sessionID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
}

func sessionID(t *testing.T, value string) primitives.SessionID {
	t.Helper()
	sessionID, err := primitives.ParseSessionID(value)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	return sessionID
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
