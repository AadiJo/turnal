package externaladapters

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	adaptersdk "github.com/AadiJo/turnal/sdk/adapter"
)

const AdapterVersion = "1.0.0"

func Manifest(name string) (adaptersdk.Manifest, bool) {
	displayNames := map[string]string{
		"opencode":    "OpenCode",
		"copilot-cli": "GitHub Copilot CLI",
		"cursor":      "Cursor",
		"pi":          "Pi",
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
	case "copilot-cli":
		return normalizeCopilot, true
	case "cursor":
		return normalizeCursor, true
	case "pi":
		return normalizePi, true
	default:
		return nil, false
	}
}

func normalizeCursor(hook string, raw json.RawMessage) ([]adaptersdk.Event, error) {
	payload, err := decodePayload(raw)
	if err != nil {
		return nil, err
	}
	base := commonEvent(payload)
	hook = normalizedHook(firstNonEmpty(firstString(payload, "hook_event_name", "hookEventName", "event"), hook))
	switch hook {
	case "sessionstart":
		base.Type = adaptersdk.EventSessionStart
		applySessionTopology(&base, payload)
		return []adaptersdk.Event{base}, nil
	case "beforesubmitprompt", "userpromptsubmit":
		base.Type = adaptersdk.EventPromptUser
		base.Text = firstString(payload, "prompt")
		return []adaptersdk.Event{base}, nil
	case "pretooluse":
		base.Type = adaptersdk.EventToolCall
		base.ToolName = firstString(payload, "tool_name", "toolName")
		base.ToolUseID = firstString(payload, "tool_use_id", "toolUseId", "tool_call_id", "toolCallId")
		base.Input = firstJSON(payload, "tool_input", "toolInput")
		return []adaptersdk.Event{base}, nil
	case "posttooluse":
		return []adaptersdk.Event{toolResultEvent(base, payload, []string{"tool_input", "toolInput"}, []string{"tool_output", "toolOutput"})}, nil
	case "posttoolusefailure":
		event := toolResultEvent(base, payload, []string{"tool_input", "toolInput"}, nil)
		event.Output = jsonObject("error", firstString(payload, "error_message", "errorMessage", "failure_type"))
		event.IsError = true
		return []adaptersdk.Event{event}, nil
	case "afterfileedit":
		digest := sha256.Sum256(raw)
		toolUseID := fmt.Sprintf("cursor-file-edit-%x", digest[:12])
		input, err := json.Marshal(map[string]any{
			"file_path": firstString(payload, "file_path", "filePath"),
			"edits":     payload["edits"],
		})
		if err != nil {
			return nil, err
		}
		call := base
		call.Type = adaptersdk.EventToolCall
		call.ToolName = "Write"
		call.ToolUseID = toolUseID
		call.Input = input
		call.MutationAlreadyApplied = true
		result := base
		result.Type = adaptersdk.EventToolResult
		result.ToolName = call.ToolName
		result.ToolUseID = toolUseID
		result.Output = jsonObject("file_path", firstString(payload, "file_path", "filePath"))
		return []adaptersdk.Event{call, result}, nil
	case "afteragentresponse":
		base.Type = adaptersdk.EventAssistantMessage
		base.Text = firstString(payload, "text", "response")
		return []adaptersdk.Event{base}, nil
	case "stop":
		base.Type = adaptersdk.EventTurnFinish
		return []adaptersdk.Event{base}, nil
	case "subagentstart":
		parentSessionID := firstNonEmpty(firstString(payload, "parent_conversation_id", "parentConversationId"), base.SessionID)
		base.Type = adaptersdk.EventSessionStart
		base.SessionID = firstString(payload, "subagent_id", "subagentId")
		base.ParentSessionID = parentSessionID
		if parentSessionID != "" {
			base.ParentToolUseID = firstString(payload, "tool_call_id", "toolCallId")
		}
		base.Model = firstNonEmpty(firstString(payload, "subagent_model", "subagentModel"), base.Model)
		base.TranscriptPath = firstNonEmpty(firstString(payload, "agent_transcript_path", "agentTranscriptPath"), base.TranscriptPath)
		return []adaptersdk.Event{base}, nil
	default:
		return nil, nil
	}
}

