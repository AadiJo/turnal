package primitives

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// EventSeq is a 1-based sequence number within a session event log.
type EventSeq uint64

func NewEventSeq(value uint64) (EventSeq, error) {
	if value == 0 {
		return 0, invalid("event sequence", "", "must be greater than zero")
	}
	return EventSeq(value), nil
}

func ParseEventSeq(value string) (EventSeq, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, invalid("event sequence", value, "must not be empty")
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, invalid("event sequence", value, "must be an unsigned integer")
	}
	return NewEventSeq(parsed)
}

func (seq EventSeq) Uint64() uint64 {
	return uint64(seq)
}

func (seq EventSeq) String() string {
	return strconv.FormatUint(uint64(seq), 10)
}

func (seq EventSeq) MarshalText() ([]byte, error) {
	parsed, err := NewEventSeq(seq.Uint64())
	if err != nil {
		return nil, err
	}
	return []byte(parsed.String()), nil
}

func (seq *EventSeq) UnmarshalText(text []byte) error {
	parsed, err := ParseEventSeq(string(text))
	if err != nil {
		return err
	}
	*seq = parsed
	return nil
}

func (seq EventSeq) MarshalJSON() ([]byte, error) {
	if _, err := NewEventSeq(seq.Uint64()); err != nil {
		return nil, err
	}
	return json.Marshal(seq.Uint64())
}

func (seq *EventSeq) UnmarshalJSON(data []byte) error {
	if strings.TrimSpace(string(data)) == "null" {
		return invalid("event sequence", "", "must not be null")
	}
	var value uint64
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("invalid event sequence: must be a JSON number: %w", err)
	}
	parsed, err := NewEventSeq(value)
	if err != nil {
		return err
	}
	*seq = parsed
	return nil
}

// EventHash is the canonical hash-chain digest stored on each event.
type EventHash string

const eventHashPrefix = "sha256:"

func ParseEventHash(value string) (EventHash, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, eventHashPrefix) {
		return "", invalid("event hash", value, "must start with sha256:")
	}

	digest := value[len(eventHashPrefix):]
	if len(digest) != 64 {
		return "", invalid("event hash", value, "sha256 digest must be 64 hex characters")
	}
	if !isHex(digest) {
		return "", invalid("event hash", value, "digest must be hex encoded")
	}

	return EventHash(eventHashPrefix + strings.ToLower(digest)), nil
}

func (hash EventHash) String() string {
	return string(hash)
}

func (hash EventHash) MarshalText() ([]byte, error) {
	parsed, err := ParseEventHash(hash.String())
	if err != nil {
		return nil, err
	}
	return []byte(parsed), nil
}

func (hash *EventHash) UnmarshalText(text []byte) error {
	parsed, err := ParseEventHash(string(text))
	if err != nil {
		return err
	}
	*hash = parsed
	return nil
}

// EventType is the normalized event kind written to the append-only event log.
type EventType string

// SecretsRedactionText is the durable marker stored when text capture is
// disabled by Turnal's secrets policy.
const SecretsRedactionText = "[redacted by turnal secrets policy]"

const (
	EventTypeSessionStart      EventType = "session.start"
	EventTypeTurnStart         EventType = "turn.start"
	EventTypePromptUser        EventType = "prompt.user"
	EventTypeAssistantMessage  EventType = "assistant.message"
	EventTypeToolCall          EventType = "tool.call"
	EventTypeToolResult        EventType = "tool.result"
	EventTypeTurnFinish        EventType = "turn.finish"
	EventTypeCheckpoint        EventType = "checkpoint"
	EventTypeRollback          EventType = "rollback"
	EventTypeError             EventType = "error"
	EventTypeAdapterRaw        EventType = "adapter.raw"
	EventTypeRunStart          EventType = "run.start"
	EventTypeRunCaptureLink    EventType = "run.capture.link"
	EventTypeRunAttemptLink    EventType = "run.attempt.link"
	EventTypeRunFinish         EventType = "run.finish"
	EventTypeTaskCreate        EventType = "task.create"
	EventTypeTaskRevision      EventType = "task.revision.create"
	EventTypeCaseCreate        EventType = "case.create"
	EventTypeCaseAttemptLink   EventType = "case.attempt.link"
	EventTypeCaseAttemptResult EventType = "case.attempt.result"
	EventTypeCaseAttemptSelect EventType = "case.attempt.select"
	EventTypeCaseAttemptApply  EventType = "case.attempt.apply"
)

