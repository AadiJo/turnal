package blame

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/provenance"
)

type capturedToolCall struct {
	ToolName          string                     `json:"tool_name"`
	ToolUseID         string                     `json:"tool_use_id"`
	MutationCandidate bool                       `json:"mutation_candidate"`
	PreSnapshot       *provenance.ActionSnapshot `json:"pre_snapshot"`
	IntentEventSeq    *primitives.EventSeq       `json:"intent_event_seq"`
	IntentCommand     bool                       `json:"intent_command"`
	AgentID           string                     `json:"agent_id"`
	AgentType         string                     `json:"agent_type"`
}

type capturedToolResult struct {
	ToolUseID    string                     `json:"tool_use_id"`
	PostSnapshot *provenance.ActionSnapshot `json:"post_snapshot"`
}

type intentStamp struct {
	Seq                   primitives.EventSeq
	Payload               provenance.IntentPayload
	ClaimedBeforeAnAction bool
	ActorUnresolved       bool
	AgentID               string
	AgentType             string
}

type actionIntentClaim struct {
	IntentEventSeq primitives.EventSeq
	CallSeq        primitives.EventSeq
	AgentID        string
	AgentType      string
}

type capturedAction struct {
	CallSeq        primitives.EventSeq
	ResultSeq      primitives.EventSeq
	Time           time.Time
	ToolName       string
	Pre            provenance.ActionSnapshot
	Post           provenance.ActionSnapshot
	IntentEventSeq *primitives.EventSeq
	IntentCommand  bool
	Intent         *intentStamp
	IntentTiming   provenance.IntentTiming
	Changed        bool
	AgentID        string
	AgentType      string
}

type changeSegment struct {
	Turn       completeTurn
	PreCommit  primitives.CommitSHA
	PostCommit primitives.CommitSHA
	Action     *capturedAction
	Concurrent bool
	Ambiguous  bool
	Baseline   checkpoint.CheckpointRefInfo
}

func (segment changeSegment) origin(paths ...string) Origin {
	if segment.Concurrent {
		return Origin{
			Kind:          "concurrent",
			CheckpointRef: segment.Turn.Post.Ref,
			Commit:        segment.PostCommit,
			Time:          segment.Turn.Post.Time,
		}
	}
	origin := turnOrigin(segment.Turn)
	if segment.Action != nil {
		origin.ActionTool = segment.Action.ToolName
		origin.ActionAgentID = segment.Action.AgentID
		origin.ActionAgentType = segment.Action.AgentType
		if !segment.Action.Time.IsZero() {
			origin.Time = segment.Action.Time
		}
	}
	if segment.Ambiguous {
		origin.Kind = "ambiguous"
		return origin
	}
	if segment.Action == nil {
		return origin
	}
	if segment.Action.Intent != nil {
		attribution := provenance.Attribute(
			segment.Action.Intent.Payload,
			segment.Action.Intent.Seq,
			segment.Action.IntentTiming,
			paths...,
		)
		origin.Intent = &attribution
	}
	return origin
}

