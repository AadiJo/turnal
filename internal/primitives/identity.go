package primitives

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const durableIDRandomBytes = 16

type RepoID string
type StoreID string
type WorktreeID string
type EventProducerID string
type EventStreamID string
type CheckpointID string
type ImportID string

func NewRepoID() (RepoID, error) {
	value, err := newDurableID("repo")
	return RepoID(value), err
}

func ParseRepoID(value string) (RepoID, error) {
	parsed, err := parseDurableID("repo id", "repo", value)
	return RepoID(parsed), err
}

func (id RepoID) String() string { return string(id) }

func NewStoreID() (StoreID, error) {
	value, err := newDurableID("store")
	return StoreID(value), err
}

func ParseStoreID(value string) (StoreID, error) {
	parsed, err := parseDurableID("store id", "store", value)
	return StoreID(parsed), err
}

func (id StoreID) String() string { return string(id) }

func NewWorktreeID() (WorktreeID, error) {
	value, err := newDurableID("wt")
	return WorktreeID(value), err
}

func ParseWorktreeID(value string) (WorktreeID, error) {
	parsed, err := parseDurableID("worktree id", "wt", value)
	return WorktreeID(parsed), err
}

func (id WorktreeID) String() string { return string(id) }

func NewEventProducerID() (EventProducerID, error) {
	value, err := newDurableID("producer")
	return EventProducerID(value), err
}

func ParseEventProducerID(value string) (EventProducerID, error) {
	parsed, err := parseDurableID("event producer id", "producer", value)
	return EventProducerID(parsed), err
}

func (id EventProducerID) String() string { return string(id) }

func NewEventStreamID() (EventStreamID, error) {
	value, err := newDurableID("stream")
	return EventStreamID(value), err
}

func ParseEventStreamID(value string) (EventStreamID, error) {
	parsed, err := parseDurableID("event stream id", "stream", value)
	return EventStreamID(parsed), err
}

func DeriveEventStreamID(producerID EventProducerID, sessionID SessionID) (EventStreamID, error) {
	producer, err := ParseEventProducerID(producerID.String())
	if err != nil {
		return "", err
	}
	session, err := ParseSessionID(sessionID.String())
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(producer.String() + "\x00" + session.String()))
	return ParseEventStreamID("stream_" + hex.EncodeToString(digest[:16]))
}

func DeriveLegacyEventStreamID(storeID StoreID, sessionID SessionID) (EventStreamID, error) {
	store, err := ParseStoreID(storeID.String())
	if err != nil {
		return "", err
	}
	session, err := ParseSessionID(sessionID.String())
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte("legacy\x00" + store.String() + "\x00" + session.String()))
	return ParseEventStreamID("stream_" + hex.EncodeToString(digest[:16]))
}

func (id EventStreamID) String() string { return string(id) }

func NewCheckpointID() (CheckpointID, error) {
	value, err := newDurableID("chk")
	return CheckpointID(value), err
}

func ParseCheckpointID(value string) (CheckpointID, error) {
	parsed, err := parseDurableID("checkpoint id", "chk", value)
	return CheckpointID(parsed), err
}

func (id CheckpointID) String() string { return string(id) }

func NewImportID() (ImportID, error) {
	value, err := newDurableID("import")
	return ImportID(value), err
}

func ParseImportID(value string) (ImportID, error) {
	parsed, err := parseDurableID("import id", "import", value)
	return ImportID(parsed), err
}

func (id ImportID) String() string { return string(id) }

func newDurableID(prefix string) (string, error) {
	random := make([]byte, durableIDRandomBytes)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return prefix + "_" + hex.EncodeToString(random), nil
}

func parseDurableID(label, prefix, value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	wantPrefix := prefix + "_"
	if !strings.HasPrefix(value, wantPrefix) {
		return "", invalid(label, value, "must start with "+wantPrefix)
	}
	digest := strings.TrimPrefix(value, wantPrefix)
	if len(digest) != durableIDRandomBytes*2 || !isHex(digest) {
		return "", invalid(label, value, fmt.Sprintf("must contain %d lowercase hex characters", durableIDRandomBytes*2))
	}
	return wantPrefix + digest, nil
}