var validEventTypes = map[EventType]struct{}{
	EventTypeSessionStart:      {},
	EventTypeTurnStart:         {},
	EventTypePromptUser:        {},
	EventTypeAssistantMessage:  {},
	EventTypeToolCall:          {},
	EventTypeToolResult:        {},
	EventTypeTurnFinish:        {},
	EventTypeCheckpoint:        {},
	EventTypeRollback:          {},
	EventTypeError:             {},
	EventTypeAdapterRaw:        {},
	EventTypeRunStart:          {},
	EventTypeRunCaptureLink:    {},
	EventTypeRunAttemptLink:    {},
	EventTypeRunFinish:         {},
	EventTypeTaskCreate:        {},
	EventTypeTaskRevision:      {},
	EventTypeCaseCreate:        {},
	EventTypeCaseAttemptLink:   {},
	EventTypeCaseAttemptResult: {},
	EventTypeCaseAttemptSelect: {},
	EventTypeCaseAttemptApply:  {},
}

func ParseEventType(value string) (EventType, error) {
	value = strings.TrimSpace(value)
	eventType := EventType(value)
	if _, ok := validEventTypes[eventType]; !ok {
		return "", invalid("event type", value, "is not a known normalized event type")
	}
	return eventType, nil
}

func (eventType EventType) Valid() bool {
	_, ok := validEventTypes[eventType]
	return ok
}

func (eventType EventType) String() string {
	return string(eventType)
}

func (eventType EventType) MarshalText() ([]byte, error) {
	parsed, err := ParseEventType(eventType.String())
	if err != nil {
		return nil, err
	}
	return []byte(parsed), nil
}

func (eventType *EventType) UnmarshalText(text []byte) error {
	parsed, err := ParseEventType(string(text))
	if err != nil {
		return err
	}
	*eventType = parsed
	return nil
}

// Timestamp is a non-zero UTC instant used by durable records.
type Timestamp struct {
	time.Time
}

func NewTimestamp(value time.Time) (Timestamp, error) {
	if value.IsZero() {
		return Timestamp{}, invalid("timestamp", "", "must not be zero")
	}
	return Timestamp{Time: value.UTC()}, nil
}

func NowTimestamp() Timestamp {
	return Timestamp{Time: time.Now().UTC()}
}

func ParseTimestamp(value string) (Timestamp, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Timestamp{}, invalid("timestamp", value, "must not be empty")
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return Timestamp{}, fmt.Errorf("invalid timestamp %q: must be RFC3339/RFC3339Nano: %w", value, err)
	}
	return NewTimestamp(parsed)
}

func (ts Timestamp) String() string {
	return ts.Time.UTC().Format(time.RFC3339Nano)
}

func (ts Timestamp) MarshalText() ([]byte, error) {
	if ts.Time.IsZero() {
		return nil, invalid("timestamp", "", "must not be zero")
	}
	return []byte(ts.String()), nil
}

func (ts *Timestamp) UnmarshalText(text []byte) error {
	parsed, err := ParseTimestamp(string(text))
	if err != nil {
		return err
	}
	*ts = parsed
	return nil
}

func (ts Timestamp) MarshalJSON() ([]byte, error) {
	text, err := ts.MarshalText()
	if err != nil {
		return nil, err
	}
	return json.Marshal(string(text))
}

func (ts *Timestamp) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("invalid timestamp: must be a JSON string: %w", err)
	}
	return ts.UnmarshalText([]byte(value))
}
