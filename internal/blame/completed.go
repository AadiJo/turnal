package blame

import (
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/provenance"
)

// CompletedTurn is a recorded turn with both checkpoints, in the order blame
// attributes changes. Other commands that walk turn history chronologically
// (bisect, for example) use this so they agree with blame about ordering and
// about which turns overlap in time.
type CompletedTurn struct {
	SessionID primitives.SessionID
	TurnID    primitives.TurnID
	Pre       checkpoint.CheckpointRefInfo
	Post      checkpoint.CheckpointRefInfo
	Adapter   string
	Model     string
	Prompt    string
	ToolNames []string
	// Intents are the agent's recorded statements for this turn, in event order.
	// Malformed statements are dropped rather than reported.
	Intents []provenance.IntentPayload
	// Start and End bound the turn in time using the most precise evidence
	// recorded; a legacy turn without event timestamps may end a second late.
	Start time.Time
	End   time.Time
}

// IncompleteTurn has a pre checkpoint but no post checkpoint: it is still
// running or was abandoned. It may have changed the workspace at any time
// after Start.
type IncompleteTurn struct {
	SessionID primitives.SessionID
	TurnID    primitives.TurnID
	Start     time.Time
}

type History struct {
	Completed  []CompletedTurn
	Incomplete []IncompleteTurn
}

// History lists the current worktree's turns: completed ones in chronological
// order, and incomplete ones that may still be changing the workspace.
func (engine Engine) History() (History, error) {
	observed, err := engine.observeHistory("", "", 0)
	if err != nil {
		return History{}, err
	}
	history := History{
		Completed:  make([]CompletedTurn, 0, len(observed.Complete)),
		Incomplete: make([]IncompleteTurn, 0, len(observed.Incomplete)),
	}
	for _, turn := range observed.Complete {
		var intents []provenance.IntentPayload
		for _, event := range turn.Records {
			if event.Type != primitives.EventTypeAgentIntent {
				continue
			}
			payload, err := provenance.ParseIntentPayload(event.Payload)
			if err != nil {
				continue
			}
			intents = append(intents, payload)
		}
		history.Completed = append(history.Completed, CompletedTurn{
			SessionID: turn.SessionID,
			TurnID:    turn.TurnID,
			Pre:       turn.Pre,
			Post:      turn.Post,
			Adapter:   turn.Events.Adapter,
			Model:     turn.Events.Model,
			Prompt:    turn.Events.Prompt,
			ToolNames: turn.Events.ToolNames,
			Intents:   intents,
			Start:     completeTurnStart(turn),
			End:       completeTurnEnd(turn),
		})
	}
	for _, turn := range observed.Incomplete {
		history.Incomplete = append(history.Incomplete, IncompleteTurn{
			SessionID: turn.SessionID,
			TurnID:    turn.TurnID,
			Start:     incompleteTurnStart(turn),
		})
	}
	return history, nil
}
