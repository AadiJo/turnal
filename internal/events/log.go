package events

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agent-vcs-again/internal/primitives"
)

const eventLogDirName = "events"

var GenesisHash = mustEventHashFromBytes(nil)

type Log struct {
	Dir string
}

type Event struct {
	Version   int                    `json:"v"`
	Seq       primitives.EventSeq    `json:"seq"`
	SessionID primitives.SessionID   `json:"session_id"`
	TurnID    *primitives.TurnID     `json:"turn_id,omitempty"`
	Type      primitives.EventType   `json:"type"`
	Adapter   primitives.AdapterName `json:"adapter,omitempty"`
	Time      primitives.Timestamp   `json:"time"`
	SourceID  string                 `json:"source_id,omitempty"`
	RawRef    string                 `json:"raw_ref,omitempty"`
	PrevHash  primitives.EventHash   `json:"prev_hash"`
	Payload   json.RawMessage        `json:"payload"`
	Hash      primitives.EventHash   `json:"hash"`
}

type AppendContext struct {
	Seq            primitives.EventSeq
	PreviousEvents []Event
}

type AppendInput struct {
	SessionID    primitives.SessionID
	TurnID       *primitives.TurnID
	Type         primitives.EventType
	Adapter      primitives.AdapterName
	Time         primitives.Timestamp
	SourceID     string
	RawRef       string
	Payload      json.RawMessage
	BuildPayload func(AppendContext) (json.RawMessage, error)
}

func Open(metadataDir string) Log {
	return Log{Dir: filepath.Join(metadataDir, "log", eventLogDirName)}
}

func ListSessions(metadataDir string) ([]primitives.SessionID, error) {
	return Open(metadataDir).ListSessions()
}

func (log Log) ListSessions() ([]primitives.SessionID, error) {
	entries, err := os.ReadDir(log.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read event log dir: %w", err)
	}

	var sessions []primitives.SessionID
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if filepath.Ext(name) != ".jsonl" {
			continue
		}
		sessionID, err := primitives.ParseSessionID(strings.TrimSuffix(name, ".jsonl"))
		if err != nil {
			return nil, fmt.Errorf("event log filename invariant failed for %s: %w", name, err)
		}
		sessions = append(sessions, sessionID)
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].String() < sessions[j].String()
	})
	return sessions, nil
}

func (log Log) Append(input AppendInput) (Event, error) {
	sessionID, err := primitives.ParseSessionID(input.SessionID.String())
	if err != nil {
		return Event{}, err
	}
	eventType, err := primitives.ParseEventType(input.Type.String())
	if err != nil {
		return Event{}, err
	}

	var adapter primitives.AdapterName
	if input.Adapter != "" {
		adapter, err = primitives.ParseAdapterName(input.Adapter.String())
		if err != nil {
			return Event{}, err
		}
	}

	var turnID *primitives.TurnID
	if input.TurnID != nil {
		parsed, err := primitives.NewTurnID(input.TurnID.Uint64())
		if err != nil {
			return Event{}, err
		}
		turnID = &parsed
	}

	timestamp := input.Time
	if timestamp.Time.IsZero() {
		timestamp = primitives.NowTimestamp()
	} else if timestamp, err = primitives.NewTimestamp(timestamp.Time); err != nil {
		return Event{}, err
	}

	payload := input.Payload
	if input.BuildPayload == nil {
		payload, err = compactPayload(payload)
		if err != nil {
			return Event{}, err
		}
	}

	if err := os.MkdirAll(log.Dir, 0o755); err != nil {
		return Event{}, fmt.Errorf("create event log dir: %w", err)
	}

	path := log.sessionPath(sessionID)
	lockDir := path + ".lock"
	if err := acquireDirLock(lockDir); err != nil {
		return Event{}, err
	}
	defer func() { _ = os.Remove(lockDir) }()

	if _, err := log.RecoverTrailingPartial(sessionID); err != nil {
		return Event{}, err
	}

	events, err := log.read(sessionID)
	if err != nil {
		return Event{}, err
	}

	nextSeq, err := primitives.NewEventSeq(uint64(len(events)) + 1)
	if err != nil {
		return Event{}, err
	}
	prevHash := GenesisHash
	if len(events) > 0 {
		prevHash = events[len(events)-1].Hash
	}

	if input.BuildPayload != nil {
		payload, err = input.BuildPayload(AppendContext{
			Seq:            nextSeq,
			PreviousEvents: events,
		})
		if err != nil {
			return Event{}, err
		}
		payload, err = compactPayload(payload)
		if err != nil {
			return Event{}, err
		}
	}

	event := Event{
		Version:   1,
		Seq:       nextSeq,
		SessionID: sessionID,
		TurnID:    turnID,
		Type:      eventType,
		Adapter:   adapter,
		Time:      timestamp,
		SourceID:  input.SourceID,
		RawRef:    input.RawRef,
		PrevHash:  prevHash,
		Payload:   payload,
	}
	event.Hash, err = eventHash(event)
	if err != nil {
		return Event{}, err
	}

	line, err := json.Marshal(event)
	if err != nil {
		return Event{}, fmt.Errorf("marshal event: %w", err)
	}
	line = append(line, '\n')

	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return Event{}, fmt.Errorf("open event log: %w", err)
	}
	defer func() { _ = file.Close() }()

	if _, err := file.Write(line); err != nil {
		return Event{}, fmt.Errorf("append event log: %w", err)
	}
	if err := file.Sync(); err != nil {
		return Event{}, fmt.Errorf("sync event log: %w", err)
	}

	return event, nil
}

