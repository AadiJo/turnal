package blame

import (
	"fmt"
	"sort"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	queryindex "github.com/AadiJo/turnal/internal/index"
	"github.com/AadiJo/turnal/internal/primitives"
)

type completeTurn struct {
	SessionID primitives.SessionID
	TurnID    primitives.TurnID
	Pre       checkpoint.CheckpointRefInfo
	Post      checkpoint.CheckpointRefInfo
	Events    queryindex.TurnEventSummary
}

type partialTurn struct {
	pre  *checkpoint.CheckpointRefInfo
	post *checkpoint.CheckpointRefInfo
}

func (engine Engine) completeTurns(sessionFilter primitives.SessionID) ([]completeTurn, error) {
	if engine.Repo == nil {
		return nil, fmt.Errorf("blame requires checkpoint repo")
	}

	infos, err := engine.Repo.ListAllCheckpointRefInfos()
	if err != nil {
		return nil, err
	}

	turnsByKey := make(map[string]*partialTurn)
	sessionSet := make(map[string]primitives.SessionID)
	for _, info := range infos {
		if sessionFilter != "" && info.SessionID != sessionFilter {
			continue
		}
		if info.Phase != primitives.CheckpointPhasePre && info.Phase != primitives.CheckpointPhasePost {
			continue
		}

		sessionSet[info.SessionID.String()] = info.SessionID
		key := turnKey(info.SessionID, info.TurnID)
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

	summaries := make(map[string]map[uint64]queryindex.TurnEventSummary)
	log := eventlog.Open(engine.Repo.MetadataDir)
	for _, sessionID := range sessionSet {
		events, err := log.Read(sessionID)
		if err != nil {
			return nil, err
		}
		summaries[sessionID.String()] = queryindex.SummarizeTurnEvents(events)
	}

	turns := make([]completeTurn, 0, len(turnsByKey))
	for _, partial := range turnsByKey {
		if partial.pre == nil || partial.post == nil {
			continue
		}
		pre := *partial.pre
		post := *partial.post
		turns = append(turns, completeTurn{
			SessionID: pre.SessionID,
			TurnID:    pre.TurnID,
			Pre:       pre,
			Post:      post,
			Events:    summaries[pre.SessionID.String()][pre.TurnID.Uint64()],
		})
	}

	sort.Slice(turns, func(i, j int) bool {
		left, right := turns[i], turns[j]
		if !left.Post.Time.Equal(right.Post.Time) {
			return left.Post.Time.Before(right.Post.Time)
		}
		if left.SessionID != right.SessionID {
			return left.SessionID.String() < right.SessionID.String()
		}
		if left.TurnID != right.TurnID {
			return left.TurnID.Uint64() < right.TurnID.Uint64()
		}
		return left.Post.Ref.String() < right.Post.Ref.String()
	})

	return turns, nil
}

func turnKey(sessionID primitives.SessionID, turnID primitives.TurnID) string {
	return sessionID.String() + ":" + turnID.String()
}