func normalizePi(hook string, raw json.RawMessage) ([]adaptersdk.Event, error) {
	payload, err := decodePayload(raw)
	if err != nil {
		return nil, err
	}
	base := commonEvent(payload)
	hook = normalizedHook(firstNonEmpty(firstString(payload, "hook", "event"), hook))
	switch hook {
	case "sessionstart":
		base.Type = adaptersdk.EventSessionStart
		applySessionTopology(&base, payload)
		return []adaptersdk.Event{base}, nil
	case "beforeagentstart":
		base.Type = adaptersdk.EventPromptUser
		base.Text = firstString(payload, "prompt")
		return []adaptersdk.Event{base}, nil
	case "toolexecutionstart":
		base.Type = adaptersdk.EventToolCall
		base.ToolName = firstString(payload, "tool_name", "toolName")
		base.ToolUseID = firstString(payload, "tool_call_id", "toolCallId")
		base.Input = firstJSON(payload, "args", "input")
		return []adaptersdk.Event{base}, nil
	case "toolexecutionend":
		event := toolResultEvent(base, payload, nil, []string{"result", "output"})
		event.IsError = firstBool(payload, "is_error", "isError")
		return []adaptersdk.Event{event}, nil
	case "agentsettled":
		base.Text = firstString(payload, "text", "response")
		base.Type = adaptersdk.EventAssistantMessage
		return []adaptersdk.Event{base}, nil
	default:
		return nil, nil
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
		applySessionTopology(&base, payload)
		base.Text = firstString(payload, "initial_prompt", "initialPrompt")
		return []adaptersdk.Event{base}, nil
	case "userpromptsubmitted", "userpromptsubmit":
		base.Type = adaptersdk.EventPromptUser
		base.Text = firstString(payload, "prompt")
		return []adaptersdk.Event{base}, nil
	case "pretooluse":
		base.Type = adaptersdk.EventToolCall
		base.ToolName = firstString(payload, "tool_name", "toolName")
		base.ToolUseID = firstString(payload, "tool_use_id", "toolUseId", "tool_call_id", "toolCallId")
		base.Input = firstJSON(payload, "tool_input", "toolArgs")
		return []adaptersdk.Event{base}, nil
	case "posttooluse", "posttoolusefailure":
		event := toolResultEvent(base, payload, []string{"tool_input", "toolArgs"}, []string{"tool_result", "toolResult", "error"})
		event.IsError = hook == "posttoolusefailure" || firstBool(payload, "is_error", "isError")
		return []adaptersdk.Event{event}, nil
	case "agentstop", "stop", "sessionend":
		if response, present := firstPresentString(payload, "response", "text", "last_assistant_message"); present {
			base.Type = adaptersdk.EventAssistantMessage
			base.Text = response
			base.TranscriptPath = firstString(payload, "transcript_path", "transcriptPath")
			return []adaptersdk.Event{base}, nil
		}
		base.Type = adaptersdk.EventTurnFinish
		base.TranscriptPath = firstString(payload, "transcript_path", "transcriptPath")
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
		topologyPayload := merged
		if info := childMap(properties, "info"); info != nil {
			topologyPayload = mergeMaps(merged, info)
			applyCommon(&base, topologyPayload)
		}
		applySessionTopology(&base, topologyPayload)
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
	case "usercompleted":
		base.Type = adaptersdk.EventPromptUser
		base.Text = firstString(merged, "text", "prompt")
		return []adaptersdk.Event{base}, nil
	case "assistantcompleted":
		base.Type = adaptersdk.EventAssistantMessage
		base.Text = firstString(merged, "text", "response")
		return []adaptersdk.Event{base}, nil
	case "toolexecutebefore":
		base.Type = adaptersdk.EventToolCall
		base.ToolName = firstString(merged, "tool", "tool_name", "toolName")
		base.ToolUseID = firstString(merged, "callID", "call_id", "tool_use_id", "toolUseId")
		base.Input = firstJSON(merged, "args", "input")
		return []adaptersdk.Event{base}, nil
	case "toolexecuteafter":
		event := toolResultEvent(base, merged, []string{"args", "input"}, []string{"output", "result"})
		event.IsError = firstBool(merged, "is_error", "isError")
		return []adaptersdk.Event{event}, nil
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
	event.SessionID = firstString(payload, "session_id", "sessionId", "sessionID", "conversation_id", "conversationId", "id")
	event.CWD = firstString(payload, "cwd", "directory", "worktree")
	if event.CWD == "" {
		event.CWD = firstStringArray(payload, "workspace_roots", "workspaceRoots")
	}
	event.SourceID = firstString(payload, "source_id", "sourceId", "event_id", "eventId")
	event.ProviderTurnID = firstString(payload, "turn_id", "turnId", "generation_id", "generationId", "message_id", "messageId")
	event.Model = firstString(payload, "model_id", "modelId", "model")
	event.PermissionMode = firstString(payload, "permission_mode", "permissionMode")
	event.TranscriptPath = firstString(payload, "transcript_path", "transcriptPath")
}

func applySessionTopology(event *adaptersdk.Event, payload map[string]any) {
	event.ParentSessionID = firstString(payload, "parent_session_id", "parentSessionId", "parentSessionID")
	if event.ParentSessionID != "" {
		event.ParentToolUseID = firstString(payload, "parent_tool_use_id", "parentToolUseId", "parentToolUseID")
	}
}

func toolResultEvent(base adaptersdk.Event, payload map[string]any, inputKeys, outputKeys []string) adaptersdk.Event {
	base.Type = adaptersdk.EventToolResult
	base.ToolName = firstString(payload, "tool_name", "toolName", "tool")
	base.ToolUseID = firstString(payload, "tool_use_id", "toolUseId", "tool_call_id", "toolCallId", "call_id", "callID")
	if len(inputKeys) > 0 {
		base.Input = firstJSON(payload, inputKeys...)
	}
	base.Output = firstJSON(payload, outputKeys...)
	return base
}

func toolEvents(base adaptersdk.Event, payload map[string]any, inputKeys, outputKeys []string) []adaptersdk.Event {
	base.ToolName = firstString(payload, "tool_name", "toolName", "tool")
	base.ToolUseID = firstString(payload, "tool_use_id", "toolUseId", "call_id", "callID")
	call := base
	call.Type = adaptersdk.EventToolCall
	call.Input = firstJSON(payload, inputKeys...)
	result := base
	result.Type = adaptersdk.EventToolResult
	// Preserve the paired call input so core privacy policy can recognize that
	// an output belongs to a prompt-like `turnal intent` command.
	result.Input = call.Input
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

func firstPresentString(payload map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		if value, ok := payload[key].(string); ok {
			return value, true
		}
	}
	return "", false
}

func firstStringArray(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		values, ok := payload[key].([]any)
		if !ok {
			continue
		}
		for _, value := range values {
			if text, ok := value.(string); ok && text != "" {
				return text
			}
		}
	}
	return ""
}

func firstBool(payload map[string]any, keys ...string) bool {
	for _, key := range keys {
		if value, ok := payload[key].(bool); ok {
			return value
		}
	}
	return false
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

func jsonObject(key string, value any) json.RawMessage {
	encoded, err := json.Marshal(map[string]any{key: value})
	if err != nil {
		return json.RawMessage(`null`)
	}
	return encoded
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