func (engine Engine) changeSegments(turns []completeTurn, concurrent concurrentTurnAttribution, collapseConcurrent bool) ([]changeSegment, []string, error) {
	segments := make([]changeSegment, 0, len(turns))
	var warnings []string
	var groups []concurrentTurnGroup
	if collapseConcurrent {
		groups = concurrentTurnGroups(turns)
	}
	groupIndex := 0
	for turnIndex := 0; turnIndex < len(turns); {
		if groupIndex < len(groups) && groups[groupIndex].Start == turnIndex {
			group := groups[groupIndex]
			if !group.OrderKnown {
				return nil, warnings, fmt.Errorf(
					"concurrent turns from sessions %s have no durable checkpoint order; intent-aware blame cannot choose a safe latest snapshot",
					strings.Join(group.Sessions, ", "),
				)
			}
			sessions := append([]string(nil), group.Sessions...)
			baseline := group.Earliest.Pre
			baselineStart := completeTurnStart(group.Earliest)
			for _, member := range group.Members {
				fact := concurrent[completeTurnIdentity(turns[member])]
				if fact.OrderUnknown {
					return nil, warnings, fmt.Errorf(
						"concurrent turns involving %s have no durable checkpoint order; intent-aware blame cannot choose a safe baseline",
						strings.Join(fact.Participants, ", "),
					)
				}
				sessions = appendUniqueStrings(sessions, fact.Participants...)
				if fact.Baseline != nil && (baselineStart.IsZero() || fact.BaselineStart.Before(baselineStart)) {
					baseline = *fact.Baseline
					baselineStart = fact.BaselineStart
				}
			}
			sort.Strings(sessions)
			segments = append(segments, changeSegment{
				Turn:       group.Latest,
				PreCommit:  baseline.Commit,
				PostCommit: group.Latest.Post.Commit,
				Concurrent: true,
				Baseline:   baseline,
			})
			warnings = append(warnings, fmt.Sprintf(
				"concurrent turns from sessions %s overlapped; their changes were attributed as concurrent rather than to one turn or action",
				strings.Join(sessions, ", "),
			))
			turnIndex = group.End + 1
			groupIndex++
			continue
		}
		turn := turns[turnIndex]
		turnSegments, turnWarnings, err := engine.turnSegments(turn)
		if err != nil {
			return nil, warnings, err
		}
		if fact, ok := concurrent[completeTurnIdentity(turn)]; ok && len(fact.Participants) > 0 {
			if fact.OrderUnknown {
				return nil, warnings, fmt.Errorf(
					"turn %s:turn:%s overlaps history with no durable checkpoint order; intent-aware blame cannot choose a safe baseline",
					turn.SessionID,
					turn.TurnID,
				)
			}
			if fact.Baseline != nil && fact.Baseline.Commit != turn.Pre.Commit {
				segments = append(segments, changeSegment{
					Turn:       turn,
					PreCommit:  fact.Baseline.Commit,
					PostCommit: turn.Pre.Commit,
					Concurrent: true,
					Baseline:   *fact.Baseline,
				})
			}
			for index := range turnSegments {
				turnSegments[index].Concurrent = true
			}
			warnings = append(warnings, fmt.Sprintf(
				"turn %s:turn:%s overlapped with another turn from sessions %s; its changes were attributed as concurrent rather than to one turn or action",
				turn.SessionID,
				turn.TurnID,
				strings.Join(fact.Participants, ", "),
			))
		}
		segments = append(segments, turnSegments...)
		warnings = append(warnings, turnWarnings...)
		turnIndex++
	}
	return segments, warnings, nil
}

type concurrentTurnGroup struct {
	Start      int
	End        int
	Earliest   completeTurn
	Latest     completeTurn
	Sessions   []string
	Members    []int
	OrderKnown bool
}

type indexedTurnInterval struct {
	Index int
	Turn  completeTurn
	Start time.Time
	End   time.Time
}

type concurrentTurnFact struct {
	Participants  []string
	Baseline      *checkpoint.CheckpointRefInfo
	BaselineStart time.Time
	OrderUnknown  bool
}

type concurrentTurnAttribution map[string]concurrentTurnFact

