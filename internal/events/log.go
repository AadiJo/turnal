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

	"github.com/AadiJo/turnal/internal/primitives"
)

const eventLogDirName = "events"

var GenesisHash = mustEventHashFromBytes(nil)

type Log struct {
	Dir           string
	WorkspaceRoot string
	RepoID        primitives.RepoID
	StoreID       primitives.StoreID
	WorktreeID    primitives.WorktreeID
	ProducerID    primitives.EventProducerID
	Aggregate     bool
}

type Event struct {
	Version    int                      `json:"v"`
	RepoID     primitives.RepoID        `json:"repo_id,omitempty"`
	WorktreeID primitives.WorktreeID    `json:"worktree_id,omitempty"`
	StreamID   primitives.EventStreamID `json:"stream_id,omitempty"`
	Seq        primitives.EventSeq      `json:"seq"`
	SessionID  primitives.SessionID     `json:"session_id"`
	TurnID     *primitives.TurnID       `json:"turn_id,omitempty"`
	Type       primitives.EventType     `json:"type"`
	Adapter    primitives.AdapterName   `json:"adapter,omitempty"`
	Time       primitives.Timestamp     `json:"time"`
	SourceID   string                   `json:"source_id,omitempty"`
	RawRef     string                   `json:"raw_ref,omitempty"`
	PrevHash   primitives.EventHash     `json:"prev_hash"`
	Payload    json.RawMessage          `json:"payload"`
	Hash       primitives.EventHash     `json:"hash"`
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
	log := Log{Dir: filepath.Join(metadataDir, "log", eventLogDirName), Aggregate: true}
	log.RepoID, log.StoreID, log.WorktreeID, log.ProducerID = readDefaultContext(metadataDir)
	return log
}

