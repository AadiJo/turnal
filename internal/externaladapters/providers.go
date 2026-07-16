package externaladapters

import (
	"encoding/json"
	"fmt"
	"strings"

	adaptersdk "github.com/AadiJo/turnal/sdk/adapter"
)

const AdapterVersion = "1.0.0"

func Manifest(name string) (adaptersdk.Manifest, bool) {
	displayNames := map[string]string{
		"opencode":    "OpenCode",
		"gemini-cli":  "Gemini CLI",
		"copilot-cli": "Copilot CLI",
	}
	display, ok := displayNames[name]
	if !ok {
		return adaptersdk.Manifest{}, false
	}
	return adaptersdk.Manifest{
		Name:             name,
		DisplayName:      display,
		AdapterVersion:   AdapterVersion,
		Provider:         display,
		ProtocolVersions: []int{adaptersdk.ProtocolVersion},
		EventTypes: []string{
			string(adaptersdk.EventSessionStart),
			string(adaptersdk.EventPromptUser),
			string(adaptersdk.EventToolCall),
			string(adaptersdk.EventToolResult),
			string(adaptersdk.EventAssistantMessage),
			string(adaptersdk.EventTurnFinish),
		},
	}, true
}

func Normalizer(name string) (adaptersdk.NormalizeFunc, bool) {
	switch name {
	case "opencode":
		return normalizeOpenCode, true
	case "gemini-cli":
		return normalizeGemini, true
	case "copilot-cli":
		return normalizeCopilot, true
	default:
		return nil, false
	}
}

func normalizeCopilot(hook string, raw json.RawMessage) ([]adaptersdk.Event, error) {
	payload, err := decodePayload(raw)
	if err != nil {
		return nil, err
	}
	base := commonEvent(payload)
	hook = normalizedHook(firstNonEmpty(firstString(payload, "hook_event_name", "hookEventName", "event"), hook))
	switch hook {
	case "sessionstart":
		base.Type = adaptersdk.EventSessionStart
		base.Text = firstString(payload, "initial_prompt", "initialPrompt")
		return []adaptersdk.Event{base}, nil
	case "userpromptsubmitted", "userpromptsubmit":
		base.Type = adaptersdk.EventPromptUser
		base.Text = firstString(payload, "prompt")
		return []adaptersdk.Event{base}, nil
	case "posttooluse", "posttoolusefailure":
		return toolEvents(base, payload, []string{"tool_input", "toolArgs"}, []string{"tool_result", "toolResult", "error"}), nil
	case "agentstop", "stop", "sessionend":
		base.Type = adaptersdk.EventTurnFinish
		base.TranscriptPath = firstString(payload, "transcript_path", "transcriptPath")
		return []adaptersdk.Event{base}, nil
	default:
		return nil, nil
	}
}

func normalizeGemini(hook string, raw json.RawMessage) ([]adaptersdk.Event, error) {
	payload, err := decodePayload(raw)
	if err != nil {
		return nil, err
	}
	base := commonEvent(payload)
	hook = normalizedHook(firstNonEmpty(firstString(payload, "hook_event_name", "hookEventName", "event"), hook))
	switch hook {
	case "sessionstart":
		base.Type = adaptersdk.EventSessionStart
		return []adaptersdk.Event{base}, nil
	case "beforeagent":
		base.Type = adaptersdk.EventPromptUser
		base.Text = firstString(payload, "prompt")
		return []adaptersdk.Event{base}, nil
	case "aftertool":
		return toolEvents(base, payload, []string{"tool_input", "toolInput"}, []string{"tool_response", "toolResponse"}), nil
	case "afteragent":
		base.Type = adaptersdk.EventAssistantMessage
		base.Text = firstString(payload, "prompt_response", "promptResponse", "response")
		return []adaptersdk.Event{base}, nil
	case "sessionend":
		base.Type = adaptersdk.EventTurnFinish
		return []adaptersdk.Event{base}, nil
	default:
		return nil, nil
	}
}