func concurrentTurnAttributions(turns []completeTurn, incomplete []incompleteTurn) concurrentTurnAttribution {
	attributions := make(concurrentTurnAttribution)
	for _, group := range concurrentTurnGroups(turns) {
		for _, member := range group.Members {
			key := completeTurnIdentity(turns[member])
			fact := attributions[key]
			fact.Participants = appendUniqueStrings(fact.Participants, otherConcurrentParticipantLabels(turns, group, turns[member])...)
			fact.OrderUnknown = fact.OrderUnknown || !group.OrderKnown
			memberStart := completeTurnStart(turns[member])
			earliestStart := completeTurnStart(group.Earliest)
			if group.OrderKnown && earliestStart.Before(memberStart) {
				setConcurrentBaseline(&fact, group.Earliest.Pre, earliestStart)
			}
			attributions[key] = fact
		}
	}
	for _, open := range incomplete {
		started := incompleteTurnStart(open)
		if started.IsZero() {
			continue
		}
		for _, turn := range turns {
			if sameCompleteIncompleteStream(turn, open) || started.After(completeTurnEnd(turn)) {
				continue
			}
			key := completeTurnIdentity(turn)
			fact := attributions[key]
			participant := open.SessionID.String()
			if open.SessionID == turn.SessionID && open.Pre.StreamID != turn.Pre.StreamID {
				participant = fmt.Sprintf("%s (stream %s)", open.SessionID, open.Pre.StreamID)
			}
			fact.Participants = appendUniqueStrings(fact.Participants, participant)
			turnStart := completeTurnStart(turn)
			if incompleteCompletePreOrderUnknown(open, turn) {
				fact.OrderUnknown = true
			} else if started.Before(turnStart) {
				setConcurrentBaseline(&fact, open.Pre, started)
			}
			attributions[key] = fact
		}
	}
	for key := range attributions {
		fact := attributions[key]
		sort.Strings(fact.Participants)
		attributions[key] = fact
	}
	return attributions
}

func otherConcurrentParticipantLabels(turns []completeTurn, group concurrentTurnGroup, current completeTurn) []string {
	participants := make(map[string]completeTurn)
	sessionCounts := make(map[string]int)
	for _, member := range group.Members {
		turn := turns[member]
		identity := completeTurnParticipantIdentity(turn)
		if _, exists := participants[identity]; exists {
			continue
		}
		participants[identity] = turn
		sessionCounts[turn.SessionID.String()]++
	}

	currentIdentity := completeTurnParticipantIdentity(current)
	labels := make([]string, 0, len(participants))
	for identity, turn := range participants {
		if identity == currentIdentity {
			continue
		}
		label := turn.SessionID.String()
		if sessionCounts[label] > 1 {
			label = fmt.Sprintf("%s (stream %s)", turn.SessionID, turn.Pre.StreamID)
		}
		labels = append(labels, label)
	}
	sort.Strings(labels)
	return labels
}

func setConcurrentBaseline(fact *concurrentTurnFact, baseline checkpoint.CheckpointRefInfo, started time.Time) {
	if fact.Baseline != nil && !started.Before(fact.BaselineStart) {
		return
	}
	copy := baseline
	fact.Baseline = &copy
	fact.BaselineStart = started
}

func incompleteCompletePreOrderUnknown(open incompleteTurn, turn completeTurn) bool {
	if !open.Pre.Time.Equal(turn.Pre.Time) {
		return false
	}
	return !open.PreEventPrecise || !turn.PreEventPrecise || open.PreEvent.Equal(turn.PreEvent)
}

func appendUniqueStrings(values []string, candidates ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(candidates))
	for _, value := range values {
		seen[value] = struct{}{}
	}
	for _, candidate := range candidates {
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		values = append(values, candidate)
	}
	return values
}

func concurrentTurnGroups(turns []completeTurn) []concurrentTurnGroup {
	intervals := make([]indexedTurnInterval, 0, len(turns))
	for index, turn := range turns {
		start, end := completeTurnStart(turn), completeTurnEnd(turn)
		if start.IsZero() || end.IsZero() || start.After(end) {
			continue
		}
		intervals = append(intervals, indexedTurnInterval{Index: index, Turn: turn, Start: start, End: end})
	}
	sort.Slice(intervals, func(i, j int) bool {
		if !intervals[i].Start.Equal(intervals[j].Start) {
			return intervals[i].Start.Before(intervals[j].Start)
		}
		return intervals[i].End.Before(intervals[j].End)
	})

	var groups []concurrentTurnGroup
	for start := 0; start < len(intervals); {
		end := start
		maxPost := intervals[start].End
		for end+1 < len(intervals) && !intervals[end+1].Start.After(maxPost) {
			end++
			if intervals[end].End.After(maxPost) {
				maxPost = intervals[end].End
			}
		}
		if end > start {
			group := buildConcurrentTurnGroup(intervals[start : end+1])
			if len(group.Sessions) > 1 {
				groups = append(groups, group)
			}
		}
		start = end + 1
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Start < groups[j].Start })
	return groups
}