func OpenFor(metadataDir, workspaceRoot string, repoID primitives.RepoID, storeID primitives.StoreID, worktreeID primitives.WorktreeID, producerID primitives.EventProducerID) Log {
	return Log{
		Dir:           filepath.Join(metadataDir, "log", eventLogDirName),
		WorkspaceRoot: workspaceRoot,
		RepoID:        repoID,
		StoreID:       storeID,
		WorktreeID:    worktreeID,
		ProducerID:    producerID,
	}
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
		name := entry.Name()
		var sessionText string
		if entry.IsDir() {
			sessionText = name
		} else {
			if filepath.Ext(name) != ".jsonl" {
				continue
			}
			sessionText = strings.TrimSuffix(name, ".jsonl")
		}
		sessionID, err := primitives.ParseSessionID(sessionText)
		if err != nil {
			return nil, fmt.Errorf("event log filename invariant failed for %s: %w", name, err)
		}
		seen := false
		for _, existing := range sessions {
			if existing == sessionID {
				seen = true
				break
			}
		}
		if !seen {
			sessions = append(sessions, sessionID)
		}
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

	version := 1
	var streamID primitives.EventStreamID
	path := log.sessionPath(sessionID)
	if log.ProducerID != "" {
		if log.RepoID, err = primitives.ParseRepoID(log.RepoID.String()); err != nil {
			return Event{}, err
		}
		if log.WorktreeID, err = primitives.ParseWorktreeID(log.WorktreeID.String()); err != nil {
			return Event{}, err
		}
		if log.ProducerID, err = primitives.ParseEventProducerID(log.ProducerID.String()); err != nil {
			return Event{}, err
		}
		streamID, err = primitives.DeriveEventStreamID(log.ProducerID, sessionID)
		if err != nil {
			return Event{}, err
		}
		version = 2
		path = log.streamPath(sessionID, streamID)
		if err := log.ensureStreamMetadata(sessionID, streamID); err != nil {
			return Event{}, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Event{}, fmt.Errorf("create event stream dir: %w", err)
	}
	lockDir := path + ".lock"
	if err := acquireDirLock(lockDir); err != nil {
		return Event{}, err
	}
	defer func() { _ = os.Remove(lockDir) }()

	if _, err := recoverTrailingPartialPath(path); err != nil {
		return Event{}, err
	}

	events, err := log.readPath(sessionID, path, streamID)
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
		Version:    version,
		RepoID:     log.RepoID,
		WorktreeID: log.WorktreeID,
		StreamID:   streamID,
		Seq:        nextSeq,
		SessionID:  sessionID,
		TurnID:     turnID,
		Type:       eventType,
		Adapter:    adapter,
		Time:       timestamp,
		SourceID:   input.SourceID,
		RawRef:     input.RawRef,
		PrevHash:   prevHash,
		Payload:    payload,
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
	if log.ProducerID != "" && !log.Aggregate {
		streamID, err := primitives.DeriveEventStreamID(log.ProducerID, parsedSessionID)
		if err != nil {
			return nil, err
		}
		return log.readPath(parsedSessionID, log.streamPath(parsedSessionID, streamID), streamID)
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
	_, ok, err := log.FindSourceID(sessionID, sourceID)
	return ok, err
}

func (log Log) FindSourceID(sessionID primitives.SessionID, sourceID string) (Event, bool, error) {
	if sourceID == "" {
		return Event{}, false, nil
	}
	events, err := log.Read(sessionID)
	if err != nil {
		return Event{}, false, err
	}
	for _, event := range events {
		if event.SourceID == sourceID {
			return event, true, nil
		}
	}
	return Event{}, false, nil
}

func (log Log) RecoverTrailingPartial(sessionID primitives.SessionID) (bool, error) {
	parsedSessionID, err := primitives.ParseSessionID(sessionID.String())
	if err != nil {
		return false, err
	}

	path := log.sessionPath(parsedSessionID)
	if log.ProducerID != "" && !log.Aggregate {
		streamID, err := primitives.DeriveEventStreamID(log.ProducerID, parsedSessionID)
		if err != nil {
			return false, err
		}
		path = log.streamPath(parsedSessionID, streamID)
	}
	return recoverTrailingPartialPath(path)
}

func recoverTrailingPartialPath(path string) (bool, error) {
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
	var events []Event
	legacy, err := log.readPath(sessionID, log.sessionPath(sessionID), "")
	if err != nil {
		return nil, err
	}
	events = append(events, legacy...)

	dir := filepath.Join(log.Dir, sessionID.String())
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read event stream dir: %w", err)
		}
	} else {
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
				continue
			}
			streamID, err := primitives.ParseEventStreamID(strings.TrimSuffix(entry.Name(), ".jsonl"))
			if err != nil {
				return nil, fmt.Errorf("event stream filename invariant failed for %s: %w", entry.Name(), err)
			}
			streamEvents, err := log.readPath(sessionID, filepath.Join(dir, entry.Name()), streamID)
			if err != nil {
				return nil, err
			}
			events = append(events, streamEvents...)
		}
	}
	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].Time.Time.Equal(events[j].Time.Time) {
			return events[i].Time.Time.Before(events[j].Time.Time)
		}
		if events[i].StreamID != events[j].StreamID {
			return events[i].StreamID.String() < events[j].StreamID.String()
		}
		return events[i].Seq.Uint64() < events[j].Seq.Uint64()
	})
	return events, nil
}