func (log Log) Read(sessionID primitives.SessionID) ([]Event, error) {
	parsedSessionID, err := primitives.ParseSessionID(sessionID.String())
	if err != nil {
		return nil, err
	}
	return log.read(parsedSessionID)
}

func (log Log) Verify(sessionID primitives.SessionID) error {
	parsedSessionID, err := primitives.ParseSessionID(sessionID.String())
	if err != nil {
		return err
	}
	_, err = log.read(parsedSessionID)
	return err
}

func (log Log) ContainsSourceID(sessionID primitives.SessionID, sourceID string) (bool, error) {
	if sourceID == "" {
		return false, nil
	}
	events, err := log.Read(sessionID)
	if err != nil {
		return false, err
	}
	for _, event := range events {
		if event.SourceID == sourceID {
			return true, nil
		}
	}
	return false, nil
}

func (log Log) RecoverTrailingPartial(sessionID primitives.SessionID) (bool, error) {
	parsedSessionID, err := primitives.ParseSessionID(sessionID.String())
	if err != nil {
		return false, err
	}

	path := log.sessionPath(parsedSessionID)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read event log: %w", err)
	}
	if len(data) == 0 || data[len(data)-1] == '\n' {
		return false, nil
	}

	truncateAt := int64(0)
	if lastNewline := bytes.LastIndexByte(data, '\n'); lastNewline >= 0 {
		truncateAt = int64(lastNewline + 1)
	}
	if err := os.Truncate(path, truncateAt); err != nil {
		return false, fmt.Errorf("recover event log trailing partial line: %w", err)
	}
	return true, nil
}

func (log Log) read(sessionID primitives.SessionID) ([]Event, error) {
	path := log.sessionPath(sessionID)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read event log: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	if data[len(data)-1] != '\n' {
		return nil, fmt.Errorf("event log invariant failed for session %s: trailing partial line", sessionID)
	}

	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	events := make([]Event, 0, len(lines))
	prevHash := GenesisHash
	for i, line := range lines {
		lineNumber := i + 1
		if len(bytes.TrimSpace(line)) == 0 {
			return nil, fmt.Errorf("event log invariant failed for session %s line %d: empty event line", sessionID, lineNumber)
		}

		event, err := parseEventLine(line)
		if err != nil {
			return nil, fmt.Errorf("event log invariant failed for session %s line %d: %w", sessionID, lineNumber, err)
		}
		if event.SessionID != sessionID {
			return nil, fmt.Errorf("event log invariant failed for session %s line %d: event session %s does not match file session", sessionID, lineNumber, event.SessionID)
		}
		wantSeq, err := primitives.NewEventSeq(uint64(lineNumber))
		if err != nil {
			return nil, err
		}
		if event.Seq != wantSeq {
			return nil, fmt.Errorf("event log invariant failed for session %s line %d: seq %s, want %s", sessionID, lineNumber, event.Seq, wantSeq)
		}
		if event.PrevHash != prevHash {
			return nil, fmt.Errorf("event log invariant failed for session %s line %d: prev hash %s, want %s", sessionID, lineNumber, event.PrevHash, prevHash)
		}
		recomputed, err := eventHash(event)
		if err != nil {
			return nil, fmt.Errorf("event log invariant failed for session %s line %d: %w", sessionID, lineNumber, err)
		}
		if event.Hash != recomputed {
			return nil, fmt.Errorf("event log invariant failed for session %s line %d: hash %s, want %s", sessionID, lineNumber, event.Hash, recomputed)
		}

		events = append(events, event)
		prevHash = event.Hash
	}
	return events, nil
}

