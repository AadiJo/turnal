package sessionhistory

import (
	"encoding/json"
	"time"

	"github.com/AadiJo/turnal/internal/primitives"
)

const (
	OriginImported = "imported"
	OriginNative   = "native"
)

type SessionStartPayload struct {
	ProviderSessionID string `json:"provider_session_id"`
	Model             string `json:"model,omitempty"`
	PermissionMode    string `json:"permission_mode,omitempty"`
	TranscriptPath    string `json:"transcript_path,omitempty"`
	Origin            string `json:"origin,omitempty"`
	ReadOnly          bool   `json:"read_only,omitempty"`
}

type ImportPayload struct {
	Origin         string `json:"origin"`
	ReadOnly       bool   `json:"read_only"`
	SourcePath     string `json:"source_path"`
	SourceSHA256   string `json:"source_sha256"`
	SourceModified string `json:"source_modified,omitempty"`
	TurnCount      int    `json:"turn_count"`
}

type AttachmentPayload struct {
	CommitSHA        primitives.CommitSHA `json:"commit_sha"`
	Revision         string               `json:"revision,omitempty"`
	HistoryRewritten bool                 `json:"history_rewritten"`
}

type Attachment struct {
	CommitSHA primitives.CommitSHA `json:"commit_sha"`
	Revision  string               `json:"revision,omitempty"`
	Time      time.Time            `json:"time"`
}

type PromptPayload struct {
	Text           string `json:"text"`
	ProviderTurnID string `json:"provider_turn_id,omitempty"`
	Model          string `json:"model,omitempty"`
	Redacted       bool   `json:"redacted"`
}

type AssistantPayload struct {
	Text           string `json:"text"`
	ProviderTurnID string `json:"provider_turn_id,omitempty"`
	Model          string `json:"model,omitempty"`
	Redacted       bool   `json:"redacted"`
}

type ToolCallPayload struct {
	ToolName       string          `json:"tool_name"`
	ToolUseID      string          `json:"tool_use_id,omitempty"`
	ProviderTurnID string          `json:"provider_turn_id,omitempty"`
	Input          json.RawMessage `json:"input"`
}

type ToolResultPayload struct {
	ToolName       string          `json:"tool_name"`
	ToolUseID      string          `json:"tool_use_id,omitempty"`
	ProviderTurnID string          `json:"provider_turn_id,omitempty"`
	Output         json.RawMessage `json:"output"`
}