func buildConcurrentTurnGroup(intervals []indexedTurnInterval) concurrentTurnGroup {
	group := concurrentTurnGroup{
		Start:      intervals[0].Index,
		End:        intervals[0].Index,
		Earliest:   intervals[0].Turn,
		Latest:     intervals[0].Turn,
		OrderKnown: true,
	}
	participants := make(map[string]completeTurn)
	sessionCounts := make(map[string]int)
	for _, interval := range intervals {
		group.Start = min(group.Start, interval.Index)
		group.End = max(group.End, interval.Index)
		group.Members = append(group.Members, interval.Index)
		if interval.Start.Before(completeTurnStart(group.Earliest)) {
			group.Earliest = interval.Turn
		}
		identity := completeTurnParticipantIdentity(interval.Turn)
		if _, exists := participants[identity]; !exists {
			participants[identity] = interval.Turn
			sessionCounts[interval.Turn.SessionID.String()]++
		}
	}
	for left := 0; left < len(intervals); left++ {
		for right := left + 1; right < len(intervals); right++ {
			leftTurn, rightTurn := intervals[left].Turn, intervals[right].Turn
			if sameCompleteTurnStream(leftTurn, rightTurn) {
				continue
			}
			preOrderUnknown := leftTurn.Pre.Time.Equal(rightTurn.Pre.Time) &&
				(!leftTurn.PreEventPrecise || !rightTurn.PreEventPrecise || leftTurn.PreEvent.Equal(rightTurn.PreEvent))
			postOrderUnknown := leftTurn.Post.Time.Equal(rightTurn.Post.Time) &&
				(!leftTurn.PostEventPrecise || !rightTurn.PostEventPrecise || leftTurn.PostEvent.Equal(rightTurn.PostEvent))
			if preOrderUnknown || postOrderUnknown {
				group.OrderKnown = false
			}
		}
	}
	sort.Ints(group.Members)
	// Turns are already ordered by the endpoint Compute uses. Matching the
	// largest original index keeps a collapsed concurrent segment's post
	// snapshot identical to the query's latest checkpoint even on time ties.
	for _, interval := range intervals {
		if interval.Index == group.End {
			group.Latest = interval.Turn
			break
		}
	}
	for _, turn := range participants {
		label := turn.SessionID.String()
		if sessionCounts[label] > 1 {
			label = fmt.Sprintf("%s (stream %s)", turn.SessionID, turn.Pre.StreamID)
		}
		group.Sessions = append(group.Sessions, label)
	}
	sort.Strings(group.Sessions)
	return group
}

func completeTurnParticipantIdentity(turn completeTurn) string {
	return turn.Pre.StreamID.String() + "\x00" + turn.SessionID.String()
}

func (segment changeSegment) baselineInfo() checkpoint.CheckpointRefInfo {
	if segment.Baseline.Ref != "" {
		return segment.Baseline
	}
	return segment.Turn.Pre
}

