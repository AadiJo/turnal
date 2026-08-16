// Package adapter defines the versioned NDJSON protocol used by external
// Turnal adapters. Adapters translate provider-specific hook payloads into
// normalized events; Turnal remains responsible for persistence and
// checkpoints.
package adapter

import "encoding/json"

const (
	ProtocolName    = "turnal-adapter"
	ProtocolVersion = 1
)

type Method string

const (
	MethodDescribe  Method = "describe"
	MethodNormalize Method = "normalize"
)

type ResponseType string

const (
	ResponseManifest ResponseType = "manifest"
	ResponseEvent    ResponseType = "event"
	ResponseError    ResponseType = "error"
)

// Request is one NDJSON line written by Turnal to an adapter process.
type Request struct {
	Protocol string          `json:"protocol"`
	Version  int             `json:"version"`
	ID       string          `json:"id"`
	Method   Method          `json:"method"`
	Hook     string          `json:"hook,omitempty"`
	Payload  json.RawMessage `json:"payload,omitempty"`
}

// Response is one NDJSON line written by an adapter. A normalize request may
// produce zero or more event responses.
type Response struct {
	Protocol string       `json:"protocol"`
	Version  int          `json:"version"`
	ID       string       `json:"id"`
	Type     ResponseType `json:"type"`
	Manifest *Manifest    `json:"manifest,omitempty"`
	Event    *Event       `json:"event,omitempty"`
	Error    *Error       `json:"error,omitempty"`
}

type Manifest struct {
	Name             string   `json:"name"`
	DisplayName      string   `json:"display_name"`
	AdapterVersion   string   `json:"adapter_version"`
	Provider         string   `json:"provider"`
	ProtocolVersions []int    `json:"protocol_versions"`
	EventTypes       []string `json:"event_types"`
}

type EventType string

const (
	EventSessionStart     EventType = "session.start"
	EventPromptUser       EventType = "prompt.user"
	EventToolCall         EventType = "tool.call"
	EventToolResult       EventType = "tool.result"
	EventAssistantMessage EventType = "assistant.message"
	EventTurnFinish       EventType = "turn.finish"
)

// Event is the provider-neutral boundary. Lifecycle fields live alongside the
// event-specific fields so adapters can be small streaming programs.
type Event struct {
	Type            EventType       `json:"type"`
	SessionID       string          `json:"session_id"`
	ParentSessionID string          `json:"parent_session_id,omitempty"`
	ParentToolUseID string          `json:"parent_tool_use_id,omitempty"`
	CWD             string          `json:"cwd"`
	SourceID        string          `json:"source_id,omitempty"`
	ProviderTurnID  string          `json:"provider_turn_id,omitempty"`
	Model           string          `json:"model,omitempty"`
	PermissionMode  string          `json:"permission_mode,omitempty"`
	TranscriptPath  string          `json:"transcript_path,omitempty"`
	Text            string          `json:"text,omitempty"`
	ToolName        string          `json:"tool_name,omitempty"`
	ToolUseID       string          `json:"tool_use_id,omitempty"`
	Input           json.RawMessage `json:"input,omitempty"`
	Output          json.RawMessage `json:"output,omitempty"`
	IsError         bool            `json:"is_error,omitempty"`
	// MutationAlreadyApplied marks provider events that are emitted only after
	// a workspace mutation. Core then anchors the call to the last durable
	// workspace state instead of snapshotting the already-mutated tree as pre.
	MutationAlreadyApplied bool `json:"mutation_already_applied,omitempty"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func NewRequest(id string, method Method) Request {
	return Request{Protocol: ProtocolName, Version: ProtocolVersion, ID: id, Method: method}
}
