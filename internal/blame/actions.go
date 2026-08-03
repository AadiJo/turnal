package blame

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/provenance"
)

type capturedToolCall struct {
	ToolName       string                     `json:"tool_name"`
	ToolUseID      string                     `json:"tool_use_id"`
	PreSnapshot    *provenance.ActionSnapshot `json:"pre_snapshot"`
	IntentEventSeq *primitives.EventSeq       `json:"intent_event_seq"`
}

type capturedToolResult struct {
	ToolUseID    string                     `json:"tool_use_id"`
	PostSnapshot *provenance.ActionSnapshot `json:"post_snapshot"`
}

type intentStamp struct {
	Seq     primitives.EventSeq
	Payload provenance.IntentPayload
}

type capturedAction struct {
	CallSeq        primitives.EventSeq
	ResultSeq      primitives.EventSeq
	Time           time.Time
	ToolName       string
	Pre            provenance.ActionSnapshot
	Post           provenance.ActionSnapshot
	IntentEventSeq *primitives.EventSeq
	Intent         *intentStamp
	IntentTiming   string
	Changed        bool
}

type changeSegment struct {
	Turn       completeTurn
	PreCommit  primitives.CommitSHA
	PostCommit primitives.CommitSHA
	Action     *capturedAction
}

func (segment changeSegment) origin(path string) Origin {
	origin := turnOrigin(segment.Turn)
	if segment.Action == nil {
		return origin
	}
	origin.ActionTool = segment.Action.ToolName
	if !segment.Action.Time.IsZero() {
		origin.Time = segment.Action.Time
	}
	if segment.Action.Intent != nil {
		attribution := provenance.Attribute(
			segment.Action.Intent.Payload,
			segment.Action.Intent.Seq,
			segment.Action.IntentTiming,
			path,
		)
		origin.Intent = &attribution
	}
	return origin
}

func (engine Engine) changeSegments(turns []completeTurn) ([]changeSegment, []string, error) {
	segments := make([]changeSegment, 0, len(turns))
	var warnings []string
	for _, turn := range turns {
		turnSegments, turnWarnings, err := engine.turnSegments(turn)
		if err != nil {
			return nil, warnings, err
		}
		segments = append(segments, turnSegments...)
		warnings = append(warnings, turnWarnings...)
	}
	return segments, warnings, nil
}

func (engine Engine) turnSegments(turn completeTurn) ([]changeSegment, []string, error) {
	actions, intents, warnings := capturedActions(turn.Records)
	for index := range actions {
		changes, err := engine.Repo.DiffNameStatusCommits(actions[index].Pre.Commit, actions[index].Post.Commit)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("could not inspect action %s in %s:turn:%s: %v", actions[index].ToolName, turn.SessionID, turn.TurnID, err))
			continue
		}
		actions[index].Changed = len(changes) > 0
	}
	associateActionIntents(actions, intents)

	segments := make([]changeSegment, 0, len(actions)*2+1)
	cursor := turn.Pre.Commit
	for index := range actions {
		action := &actions[index]
		segments = append(segments, changeSegment{
			Turn:       turn,
			PreCommit:  cursor,
			PostCommit: action.Pre.Commit,
		})
		segments = append(segments, changeSegment{
			Turn:       turn,
			PreCommit:  action.Pre.Commit,
			PostCommit: action.Post.Commit,
			Action:     action,
		})
		cursor = action.Post.Commit
	}
	segments = append(segments, changeSegment{
		Turn:       turn,
		PreCommit:  cursor,
		PostCommit: turn.Post.Commit,
	})
	return segments, warnings, nil
}

func capturedActions(events []eventlog.Event) ([]capturedAction, []intentStamp, []string) {
	pending := make(map[string]capturedAction)
	var actions []capturedAction
	var intents []intentStamp
	var warnings []string

	for _, event := range events {
		switch event.Type {
		case primitives.EventTypeAgentIntent:
			payload, err := provenance.ParseIntentPayload(event.Payload)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("ignored malformed agent intent at event %s: %v", event.Seq, err))
				continue
			}
			intents = append(intents, intentStamp{Seq: event.Seq, Payload: payload})
		case primitives.EventTypeToolCall:
			var payload capturedToolCall
			if json.Unmarshal(event.Payload, &payload) != nil || payload.ToolUseID == "" || payload.PreSnapshot == nil {
				continue
			}
			pending[payload.ToolUseID] = capturedAction{
				CallSeq:        event.Seq,
				ToolName:       payload.ToolName,
				Pre:            *payload.PreSnapshot,
				IntentEventSeq: payload.IntentEventSeq,
			}
		case primitives.EventTypeToolResult:
			var payload capturedToolResult
			if json.Unmarshal(event.Payload, &payload) != nil || payload.ToolUseID == "" || payload.PostSnapshot == nil {
				continue
			}
			action, ok := pending[payload.ToolUseID]
			if !ok {
				continue
			}
			delete(pending, payload.ToolUseID)
			action.ResultSeq = event.Seq
			action.Time = event.Time.Time
			action.Post = *payload.PostSnapshot
			actions = append(actions, action)
		}
	}

	sort.Slice(actions, func(i, j int) bool { return actions[i].CallSeq < actions[j].CallSeq })
	sort.Slice(intents, func(i, j int) bool { return intents[i].Seq < intents[j].Seq })
	return actions, intents, warnings
}

func associateActionIntents(actions []capturedAction, intents []intentStamp) {
	bySeq := make(map[primitives.EventSeq]*intentStamp, len(intents))
	for index := range intents {
		bySeq[intents[index].Seq] = &intents[index]
	}
	for index := range actions {
		action := &actions[index]
		if action.IntentEventSeq != nil {
			if stamp := bySeq[*action.IntentEventSeq]; stamp != nil && stamp.Seq < action.CallSeq {
				action.Intent = stamp
				action.IntentTiming = provenance.IntentTimingBefore
				continue
			}
		}
		if !action.Changed {
			continue
		}
		limit := primitives.EventSeq(0)
		for next := index + 1; next < len(actions); next++ {
			if actions[next].Changed {
				limit = actions[next].CallSeq
				break
			}
		}
		for stampIndex := range intents {
			stamp := &intents[stampIndex]
			if stamp.Seq <= action.ResultSeq {
				continue
			}
			if limit != 0 && stamp.Seq > limit {
				break
			}
			action.Intent = stamp
			action.IntentTiming = provenance.IntentTimingAfter
			break
		}
	}
}
