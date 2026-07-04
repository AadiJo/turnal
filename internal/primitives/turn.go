package primitives

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// TurnID is a 1-based turn number within a captured session.
type TurnID uint64

func NewTurnID(value uint64) (TurnID, error) {
	if value == 0 {
		return 0, invalid("turn id", "", "must be greater than zero")
	}
	return TurnID(value), nil
}

func ParseTurnID(value string) (TurnID, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, invalid("turn id", value, "must not be empty")
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, invalid("turn id", value, "must be an unsigned integer")
	}
	return NewTurnID(parsed)
}

func (id TurnID) Uint64() uint64 {
	return uint64(id)
}

func (id TurnID) String() string {
	return strconv.FormatUint(uint64(id), 10)
}

func (id TurnID) RefSegment() string {
	return fmt.Sprintf("%06d", uint64(id))
}

func (id TurnID) MarshalText() ([]byte, error) {
	parsed, err := NewTurnID(id.Uint64())
	if err != nil {
		return nil, err
	}
	return []byte(parsed.String()), nil
}

func (id *TurnID) UnmarshalText(text []byte) error {
	parsed, err := ParseTurnID(string(text))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func (id TurnID) MarshalJSON() ([]byte, error) {
	if _, err := NewTurnID(id.Uint64()); err != nil {
		return nil, err
	}
	return json.Marshal(id.Uint64())
}

func (id *TurnID) UnmarshalJSON(data []byte) error {
	if strings.TrimSpace(string(data)) == "null" {
		return invalid("turn id", "", "must not be null")
	}
	var value uint64
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("invalid turn id: must be a JSON number: %w", err)
	}
	parsed, err := NewTurnID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}