func (log Log) readPath(sessionID primitives.SessionID, path string, expectedStreamID primitives.EventStreamID) ([]Event, error) {
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
		if event.Version == 1 && event.StreamID == "" && expectedStreamID != "" {
			event.StreamID = expectedStreamID
			if metadata, ok := log.readStreamMetadata(expectedStreamID); ok {
				event.WorktreeID = metadata.WorktreeID
				event.RepoID = metadata.RepoID
			}
		}
		if expectedStreamID != "" && event.StreamID != expectedStreamID {
			return nil, fmt.Errorf("event log invariant failed for session %s line %d: event stream %s does not match file stream %s", sessionID, lineNumber, event.StreamID, expectedStreamID)
		}
		if event.Version == 1 && event.StreamID == "" && log.StoreID != "" {
			event.StreamID, err = primitives.DeriveLegacyEventStreamID(log.StoreID, sessionID)
			if err != nil {
				return nil, err
			}
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
	if event.Version != 1 && event.Version != 2 {
		return Event{}, fmt.Errorf("unsupported version %d", event.Version)
	}
	if event.Version == 2 {
		var err error
		if event.RepoID, err = primitives.ParseRepoID(event.RepoID.String()); err != nil {
			return Event{}, err
		}
		if event.WorktreeID, err = primitives.ParseWorktreeID(event.WorktreeID.String()); err != nil {
			return Event{}, err
		}
		if event.StreamID, err = primitives.ParseEventStreamID(event.StreamID.String()); err != nil {
			return Event{}, err
		}
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

func (log Log) streamPath(sessionID primitives.SessionID, streamID primitives.EventStreamID) string {
	return filepath.Join(log.Dir, sessionID.String(), streamID.String()+".jsonl")
}

func StreamPath(metadataDir string, sessionID primitives.SessionID, streamID primitives.EventStreamID) string {
	return filepath.Join(metadataDir, "log", eventLogDirName, sessionID.String(), streamID.String()+".jsonl")
}

type StreamMetadata struct {
	Version    int                        `json:"version"`
	StreamID   primitives.EventStreamID   `json:"stream_id"`
	ProducerID primitives.EventProducerID `json:"event_producer_id"`
	RepoID     primitives.RepoID          `json:"repo_id"`
	WorktreeID primitives.WorktreeID      `json:"worktree_id"`
	SessionID  primitives.SessionID       `json:"session_id"`
	CreatedAt  string                     `json:"created_at"`
}

func (log Log) ensureStreamMetadata(sessionID primitives.SessionID, streamID primitives.EventStreamID) error {
	metadataDir := filepath.Clean(filepath.Join(log.Dir, "..", "streams"))
	path := filepath.Join(metadataDir, streamID.String()+".json")
	if data, err := os.ReadFile(path); err == nil {
		var metadata StreamMetadata
		if err := json.Unmarshal(data, &metadata); err != nil {
			return fmt.Errorf("event stream metadata invariant failed at %s: %w", path, err)
		}
		if metadata.StreamID != streamID || metadata.ProducerID != log.ProducerID || metadata.SessionID != sessionID || metadata.WorktreeID != log.WorktreeID || metadata.RepoID != log.RepoID {
			return fmt.Errorf("event stream metadata invariant failed at %s: identity mismatch", path)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read event stream metadata: %w", err)
	}
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		return fmt.Errorf("create event stream metadata dir: %w", err)
	}
	metadata := StreamMetadata{
		Version:    1,
		StreamID:   streamID,
		ProducerID: log.ProducerID,
		RepoID:     log.RepoID,
		WorktreeID: log.WorktreeID,
		SessionID:  sessionID,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal event stream metadata: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(metadataDir, ".stream-*")
	if err != nil {
		return fmt.Errorf("create event stream metadata temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("commit event stream metadata: %w", err)
	}
	return nil
}

func WriteStreamMetadata(metadataDir string, metadata StreamMetadata) error {
	if metadata.Version != 1 {
		return fmt.Errorf("event stream metadata invariant failed: unsupported version %d", metadata.Version)
	}
	streamID, err := primitives.ParseEventStreamID(metadata.StreamID.String())
	if err != nil {
		return err
	}
	metadata.StreamID = streamID
	if metadata.RepoID, err = primitives.ParseRepoID(metadata.RepoID.String()); err != nil {
		return err
	}
	if metadata.WorktreeID, err = primitives.ParseWorktreeID(metadata.WorktreeID.String()); err != nil {
		return err
	}
	if metadata.SessionID, err = primitives.ParseSessionID(metadata.SessionID.String()); err != nil {
		return err
	}
	if metadata.ProducerID != "" {
		if metadata.ProducerID, err = primitives.ParseEventProducerID(metadata.ProducerID.String()); err != nil {
			return err
		}
	}
	if metadata.CreatedAt == "" {
		metadata.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	dir := filepath.Join(metadataDir, "log", "streams")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create event stream metadata dir: %w", err)
	}
	path := filepath.Join(dir, streamID.String()+".json")
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if existing, readErr := os.ReadFile(path); readErr == nil {
		var parsed StreamMetadata
		if json.Unmarshal(existing, &parsed) != nil || parsed.StreamID != metadata.StreamID || parsed.RepoID != metadata.RepoID || parsed.WorktreeID != metadata.WorktreeID || parsed.SessionID != metadata.SessionID {
			return fmt.Errorf("event stream metadata collision at %s", path)
		}
		return nil
	} else if !os.IsNotExist(readErr) {
		return readErr
	}
	return os.WriteFile(path, data, 0o600)
}

func (log Log) readStreamMetadata(streamID primitives.EventStreamID) (StreamMetadata, bool) {
	path := filepath.Clean(filepath.Join(log.Dir, "..", "streams", streamID.String()+".json"))
	data, err := os.ReadFile(path)
	if err != nil {
		return StreamMetadata{}, false
	}
	var metadata StreamMetadata
	if json.Unmarshal(data, &metadata) != nil || metadata.StreamID != streamID {
		return StreamMetadata{}, false
	}
	return metadata, true
}

func readDefaultContext(metadataDir string) (primitives.RepoID, primitives.StoreID, primitives.WorktreeID, primitives.EventProducerID) {
	data, err := os.ReadFile(filepath.Join(metadataDir, "identity.json"))
	if err != nil {
		return "", "", "", ""
	}
	var identity struct {
		RepoID  primitives.RepoID  `json:"repo_id"`
		StoreID primitives.StoreID `json:"store_id"`
	}
	if json.Unmarshal(data, &identity) != nil {
		return "", "", "", ""
	}
	repoID, err := primitives.ParseRepoID(identity.RepoID.String())
	if err != nil {
		return "", "", "", ""
	}
	storeID, err := primitives.ParseStoreID(identity.StoreID.String())
	if err != nil {
		return "", "", "", ""
	}
	entries, err := os.ReadDir(filepath.Join(metadataDir, "worktrees"))
	if err != nil {
		return repoID, storeID, "", ""
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		bindingData, err := os.ReadFile(filepath.Join(metadataDir, "worktrees", entry.Name()))
		if err != nil {
			continue
		}
		var binding struct {
			WorktreeID primitives.WorktreeID      `json:"worktree_id"`
			ProducerID primitives.EventProducerID `json:"event_producer_id"`
			Primary    bool                       `json:"primary"`
		}
		if json.Unmarshal(bindingData, &binding) != nil || !binding.Primary {
			continue
		}
		worktreeID, worktreeErr := primitives.ParseWorktreeID(binding.WorktreeID.String())
		producerID, producerErr := primitives.ParseEventProducerID(binding.ProducerID.String())
		if worktreeErr == nil && producerErr == nil {
			return repoID, storeID, worktreeID, producerID
		}
	}
	return repoID, storeID, "", ""
}

func eventHash(event Event) (primitives.EventHash, error) {
	payload, err := compactPayload(event.Payload)
	if err != nil {
		return "", err
	}
	payloadDigest := sha256.Sum256(payload)

	var input []byte
	input = appendLengthPrefixed(input, fmt.Sprintf("%d", event.Version))
	if event.Version >= 2 {
		input = appendLengthPrefixed(input, event.RepoID.String())
		input = appendLengthPrefixed(input, event.WorktreeID.String())
		input = appendLengthPrefixed(input, event.StreamID.String())
	}
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
