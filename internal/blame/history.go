package blame

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	queryindex "github.com/AadiJo/turnal/internal/index"
	"github.com/AadiJo/turnal/internal/primitives"
)

type completeTurn struct {
	SessionID           primitives.SessionID
	TurnID              primitives.TurnID
	Pre                 checkpoint.CheckpointRefInfo
	Post                checkpoint.CheckpointRefInfo
	Events              queryindex.TurnEventSummary
	Records             []eventlog.Event
	PreEvent            time.Time
	PostEvent           time.Time
	PreEventPrecise     bool
	PostEventPrecise    bool
	HasCheckpointEvents bool
}

type incompleteTurn struct {
	SessionID           primitives.SessionID
	TurnID              primitives.TurnID
	Pre                 checkpoint.CheckpointRefInfo
	Events              queryindex.TurnEventSummary
	Records             []eventlog.Event
	PreEvent            time.Time
	PreEventPrecise     bool
	HasCheckpointEvents bool
}

type observedHistory struct {
	Complete   []completeTurn
	Incomplete []incompleteTurn
}

type partialTurn struct {
	pre  *checkpoint.CheckpointRefInfo
	post *checkpoint.CheckpointRefInfo
}

func (engine Engine) completeTurns(sessionFilter primitives.SessionID) ([]completeTurn, error) {
	history, err := engine.observeHistory(sessionFilter)
	if err != nil {
		return nil, err
	}
	return history.Complete, nil
}

func (engine Engine) observeHistory(sessionFilter primitives.SessionID) (observedHistory, error) {
	if engine.Repo == nil {
		return observedHistory{}, fmt.Errorf("blame requires checkpoint repo")
	}

	infos, err := engine.Repo.ListAllCheckpointRefInfos()
	if err != nil {
		return observedHistory{}, err
	}

	turnsByKey := make(map[string]*partialTurn)
	sessionSet := make(map[string]primitives.SessionID)
	for _, info := range infos {
		if sessionFilter != "" && info.SessionID != sessionFilter {
			continue
		}
		if engine.Repo.WorktreeID != "" && info.WorktreeID != "" && info.WorktreeID != engine.Repo.WorktreeID {
			continue
		}
		if info.Phase != primitives.CheckpointPhasePre && info.Phase != primitives.CheckpointPhasePost {
			continue
		}

		sessionSet[info.SessionID.String()] = info.SessionID
		key := info.StreamID.String() + ":" + turnKey(info.SessionID, info.TurnID)
		if turnsByKey[key] == nil {
			turnsByKey[key] = &partialTurn{}
		}
		infoCopy := info
		switch info.Phase {
		case primitives.CheckpointPhasePre:
			turnsByKey[key].pre = &infoCopy
		case primitives.CheckpointPhasePost:
			turnsByKey[key].post = &infoCopy
		}
	}

	summaries := make(map[string]map[queryindex.StreamTurnKey]queryindex.TurnEventSummary)
	records := make(map[string]map[queryindex.StreamTurnKey][]eventlog.Event)
	log := eventlog.Open(engine.Repo.MetadataDir)
	for _, sessionID := range sessionSet {
		events, err := log.Read(sessionID)
		if err != nil {
			return observedHistory{}, err
		}
		summaries[sessionID.String()] = queryindex.SummarizeTurnEventsByStream(events)
		records[sessionID.String()] = make(map[queryindex.StreamTurnKey][]eventlog.Event)
		for _, event := range events {
			if event.TurnID == nil {
				continue
			}
			key := queryindex.StreamTurnKey{StreamID: event.StreamID, TurnID: event.TurnID.Uint64()}
			records[sessionID.String()][key] = append(records[sessionID.String()][key], event)
		}
	}

	history := observedHistory{
		Complete:   make([]completeTurn, 0, len(turnsByKey)),
		Incomplete: make([]incompleteTurn, 0),
	}
	for _, partial := range turnsByKey {
		if partial.pre == nil {
			continue
		}
		pre := *partial.pre
		key := queryindex.StreamTurnKey{StreamID: pre.StreamID, TurnID: pre.TurnID.Uint64()}
		turnRecords := records[pre.SessionID.String()][key]
		preEvent, preEventFound, preEventPrecise := checkpointEventTime(turnRecords, pre)
		hasCheckpointEvents := turnHasCheckpointEvent(turnRecords)
		postEvent := time.Time{}
		postEventFound := false
		postEventPrecise := false
		if partial.post != nil {
			postEvent, postEventFound, postEventPrecise = checkpointEventTime(turnRecords, *partial.post)
		}
		if partial.post == nil || (hasCheckpointEvents && (!preEventFound || !postEventFound)) {
			history.Incomplete = append(history.Incomplete, incompleteTurn{
				SessionID:           pre.SessionID,
				TurnID:              pre.TurnID,
				Pre:                 pre,
				Events:              summaries[pre.SessionID.String()][key],
				Records:             turnRecords,
				PreEvent:            preEvent,
				PreEventPrecise:     preEventPrecise,
				HasCheckpointEvents: hasCheckpointEvents,
			})
			continue
		}
		post := *partial.post
		history.Complete = append(history.Complete, completeTurn{
			SessionID:           pre.SessionID,
			TurnID:              pre.TurnID,
			Pre:                 pre,
			Post:                post,
			Events:              summaries[pre.SessionID.String()][key],
			Records:             turnRecords,
			PreEvent:            preEvent,
			PostEvent:           postEvent,
			PreEventPrecise:     preEventPrecise,
			PostEventPrecise:    postEventPrecise,
			HasCheckpointEvents: hasCheckpointEvents,
		})
	}

	sortCompleteTurns(history.Complete)
	sort.Slice(history.Incomplete, func(i, j int) bool {
		left, right := history.Incomplete[i], history.Incomplete[j]
		leftStart, rightStart := incompleteTurnStart(left), incompleteTurnStart(right)
		if !leftStart.Equal(rightStart) {
			return leftStart.Before(rightStart)
		}
		if left.SessionID != right.SessionID {
			return left.SessionID.String() < right.SessionID.String()
		}
		return left.TurnID.Uint64() < right.TurnID.Uint64()
	})

	return history, nil
}