func (engine Engine) turnSegments(turn completeTurn) ([]changeSegment, []string, error) {
	actions, incomplete, intents, warnings := capturedActions(turn.Records)
	var ambiguousPre *primitives.CommitSHA
	if len(incomplete) > 0 {
		firstIncomplete := incomplete[0]
		safeCount := 0
		for safeCount < len(actions) && actions[safeCount].ResultSeq < firstIncomplete.CallSeq {
			safeCount++
		}
		commit := firstIncomplete.Pre.Commit
		if safeCount < len(actions) && actions[safeCount].CallSeq < firstIncomplete.CallSeq {
			commit = actions[safeCount].Pre.Commit
		}
		ambiguousPre = &commit
		actions = actions[:safeCount]
		warnings = append(warnings, fmt.Sprintf(
			"incomplete or overlapping actions in %s:turn:%s were attributed to the turn rather than an individual action",
			turn.SessionID,
			turn.TurnID,
		))
	}
	for index := range actions {
		changes, err := engine.Repo.DiffNameStatusCommits(actions[index].Pre.Commit, actions[index].Post.Commit)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("could not inspect action %s in %s:turn:%s: %v", actions[index].ToolName, turn.SessionID, turn.TurnID, err))
			continue
		}
		actions[index].Changed = len(changes) > 0
	}
	associateActionIntents(actions, intents)
	hasRecordedIntent := len(intents) > 0

	segments := make([]changeSegment, 0, len(actions)*2+1)
	cursor := turn.Pre.Commit
	for index := 0; index < len(actions); {
		groupEnd := index
		lastResult := index
		lastResultSeq := actions[index].ResultSeq
		for groupEnd+1 < len(actions) && actions[groupEnd+1].CallSeq < lastResultSeq {
			groupEnd++
			if actions[groupEnd].ResultSeq > lastResultSeq {
				lastResult = groupEnd
				lastResultSeq = actions[groupEnd].ResultSeq
			}
		}

		action := &actions[index]
		segments = append(segments, changeSegment{
			Turn:       turn,
			PreCommit:  cursor,
			PostCommit: action.Pre.Commit,
			Ambiguous:  hasRecordedIntent,
		})
		if groupEnd > index {
			segments = append(segments, changeSegment{
				Turn:       turn,
				PreCommit:  action.Pre.Commit,
				PostCommit: actions[lastResult].Post.Commit,
				Ambiguous:  true,
			})
			warnings = append(warnings, fmt.Sprintf(
				"overlapping actions in %s:turn:%s were attributed to the turn rather than an individual action",
				turn.SessionID,
				turn.TurnID,
			))
			cursor = actions[lastResult].Post.Commit
			index = groupEnd + 1
			continue
		}
		segments = append(segments, changeSegment{
			Turn:       turn,
			PreCommit:  action.Pre.Commit,
			PostCommit: action.Post.Commit,
			Action:     action,
			Ambiguous:  action.Intent == nil && hasRecordedIntent,
		})
		cursor = action.Post.Commit
		index++
	}
	if ambiguousPre != nil {
		segments = append(segments, changeSegment{
			Turn:       turn,
			PreCommit:  cursor,
			PostCommit: *ambiguousPre,
			Ambiguous:  hasRecordedIntent,
		})
		cursor = *ambiguousPre
		segments = append(segments, changeSegment{
			Turn:       turn,
			PreCommit:  cursor,
			PostCommit: turn.Post.Commit,
			Ambiguous:  true,
		})
		return segments, warnings, nil
	}
	segments = append(segments, changeSegment{Turn: turn, PreCommit: cursor, PostCommit: turn.Post.Commit, Ambiguous: hasRecordedIntent})
	return segments, warnings, nil
}