func parseEventLine(line []byte) (Event, error) {
	var event Event
	if err := json.Unmarshal(line, &event); err != nil {
		return Event{}, fmt.Errorf("malformed JSON: %w", err)
	}
	if event.Version != 1 {
		return Event{}, fmt.Errorf("unsupported version %d", event.Version)
	}
	if _, err := primitives.NewEventSeq(event.Seq.Uint64()); err != nil {
		return Event{}, err
	}
	sessionID, err := primitives.ParseSessionID(event.SessionID.String())
	if err != nil {
		return Event{}, err
	}
	event.SessionID = sessionID
	if event.TurnID != nil {
		turnID, err := primitives.NewTurnID(event.TurnID.Uint64())
		if err != nil {
			return Event{}, err
		}
		event.TurnID = &turnID
	}
	eventType, err := primitives.ParseEventType(event.Type.String())
	if err != nil {
		return Event{}, err
	}
	event.Type = eventType
	if event.Adapter != "" {
		adapter, err := primitives.ParseAdapterName(event.Adapter.String())
		if err != nil {
			return Event{}, err
		}
		event.Adapter = adapter
	}
	timestamp, err := primitives.ParseTimestamp(event.Time.String())
	if err != nil {
		return Event{}, err
	}
	event.Time = timestamp
	prevHash, err := primitives.ParseEventHash(event.PrevHash.String())
	if err != nil {
		return Event{}, err
	}
	event.PrevHash = prevHash
	hash, err := primitives.ParseEventHash(event.Hash.String())
	if err != nil {
		return Event{}, err
	}
	event.Hash = hash
	payload, err := compactPayload(event.Payload)
	if err != nil {
		return Event{}, err
	}
	event.Payload = payload
	return event, nil
}

func (log Log) sessionPath(sessionID primitives.SessionID) string {
	return filepath.Join(log.Dir, sessionID.String()+".jsonl")
}

func eventHash(event Event) (primitives.EventHash, error) {
	payload, err := compactPayload(event.Payload)
	if err != nil {
		return "", err
	}
	payloadDigest := sha256.Sum256(payload)

	var input []byte
	input = appendLengthPrefixed(input, fmt.Sprintf("%d", event.Version))
	input = appendLengthPrefixed(input, event.Seq.String())
	input = appendLengthPrefixed(input, event.SessionID.String())
	if event.TurnID == nil {
		input = appendLengthPrefixed(input, "")
	} else {
		input = appendLengthPrefixed(input, event.TurnID.String())
	}
	input = appendLengthPrefixed(input, event.Type.String())
	input = appendLengthPrefixed(input, event.Adapter.String())
	input = appendLengthPrefixed(input, event.Time.String())
	input = appendLengthPrefixed(input, event.SourceID)
	input = appendLengthPrefixed(input, event.RawRef)
	input = appendLengthPrefixed(input, event.PrevHash.String())
	input = appendLengthPrefixed(input, hex.EncodeToString(payloadDigest[:]))

	digest := sha256.Sum256(input)
	return primitives.ParseEventHash("sha256:" + hex.EncodeToString(digest[:]))
}

func compactPayload(payload json.RawMessage) (json.RawMessage, error) {
	if len(payload) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, payload); err != nil {
		return nil, fmt.Errorf("payload must be valid JSON: %w", err)
	}
	return append(json.RawMessage(nil), buf.Bytes()...), nil
}

func appendLengthPrefixed(input []byte, value string) []byte {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	input = append(input, length[:]...)
	input = append(input, value...)
	return input
}

func mustEventHashFromBytes(data []byte) primitives.EventHash {
	digest := sha256.Sum256(data)
	hash, err := primitives.ParseEventHash("sha256:" + hex.EncodeToString(digest[:]))
	if err != nil {
		panic(err)
	}
	return hash
}

func acquireDirLock(lockDir string) error {
	const attempts = 100
	for i := 0; i < attempts; i++ {
		if err := os.Mkdir(lockDir, 0o700); err == nil {
			return nil
		} else if !os.IsExist(err) {
			return fmt.Errorf("create event log lock: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("event log lock busy: %s", strings.TrimSuffix(lockDir, ".lock"))
}