func sortCompleteTurns(turns []completeTurn) {
	sort.Slice(turns, func(i, j int) bool {
		left, right := turns[i], turns[j]
		if left.PostEventPrecise && right.PostEventPrecise && !left.PostEvent.Equal(right.PostEvent) {
			return left.PostEvent.Before(right.PostEvent)
		}
		if !left.Post.Time.Equal(right.Post.Time) {
			return left.Post.Time.Before(right.Post.Time)
		}
		if sameCompleteTurnStream(left, right) && left.TurnID != right.TurnID {
			return left.TurnID.Uint64() < right.TurnID.Uint64()
		}
		leftEnd, rightEnd := completeTurnEnd(left), completeTurnEnd(right)
		if !leftEnd.IsZero() && !rightEnd.IsZero() && !leftEnd.Equal(rightEnd) {
			return leftEnd.Before(rightEnd)
		}
		if left.SessionID != right.SessionID {
			return left.SessionID.String() < right.SessionID.String()
		}
		if left.Pre.StreamID != right.Pre.StreamID {
			return left.Pre.StreamID.String() < right.Pre.StreamID.String()
		}
		if left.TurnID != right.TurnID {
			return left.TurnID.Uint64() < right.TurnID.Uint64()
		}
		return left.Post.Ref.String() < right.Post.Ref.String()
	})
}

func completeTurnStart(turn completeTurn) time.Time {
	if turn.PreEventPrecise {
		return earliestTime(turn.PreEvent, turn.Events.First)
	}
	if turn.HasCheckpointEvents {
		return earliestTime(turn.Pre.Time, turn.PreEvent, turn.Events.First)
	}
	return earliestTime(turn.Pre.Time, turn.Events.First)
}

func completeTurnEnd(turn completeTurn) time.Time {
	if !turn.PostEvent.IsZero() {
		return turn.PostEvent
	}
	if !turn.Events.Last.IsZero() {
		return turn.Events.Last
	}
	if turn.Post.Time.IsZero() {
		return time.Time{}
	}
	// Git commit dates retain only whole seconds. Treat a legacy turn without
	// event timestamps as possibly ending anywhere in that second.
	return turn.Post.Time.Add(time.Second)
}

func incompleteTurnStart(turn incompleteTurn) time.Time {
	if turn.PreEventPrecise {
		return earliestTime(turn.PreEvent, turn.Events.First)
	}
	if turn.HasCheckpointEvents {
		return earliestTime(turn.Pre.Time, turn.PreEvent, turn.Events.First)
	}
	return earliestTime(turn.Pre.Time, turn.Events.First)
}

func earliestTime(values ...time.Time) time.Time {
	var earliest time.Time
	for _, value := range values {
		if value.IsZero() || (!earliest.IsZero() && !value.Before(earliest)) {
			continue
		}
		earliest = value
	}
	return earliest
}

func turnHasCheckpointEvent(records []eventlog.Event) bool {
	for _, event := range records {
		if event.Type == primitives.EventTypeCheckpoint {
			return true
		}
	}
	return false
}

func checkpointEventTime(records []eventlog.Event, info checkpoint.CheckpointRefInfo) (time.Time, bool, bool) {
	for _, event := range records {
		if event.Type != primitives.EventTypeCheckpoint {
			continue
		}
		var payload struct {
			Phase      string `json:"phase"`
			CommitSHA  string `json:"commit_sha"`
			CapturedAt string `json:"captured_at"`
		}
		if json.Unmarshal(event.Payload, &payload) != nil {
			continue
		}
		if payload.Phase == info.Phase.String() && payload.CommitSHA == info.Commit.String() {
			if capturedAt, err := time.Parse(time.RFC3339Nano, payload.CapturedAt); err == nil && !capturedAt.IsZero() {
				return capturedAt, true, true
			}
			return event.Time.Time, true, false
		}
	}
	return time.Time{}, false, false
}

func completeTurnIdentity(turn completeTurn) string {
	return turn.Pre.StreamID.String() + ":" + turnKey(turn.SessionID, turn.TurnID)
}

func sameCompleteTurnStream(left, right completeTurn) bool {
	return sameHistoryStream(left.SessionID, left.Pre.StreamID, right.SessionID, right.Pre.StreamID)
}

func sameCompleteIncompleteStream(complete completeTurn, incomplete incompleteTurn) bool {
	return sameHistoryStream(complete.SessionID, complete.Pre.StreamID, incomplete.SessionID, incomplete.Pre.StreamID)
}

func sameHistoryStream(leftSession primitives.SessionID, leftStream primitives.EventStreamID, rightSession primitives.SessionID, rightStream primitives.EventStreamID) bool {
	return leftSession == rightSession && leftStream == rightStream
}

func turnKey(sessionID primitives.SessionID, turnID primitives.TurnID) string {
	return sessionID.String() + ":" + turnID.String()
}