func normalizeOpenCode(hook string, raw json.RawMessage) ([]adaptersdk.Event, error) {
	payload, err := decodePayload(raw)
	if err != nil {
		return nil, err
	}
	// OpenCode's event subscriber supplies {event:{type,properties},directory}
	// while direct tool hooks commonly supply {sessionID,tool,callID,args,...}.
	eventMap := childMap(payload, "event")
	if eventMap == nil {
		eventMap = payload
	}
	properties := childMap(eventMap, "properties")
	if properties == nil {
		properties = eventMap
	}
	merged := mergeMaps(payload, properties)
	base := commonEvent(merged)
	hook = normalizedHook(firstNonEmpty(firstString(eventMap, "type"), firstString(payload, "type", "event"), hook))
	switch hook {
	case "sessioncreated":
		base.Type = adaptersdk.EventSessionStart
		if info := childMap(properties, "info"); info != nil {
			applyCommon(&base, mergeMaps(merged, info))
		}
		return []adaptersdk.Event{base}, nil
	case "messageupdated":
		info := childMap(properties, "info")
		if info == nil {
			info = properties
		}
		applyCommon(&base, mergeMaps(merged, info))
		role := strings.ToLower(firstString(info, "role"))
		base.Text = firstString(info, "text", "content")
		if role == "user" {
			base.Type = adaptersdk.EventPromptUser
			return []adaptersdk.Event{base}, nil
		}
		// Assistant messages can be updated repeatedly while streaming. The
		// session.idle event closes the turn without risking an early post
		// checkpoint from a partial response.
		return nil, nil
	case "toolexecuteafter":
		return toolEvents(base, merged, []string{"args", "input"}, []string{"output", "result"}), nil
	case "sessionidle":
		base.Type = adaptersdk.EventTurnFinish
		return []adaptersdk.Event{base}, nil
	default:
		return nil, nil
	}
}

func commonEvent(payload map[string]any) adaptersdk.Event {
	var event adaptersdk.Event
	applyCommon(&event, payload)
	return event
}

func applyCommon(event *adaptersdk.Event, payload map[string]any) {
	event.SessionID = firstString(payload, "session_id", "sessionId", "sessionID", "id")
	event.CWD = firstString(payload, "cwd", "directory", "worktree")
	event.SourceID = firstString(payload, "source_id", "sourceId", "event_id", "eventId")
	event.ProviderTurnID = firstString(payload, "turn_id", "turnId", "message_id", "messageId")
	event.Model = firstString(payload, "model")
	event.PermissionMode = firstString(payload, "permission_mode", "permissionMode")
	event.TranscriptPath = firstString(payload, "transcript_path", "transcriptPath")
}

func toolEvents(base adaptersdk.Event, payload map[string]any, inputKeys, outputKeys []string) []adaptersdk.Event {
	base.ToolName = firstString(payload, "tool_name", "toolName", "tool")
	base.ToolUseID = firstString(payload, "tool_use_id", "toolUseId", "call_id", "callID")
	call := base
	call.Type = adaptersdk.EventToolCall
	call.Input = firstJSON(payload, inputKeys...)
	result := base
	result.Type = adaptersdk.EventToolResult
	result.Output = firstJSON(payload, outputKeys...)
	return []adaptersdk.Event{call, result}
}

func decodePayload(raw json.RawMessage) (map[string]any, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("decode provider payload: %w", err)
	}
	return payload, nil
}

func firstString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func firstJSON(payload map[string]any, keys ...string) json.RawMessage {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok {
			continue
		}
		encoded, err := json.Marshal(value)
		if err == nil {
			return encoded
		}
	}
	return json.RawMessage(`null`)
}

func childMap(payload map[string]any, key string) map[string]any {
	value, _ := payload[key].(map[string]any)
	return value
}

func mergeMaps(primary, secondary map[string]any) map[string]any {
	merged := make(map[string]any, len(primary)+len(secondary))
	for key, value := range primary {
		merged[key] = value
	}
	for key, value := range secondary {
		merged[key] = value
	}
	return merged
}

func normalizedHook(value string) string {
	value = strings.NewReplacer("_", "", "-", "", ".", "").Replace(value)
	return strings.ToLower(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
