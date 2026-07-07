package recall

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
)

type TurnTarget struct {
	SessionID primitives.SessionID `json:"session_id"`
	TurnID    primitives.TurnID    `json:"turn_id"`
}

func (reader Reader) ResolveTurnRef(value string) (TurnTarget, error) {
	if strings.TrimSpace(reader.MetadataDir) == "" {
		return TurnTarget{}, fmt.Errorf("recall requires metadata dir")
	}

	value = strings.TrimSpace(value)
	if value == "" || value == "latest" {
		return reader.latestTurn("")
	}

	if strings.Contains(value, ":") {
		sessionText, turnText, ok := strings.Cut(value, ":")
		if !ok || strings.Contains(turnText, ":") {
			return TurnTarget{}, fmt.Errorf("invalid turn ref %q: must be <turn>, latest, or <session>:<turn|latest>", value)
		}
		sessionID, err := primitives.ParseSessionID(sessionText)
		if err != nil {
			return TurnTarget{}, err
		}
		if strings.TrimSpace(turnText) == "latest" {
			return reader.latestTurn(sessionID)
		}
		turnID, err := primitives.ParseTurnID(turnText)
		if err != nil {
			return TurnTarget{}, err
		}
		return TurnTarget{SessionID: sessionID, TurnID: turnID}, nil
	}

	turnID, err := primitives.ParseTurnID(value)
	if err != nil {
		return TurnTarget{}, fmt.Errorf("invalid turn ref %q: must be <turn>, latest, or <session>:<turn|latest>: %w", value, err)
	}
	return reader.resolveBareTurn(turnID)
}

func (reader Reader) resolveBareTurn(turnID primitives.TurnID) (TurnTarget, error) {
	sessions, err := reader.sessions()
	if err != nil {
		return TurnTarget{}, err
	}

	var matches []primitives.SessionID
	for _, sessionID := range sessions {
		events, err := eventlog.Open(reader.MetadataDir).Read(sessionID)
		if err != nil {
			return TurnTarget{}, err
		}
		if turnExists(events, turnID) {
			matches = append(matches, sessionID)
		}
	}
	switch len(matches) {
	case 0:
		return TurnTarget{}, fmt.Errorf("turn %s not found in any session", turnID)
	case 1:
		return TurnTarget{SessionID: matches[0], TurnID: turnID}, nil
	default:
		return TurnTarget{}, fmt.Errorf("turn %s is ambiguous across sessions %s; use <session>:%s", turnID, joinSessionIDs(matches), turnID)
	}
}

func (reader Reader) latestTurn(sessionFilter primitives.SessionID) (TurnTarget, error) {
	var sessions []primitives.SessionID
	var err error
	if sessionFilter != "" {
		sessions = []primitives.SessionID{sessionFilter}
	} else {
		sessions, err = reader.sessions()
		if err != nil {
			return TurnTarget{}, err
		}
	}

	var latest TurnTarget
	var latestTime primitives.Timestamp
	for _, sessionID := range sessions {
		events, err := eventlog.Open(reader.MetadataDir).Read(sessionID)
		if err != nil {
			return TurnTarget{}, err
		}
		for _, event := range events {
			if event.TurnID == nil {
				continue
			}
			if latest.TurnID == 0 || event.Time.Time.After(latestTime.Time) || (event.Time.Time.Equal(latestTime.Time) && tieBreakTurn(sessionID, *event.TurnID, latest)) {
				latest = TurnTarget{SessionID: sessionID, TurnID: *event.TurnID}
				latestTime = event.Time
			}
		}
	}
	if latest.TurnID == 0 {
		if sessionFilter != "" {
			return TurnTarget{}, fmt.Errorf("no turns found for session %s", sessionFilter)
		}
		return TurnTarget{}, fmt.Errorf("no turns found")
	}
	return latest, nil
}

func (reader Reader) sessions() ([]primitives.SessionID, error) {
	dir := eventlog.Open(reader.MetadataDir).Dir
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no event logs found")
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

func turnExists(events []eventlog.Event, turnID primitives.TurnID) bool {
	for _, event := range events {
		if event.TurnID != nil && *event.TurnID == turnID {
			return true
		}
	}
	return false
}

func tieBreakTurn(sessionID primitives.SessionID, turnID primitives.TurnID, current TurnTarget) bool {
	if sessionID != current.SessionID {
		return sessionID.String() > current.SessionID.String()
	}
	return turnID.Uint64() > current.TurnID.Uint64()
}

func joinSessionIDs(sessions []primitives.SessionID) string {
	values := make([]string, 0, len(sessions))
	for _, sessionID := range sessions {
		values = append(values, sessionID.String())
	}
	return strings.Join(values, ",")
}
