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

	err = log.Verify(sessionID)
	if err == nil {
		t.Fatal("Verify succeeded for corrupted payload")
	}
	if !strings.Contains(err.Error(), "hash") {
		t.Fatalf("Verify error = %v, want hash invariant", err)
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