func capturedActions(events []eventlog.Event) ([]capturedAction, []capturedAction, []intentStamp, []string) {
	pending := make(map[string]capturedAction)
	var actions []capturedAction
	var intents []intentStamp
	var claims []actionIntentClaim
	var warnings []string

	for _, event := range events {
		switch event.Type {
		case primitives.EventTypeAgentIntent:
			payload, err := provenance.ParseIntentPayload(event.Payload)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("ignored malformed agent intent at event %s: %v", event.Seq, err))
				continue
			}
			actorUnresolved := false
			if payload.AgentID == "" {
				var activeIntentCommand *capturedAction
				intentCommandCount := 0
				for _, action := range pending {
					if !action.IntentCommand {
						continue
					}
					intentCommandCount++
					copy := action
					activeIntentCommand = &copy
				}
				if intentCommandCount == 1 {
					payload.AgentID = activeIntentCommand.AgentID
					payload.AgentType = activeIntentCommand.AgentType
				} else if intentCommandCount > 1 {
					actorUnresolved = true
					warnings = append(warnings, fmt.Sprintf("agent intent at event %s could not be tied to one of %d overlapping intent commands", event.Seq, intentCommandCount))
				}
			}
			intents = append(intents, intentStamp{Seq: event.Seq, Payload: payload, ActorUnresolved: actorUnresolved, AgentID: payload.AgentID, AgentType: payload.AgentType})
		case primitives.EventTypeToolCall:
			var payload capturedToolCall
			if json.Unmarshal(event.Payload, &payload) != nil {
				continue
			}
			if (payload.MutationCandidate || payload.PreSnapshot != nil) && payload.IntentEventSeq != nil && *payload.IntentEventSeq < event.Seq {
				claims = append(claims, actionIntentClaim{
					IntentEventSeq: *payload.IntentEventSeq,
					CallSeq:        event.Seq,
					AgentID:        payload.AgentID,
					AgentType:      payload.AgentType,
				})
			}
			if payload.ToolUseID == "" || payload.PreSnapshot == nil {
				continue
			}
			pending[payload.ToolUseID] = capturedAction{
				CallSeq:        event.Seq,
				ToolName:       payload.ToolName,
				Pre:            *payload.PreSnapshot,
				IntentEventSeq: payload.IntentEventSeq,
				IntentCommand:  payload.IntentCommand,
				AgentID:        payload.AgentID,
				AgentType:      payload.AgentType,
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

	intentsBySeq := make(map[primitives.EventSeq]*intentStamp, len(intents))
	for index := range intents {
		intentsBySeq[intents[index].Seq] = &intents[index]
	}
	for _, claim := range claims {
		stamp := intentsBySeq[claim.IntentEventSeq]
		if stamp == nil || !actorsMatch(capturedAction{
			CallSeq:   claim.CallSeq,
			AgentID:   claim.AgentID,
			AgentType: claim.AgentType,
		}, *stamp) {
			continue
		}
		stamp.ClaimedBeforeAnAction = true
	}
	incomplete := make([]capturedAction, 0, len(pending))
	for _, action := range pending {
		incomplete = append(incomplete, action)
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i].CallSeq < actions[j].CallSeq })
	sort.Slice(incomplete, func(i, j int) bool { return incomplete[i].CallSeq < incomplete[j].CallSeq })
	sort.Slice(intents, func(i, j int) bool { return intents[i].Seq < intents[j].Seq })
	return actions, incomplete, intents, warnings
}

func associateActionIntents(actions []capturedAction, intents []intentStamp) {
	bySeq := make(map[primitives.EventSeq]*intentStamp, len(intents))
	for index := range intents {
		bySeq[intents[index].Seq] = &intents[index]
	}
	usedBefore := make(map[primitives.EventSeq]struct{}, len(actions))
	for index := range intents {
		if intents[index].ClaimedBeforeAnAction {
			usedBefore[intents[index].Seq] = struct{}{}
		}
	}
	for index := range actions {
		action := &actions[index]
		if action.IntentEventSeq != nil {
			if stamp := bySeq[*action.IntentEventSeq]; stamp != nil && stamp.Seq < action.CallSeq && actorsMatch(*action, *stamp) {
				action.Intent = stamp
				action.IntentTiming = provenance.IntentTimingBefore
				usedBefore[stamp.Seq] = struct{}{}
			}
		}
	}
	for index := range actions {
		action := &actions[index]
		if action.Intent != nil {
			continue
		}
		for stampIndex := len(intents) - 1; stampIndex >= 0; stampIndex-- {
			stamp := &intents[stampIndex]
			if stamp.Seq >= action.CallSeq || !actorsMatch(*action, *stamp) {
				continue
			}
			action.Intent = stamp
			action.IntentTiming = provenance.IntentTimingBefore
			usedBefore[stamp.Seq] = struct{}{}
			break
		}
	}
	for index := range actions {
		action := &actions[index]
		if action.Intent != nil {
			continue
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
			if _, used := usedBefore[stamp.Seq]; used {
				continue
			}
			if !actorsMatch(*action, *stamp) {
				continue
			}
			action.Intent = stamp
			action.IntentTiming = provenance.IntentTimingAfter
			break
		}
	}
}

func actorsMatch(action capturedAction, stamp intentStamp) bool {
	if stamp.ActorUnresolved || action.AgentID != stamp.AgentID {
		return false
	}
	return action.AgentType == "" || stamp.AgentType == "" || action.AgentType == stamp.AgentType
}
