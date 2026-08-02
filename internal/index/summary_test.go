package index

import (
	"encoding/json"
	"testing"

	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
)

func TestSummarizeTurnEventsTracksObservedModelChanges(t *testing.T) {
	turn1, _ := primitives.NewTurnID(1)
	turn2, _ := primitives.NewTurnID(2)
	turn3, _ := primitives.NewTurnID(3)
	events := []eventlog.Event{
		{
			Type:    primitives.EventTypeSessionStart,
			Adapter: primitives.AdapterClaudeCode,
			Payload: json.RawMessage(`{"model":"claude-sonnet-4-6"}`),
		},
		{
			TurnID:  &turn1,
			Type:    primitives.EventTypePromptUser,
			Adapter: primitives.AdapterClaudeCode,
			Payload: json.RawMessage(`{"text":"first"}`),
		},
		{
			Type:    primitives.EventTypeSessionStart,
			Adapter: primitives.AdapterClaudeCode,
			Payload: json.RawMessage(`{"model":"claude-opus-4-1"}`),
		},
		{
			TurnID:  &turn2,
			Type:    primitives.EventTypePromptUser,
			Adapter: primitives.AdapterClaudeCode,
			Payload: json.RawMessage(`{"text":"second"}`),
		},
		{
			TurnID:  &turn3,
			Type:    primitives.EventTypePromptUser,
			Adapter: primitives.AdapterClaudeCode,
			Payload: json.RawMessage(`{"text":"third","model":"claude-haiku-4-5"}`),
		},
	}

	summaries := SummarizeTurnEvents(events)
	if summaries[1].Model != "claude-sonnet-4-6" {
		t.Fatalf("turn 1 model = %q", summaries[1].Model)
	}
	if summaries[2].Model != "claude-opus-4-1" {
		t.Fatalf("turn 2 model = %q", summaries[2].Model)
	}
	if summaries[3].Model != "claude-haiku-4-5" {
		t.Fatalf("turn 3 model = %q", summaries[3].Model)
	}
}
