package blame

import (
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/provenance"
)

// CompletedTurn is a recorded turn with both checkpoints, in the order blame
// attributes changes. Other commands that walk turn history chronologically
// (bisect, for example) use this so they agree with blame about ordering.
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
	Start   time.Time
	End     time.Time
}

// CompletedTurns lists the current worktree's completed turns in chronological
// order, optionally narrowed to one session.
func (engine Engine) CompletedTurns(sessionFilter primitives.SessionID) ([]CompletedTurn, error) {
	turns, err := engine.completeTurns(sessionFilter, "", 0)
	if err != nil {
		return nil, err
	}
	completed := make([]CompletedTurn, 0, len(turns))
	for _, turn := range turns {
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
		completed = append(completed, CompletedTurn{
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
	return completed, nil
}
