package index

import (
	"encoding/json"

	eventlog "agent-vcs-again/internal/events"
	"agent-vcs-again/internal/primitives"
)

func SummarizeTurnEvents(events []eventlog.Event) map[uint64]TurnEventSummary {
	summaries := make(map[uint64]TurnEventSummary)
	seenTools := make(map[uint64]map[string]struct{})

	for _, event := range events {
		if event.TurnID == nil {
			continue
		}

		turnKey := event.TurnID.Uint64()
		summary := summaries[turnKey]
		summary.Count++
		if summary.TypeCounts == nil {
			summary.TypeCounts = make(map[primitives.EventType]int)
		}
		summary.TypeCounts[event.Type]++
		if summary.Adapter == "" && event.Adapter != "" {
			summary.Adapter = event.Adapter.String()
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
		case primitives.EventTypeToolCall:
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
