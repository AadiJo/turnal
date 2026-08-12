package adapter

import (
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

var adapterNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func ValidateRequest(request Request) error {
	if request.Protocol != ProtocolName {
		return fmt.Errorf("protocol must be %q", ProtocolName)
	}
	if request.Version != ProtocolVersion {
		return fmt.Errorf("unsupported protocol version %d", request.Version)
	}
	if strings.TrimSpace(request.ID) == "" {
		return fmt.Errorf("request id is required")
	}
	switch request.Method {
	case MethodDescribe:
		return nil
	case MethodNormalize:
		if strings.TrimSpace(request.Hook) == "" {
			return fmt.Errorf("normalize hook is required")
		}
		if len(request.Payload) == 0 || !json.Valid(request.Payload) {
			return fmt.Errorf("normalize payload must be valid JSON")
		}
		return nil
	default:
		return fmt.Errorf("unknown method %q", request.Method)
	}
}

func ValidateManifest(manifest Manifest) error {
	if !adapterNamePattern.MatchString(manifest.Name) {
		return fmt.Errorf("invalid adapter name %q", manifest.Name)
	}
	if strings.TrimSpace(manifest.DisplayName) == "" || strings.TrimSpace(manifest.Provider) == "" {
		return fmt.Errorf("display_name and provider are required")
	}
	if strings.TrimSpace(manifest.AdapterVersion) == "" {
		return fmt.Errorf("adapter_version is required")
	}
	compatible := false
	for _, version := range manifest.ProtocolVersions {
		if version == ProtocolVersion {
			compatible = true
		}
	}
	if !compatible {
		return fmt.Errorf("adapter does not advertise protocol version %d", ProtocolVersion)
	}
	if len(manifest.EventTypes) == 0 {
		return fmt.Errorf("event_types must not be empty")
	}
	seen := map[EventType]bool{}
	for _, value := range manifest.EventTypes {
		eventType := EventType(value)
		if !validEventType(eventType) {
			return fmt.Errorf("manifest advertises unknown event type %q", value)
		}
		if seen[eventType] {
			return fmt.Errorf("manifest repeats event type %q", value)
		}
		seen[eventType] = true
	}
	return nil
}

func ValidateEvent(event Event) error {
	if !validEventType(event.Type) {
		return fmt.Errorf("unknown event type %q", event.Type)
	}
	if !sessionIDPattern.MatchString(strings.TrimSpace(event.SessionID)) {
		return fmt.Errorf("invalid session_id %q", event.SessionID)
	}
	if !isAbsolutePath(event.CWD) {
		return fmt.Errorf("cwd must be an absolute path")
	}
	if event.ParentSessionID != "" {
		if event.Type != EventSessionStart {
			return fmt.Errorf("parent_session_id is only valid on session.start")
		}
		if !sessionIDPattern.MatchString(strings.TrimSpace(event.ParentSessionID)) {
			return fmt.Errorf("invalid parent_session_id %q", event.ParentSessionID)
		}
		if strings.EqualFold(strings.TrimSpace(event.ParentSessionID), strings.TrimSpace(event.SessionID)) {
			return fmt.Errorf("parent_session_id must differ from session_id")
		}
	}
	if event.ParentToolUseID != "" && event.ParentSessionID == "" {
		return fmt.Errorf("parent_tool_use_id requires parent_session_id")
	}
	switch event.Type {
	case EventToolCall:
		if event.ToolName == "" {
			return fmt.Errorf("tool.call requires tool_name")
		}
		if len(event.Input) > 0 && !json.Valid(event.Input) {
			return fmt.Errorf("tool.call input must be valid JSON")
		}
	case EventToolResult:
		if event.ToolName == "" {
			return fmt.Errorf("tool.result requires tool_name")
		}
		if len(event.Output) > 0 && !json.Valid(event.Output) {
			return fmt.Errorf("tool.result output must be valid JSON")
		}
	}
	return nil
}

func isAbsolutePath(value string) bool {
	if filepath.IsAbs(value) || path.IsAbs(value) {
		return true
	}
	// filepath.IsAbs only recognizes paths for the current GOOS. The wire
	// protocol and its fixtures must also be inspectable on other platforms.
	if len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' && (value[2] == '\\' || value[2] == '/') {
		return true
	}
	return strings.HasPrefix(value, `\\`)
}

func validEventType(eventType EventType) bool {
	switch eventType {
	case EventSessionStart, EventPromptUser, EventToolCall, EventToolResult, EventAssistantMessage, EventTurnFinish:
		return true
	default:
		return false
	}
}
