package index

import (
	"encoding/json"

	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
)

type StreamTurnKey struct {
	StreamID primitives.EventStreamID
	TurnID   uint64
}

func SummarizeTurnEventsByStream(events []eventlog.Event) map[StreamTurnKey]TurnEventSummary {
	summaries := make(map[StreamTurnKey]TurnEventSummary)
	seenTools := make(map[StreamTurnKey]map[string]struct{})
	models := make(map[streamAdapterKey]string)
	for _, event := range events {
		modelKey := streamAdapterKey{StreamID: event.StreamID, Adapter: event.Adapter}
		if event.Type == primitives.EventTypeSessionStart {
			if model := payloadString(event.Payload, "model"); model != "" {
				models[modelKey] = model
			}
			continue
		}
		if event.TurnID == nil {
			continue
		}
		key := StreamTurnKey{StreamID: event.StreamID, TurnID: event.TurnID.Uint64()}
		summary := summaries[key]
		applyEventSummary(&summary, event, models[modelKey], seenTools[key])
		if seenTools[key] == nil {
			seenTools[key] = make(map[string]struct{})
		}
		if event.Type == primitives.EventTypeToolCall {
			toolName := payloadString(event.Payload, "tool_name")
			if toolName != "" {
				if _, ok := seenTools[key][toolName]; !ok {
					seenTools[key][toolName] = struct{}{}
					summary.ToolNames = append(summary.ToolNames, toolName)
				}
			}
		}
		summaries[key] = summary
	}
	return summaries
}

func SummarizeTurnEvents(events []eventlog.Event) map[uint64]TurnEventSummary {
	summaries := make(map[uint64]TurnEventSummary)
	seenTools := make(map[uint64]map[string]struct{})
	models := make(map[primitives.AdapterName]string)

	for _, event := range events {
		if event.Type == primitives.EventTypeSessionStart {
			if model := payloadString(event.Payload, "model"); model != "" {
				models[event.Adapter] = model
			}
			continue
		}
		if event.TurnID == nil {
			continue
		}

		turnKey := event.TurnID.Uint64()
		summary := summaries[turnKey]
		applyEventSummary(&summary, event, models[event.Adapter], seenTools[turnKey])
		if event.Type == primitives.EventTypeToolCall {
			toolName := payloadString(event.Payload, "tool_name")
			if toolName != "" {
				if seenTools[turnKey] == nil {
					seenTools[turnKey] = make(map[string]struct{})
				}
				if _, ok := seenTools[turnKey][toolName]; !ok {
					seenTools[turnKey][toolName] = struct{}{}
					summary.ToolNames = append(summary.ToolNames, toolName)
				}
			}
		}

		summaries[turnKey] = summary
	}

	return summaries
}

type streamAdapterKey struct {
	StreamID primitives.EventStreamID
	Adapter  primitives.AdapterName
}

func applyEventSummary(summary *TurnEventSummary, event eventlog.Event, inheritedModel string, _ map[string]struct{}) {
	summary.Count++
	if summary.TypeCounts == nil {
		summary.TypeCounts = make(map[primitives.EventType]int)
	}
	summary.TypeCounts[event.Type]++
	if summary.Adapter == "" && event.Adapter != "" {
		summary.Adapter = event.Adapter.String()
	}
	if model := payloadString(event.Payload, "model"); model != "" {
		summary.Model = model
	} else if summary.Model == "" {
		summary.Model = inheritedModel
	}
	if summary.First.IsZero() || event.Time.Time.Before(summary.First) {
		summary.First = event.Time.Time
	}
	if summary.Last.IsZero() || event.Time.Time.After(summary.Last) {
		summary.Last = event.Time.Time
	}
	switch event.Type {
	case primitives.EventTypePromptUser:
		if summary.Prompt == "" {
			summary.Prompt = payloadString(event.Payload, "text")
		}
	case primitives.EventTypeAssistantMessage:
		if summary.Assistant == "" {
			summary.Assistant = payloadString(event.Payload, "text")
		}
	}
}

func payloadString(payload json.RawMessage, key string) string {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		return ""
	}

	raw, ok := object[key]
	if !ok {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}
