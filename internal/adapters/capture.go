package adapters

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	agentconfig "github.com/AadiJo/turnal/internal/config"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/filelock"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/turnevents"
	"github.com/AadiJo/turnal/internal/turns"
)

type hookPayload struct {
	SessionID            string          `json:"session_id"`
	TurnID               string          `json:"turn_id"`
	TranscriptPath       string          `json:"transcript_path"`
	CWD                  string          `json:"cwd"`
	HookEventName        string          `json:"hook_event_name"`
	Model                string          `json:"model"`
	PermissionMode       string          `json:"permission_mode"`
	Prompt               string          `json:"prompt"`
	LastAssistantMessage string          `json:"last_assistant_message"`
	ToolName             string          `json:"tool_name"`
	ToolInput            json.RawMessage `json:"tool_input"`
	ToolUseID            string          `json:"tool_use_id"`
	ToolResponse         json.RawMessage `json:"tool_response"`
}

type codexHookPayload struct {
	SessionID            string          `json:"session_id"`
	TranscriptPath       string          `json:"transcript_path"`
	CWD                  string          `json:"cwd"`
	HookEventName        string          `json:"hook_event_name"`
	Model                string          `json:"model"`
	PermissionMode       string          `json:"permission_mode"`
	TurnID               string          `json:"turn_id"`
	Source               string          `json:"source"`
	Prompt               string          `json:"prompt"`
	ToolName             string          `json:"tool_name"`
	ToolUseID            string          `json:"tool_use_id"`
	ToolInput            json.RawMessage `json:"tool_input"`
	ToolResponse         json.RawMessage `json:"tool_response"`
	Trigger              string          `json:"trigger"`
	AgentID              string          `json:"agent_id"`
	AgentType            string          `json:"agent_type"`
	AgentTranscriptPath  string          `json:"agent_transcript_path"`
	StopHookActive       bool            `json:"stop_hook_active"`
	LastAssistantMessage string          `json:"last_assistant_message"`
}

func (payload codexHookPayload) normalize() hookPayload {
	return hookPayload{
		SessionID:            payload.SessionID,
		TurnID:               payload.TurnID,
		TranscriptPath:       payload.TranscriptPath,
		CWD:                  payload.CWD,
		HookEventName:        payload.HookEventName,
		Model:                payload.Model,
		PermissionMode:       payload.PermissionMode,
		Prompt:               payload.Prompt,
		LastAssistantMessage: payload.LastAssistantMessage,
		ToolName:             payload.ToolName,
		ToolInput:            payload.ToolInput,
		ToolUseID:            payload.ToolUseID,
		ToolResponse:         payload.ToolResponse,
	}
}

type sessionPayload struct {
	ProviderSessionID string `json:"provider_session_id"`
	Model             string `json:"model,omitempty"`
	PermissionMode    string `json:"permission_mode,omitempty"`
	TranscriptPath    string `json:"transcript_path,omitempty"`
}

type promptPayload struct {
	Text           string `json:"text"`
	ProviderTurnID string `json:"provider_turn_id,omitempty"`
}

type assistantPayload struct {
	Text           string `json:"text"`
	ProviderTurnID string `json:"provider_turn_id,omitempty"`
}

type toolCallPayload struct {
	ToolName       string          `json:"tool_name"`
	ToolUseID      string          `json:"tool_use_id,omitempty"`
	ProviderTurnID string          `json:"provider_turn_id,omitempty"`
	Input          json.RawMessage `json:"input"`
}

type toolResultPayload struct {
	ToolName       string          `json:"tool_name"`
	ToolUseID      string          `json:"tool_use_id,omitempty"`
	ProviderTurnID string          `json:"provider_turn_id,omitempty"`
	Output         json.RawMessage `json:"output"`
}

type errorPayload struct {
	Hook    string `json:"hook"`
	Message string `json:"message"`
}

func HandleHookPayload(adapter primitives.AdapterName, hookName string, raw []byte) error {
	if repo, sessionID, ok := hookSessionContext(raw); ok {
		unlock, err := AcquireSessionLock(repo, sessionID)
		if err != nil {
			return err
		}
		defer unlock()
		rawRef, recordErr := RecordHookPayload(adapter, hookName, raw)
		if recordErr != nil {
			return recordErr
		}
		if rawRef == "" {
			return nil
		}
		return processHookPayload(adapter, hookName, rawRef, raw, true)
	}
	rawRef, recordErr := RecordHookPayload(adapter, hookName, raw)
	if recordErr != nil {
		return recordErr
	}
	if rawRef == "" {
		return nil
	}
	return processHookPayload(adapter, hookName, rawRef, raw, false)
}

func ProcessHookPayload(adapter primitives.AdapterName, hookName, rawRef string, raw []byte) error {
	return processHookPayload(adapter, hookName, rawRef, raw, false)
}

func processHookPayload(adapter primitives.AdapterName, hookName, rawRef string, raw []byte, sessionLockHeld bool) error {
	parsedAdapter, err := primitives.ParseAdapterName(adapter.String())
	if err != nil {
		return err
	}

	payload, err := decodeHookPayload(parsedAdapter, raw)
	if err != nil {
		return nil
	}
	if hookName == "CodexHook" && payload.HookEventName != "" {
		hookName = payload.HookEventName
	}

	sessionID, err := primitives.ParseSessionID(payload.SessionID)
	if err != nil {
		return nil
	}

	cwd, err := hookWorkspaceCWD(raw)
	if err != nil {
		return err
	}
	root, err := checkpoint.FindRoot(cwd)
	if err != nil {
		return nil
	}
	repo, err := checkpoint.Open(root)
	if err != nil {
		return nil
	}
	effective, _, err := agentconfig.ResolvePath(filepath.Join(repo.MetadataDir, "config.toml"), agentconfig.Overrides{})
	if err != nil {
		return err
	}

	log := repo.EventLog()
	manager := turns.NewManager(repo)
	if !sessionLockHeld {
		unlock, err := AcquireSessionLock(repo, sessionID)
		if err != nil {
			return err
		}
		defer unlock()
	}
	if err := turnevents.RecoverCheckpointJournals(log, repo); err != nil {
		return err
	}
	if err := processHook(log, manager, parsedAdapter, hookName, rawRef, raw, sessionID, payload, effective.Secrets); err != nil {
		_ = appendErrorEvent(log, parsedAdapter, sessionID, rawRef, hookName, err)
		return err
	}
	return nil
}

func AcquireSessionLock(repo *checkpoint.Repo, sessionID primitives.SessionID) (func(), error) {
	lockDir := filepath.Join(repo.TmpDir, "hooks", sessionID.String()+".lock")
	lock, err := filelock.Acquire(lockDir, 30*time.Second)
	if err != nil {
		return nil, err
	}
	return func() { _ = lock.Release() }, nil
}

func hookSessionContext(raw []byte) (*checkpoint.Repo, primitives.SessionID, bool) {
	sessionID := sessionIDFromRawPayload(raw)
	if sessionID == "" {
		return nil, "", false
	}
	cwd, err := hookWorkspaceCWD(raw)
	if err != nil {
		return nil, "", false
	}
	root, err := checkpoint.FindRoot(cwd)
	if err != nil {
		return nil, "", false
	}
	repo, err := checkpoint.Open(root)
	if err != nil {
		return nil, "", false
	}
	return repo, sessionID, true
}

func decodeHookPayload(adapter primitives.AdapterName, raw []byte) (hookPayload, error) {
	if adapter == primitives.AdapterCodex {
		var payload codexHookPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return hookPayload{}, err
		}
		return payload.normalize(), nil
	}

	var payload hookPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return hookPayload{}, err
	}
	return payload, nil
}

func processHook(log eventlog.Log, manager turns.Manager, adapter primitives.AdapterName, hookName, rawRef string, raw []byte, sessionID primitives.SessionID, payload hookPayload, secrets agentconfig.Secrets) error {
	sourceID := sourceIDFor(adapter, hookName, payload.SessionID, payload.TurnID, payload.ToolUseID, raw)
	if seen, err := log.ContainsSourceID(sessionID, sourceID); err != nil {
		return err
	} else if seen {
		return nil
	}

	normalizedHook := normalizeHookName(hookName)
	switch normalizedHook {
	case "sessionstart":
		return appendSessionStart(log, adapter, sessionID, rawRef, sourceID, payload)
	case "userpromptsubmit":
		if err := ensureSessionStarted(log, adapter, sessionID, rawRef, payload); err != nil {
			return err
		}
		turnID, err := startPromptTurn(log, manager, adapter, sessionID, rawRef, payload)
		if err != nil {
			return err
		}
		return appendPrompt(log, adapter, sessionID, turnID, rawRef, sourceID, payload, secrets)
	case "posttooluse":
		if err := ensureSessionStarted(log, adapter, sessionID, rawRef, payload); err != nil {
			return err
		}
		turnID, err := ensureActiveTurn(log, manager, adapter, sessionID, rawRef)
		if err != nil {
			return err
		}
		return appendToolUse(log, adapter, sessionID, turnID, rawRef, sourceID, payload, secrets)
	case "stop":
		if err := ensureSessionStarted(log, adapter, sessionID, rawRef, payload); err != nil {
			return err
		}
		active, ok, err := manager.Active(sessionID)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if err := appendAssistant(log, adapter, sessionID, active.TurnID, rawRef, sourceID, payload, secrets); err != nil {
			return err
		}
		return finishTurn(log, manager, adapter, sessionID, active.TurnID, rawRef)
	default:
		return appendAdapterRawEvent(log, adapter, sessionID, rawRef, sourceID, hookName)
	}
}

func ensureSessionStarted(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID, rawRef string, payload hookPayload) error {
	sourceID := fmt.Sprintf("%s:session:%s", adapter, sessionID)
	seen, err := log.ContainsSourceID(sessionID, sourceID)
	if err != nil {
		return err
	}
	if seen {
		return nil
	}
	started, err := hasSessionStart(log, adapter, sessionID)
	if err != nil {
		return err
	}
	if started {
		return nil
	}
	return appendSessionStart(log, adapter, sessionID, rawRef, sourceID, payload)
}

func hasSessionStart(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID) (bool, error) {
	events, err := log.Read(sessionID)
	if err != nil {
		return false, err
	}
	for _, event := range events {
		if event.Type == primitives.EventTypeSessionStart && event.Adapter == adapter {
			return true, nil
		}
	}
	return false, nil
}

func appendSessionStart(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID, rawRef, sourceID string, payload hookPayload) error {
	return appendPayloadEvent(log, eventlog.AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypeSessionStart,
		Adapter:   adapter,
		SourceID:  sourceID,
		RawRef:    rawRef,
		Payload: mustJSON(sessionPayload{
			ProviderSessionID: payload.SessionID,
			Model:             payload.Model,
			PermissionMode:    payload.PermissionMode,
			TranscriptPath:    payload.TranscriptPath,
		}),
	})
}

func startPromptTurn(log eventlog.Log, manager turns.Manager, adapter primitives.AdapterName, sessionID primitives.SessionID, rawRef string, payload hookPayload) (primitives.TurnID, error) {
	manager = manager.WithCheckpointEvents(adapter, rawRef)
	active, ok, err := manager.Active(sessionID)
	if err != nil {
		return 0, err
	}
	if ok {
		events, err := log.Read(sessionID)
		if err != nil {
			return 0, err
		}
		if !turnHasPrompt(events, active.TurnID) {
			return active.TurnID, nil
		}
		if err := finishTurn(log, manager, adapter, sessionID, active.TurnID, rawRef); err != nil {
			return 0, err
		}
	}

	started, err := manager.Start(sessionID, 0)
	if err != nil {
		return 0, err
	}
	if err := appendTurnStart(log, adapter, sessionID, started.TurnID, rawRef); err != nil {
		return 0, err
	}
	if err := appendCheckpoint(log, adapter, sessionID, started.TurnID, primitives.CheckpointPhasePre, started.Pre, started.GitSync, rawRef); err != nil {
		return 0, err
	}
	return started.TurnID, nil
}

func ensureActiveTurn(log eventlog.Log, manager turns.Manager, adapter primitives.AdapterName, sessionID primitives.SessionID, rawRef string) (primitives.TurnID, error) {
	manager = manager.WithCheckpointEvents(adapter, rawRef)
	active, ok, err := manager.Active(sessionID)
	if err != nil {
		return 0, err
	}
	if ok {
		return active.TurnID, nil
	}
	started, err := manager.Start(sessionID, 0)
	if err != nil {
		return 0, err
	}
	if err := appendTurnStart(log, adapter, sessionID, started.TurnID, rawRef); err != nil {
		return 0, err
	}
	if err := appendCheckpoint(log, adapter, sessionID, started.TurnID, primitives.CheckpointPhasePre, started.Pre, started.GitSync, rawRef); err != nil {
		return 0, err
	}
	return started.TurnID, nil
}

func finishTurn(log eventlog.Log, manager turns.Manager, adapter primitives.AdapterName, sessionID primitives.SessionID, turnID primitives.TurnID, rawRef string) error {
	manager = manager.WithCheckpointEvents(adapter, rawRef)
	finished, err := manager.Finish(sessionID, turnID)
	if err != nil {
		if strings.Contains(err.Error(), "post checkpoint already exists") {
			return nil
		}
		return err
	}
	if err := appendTurnFinish(log, adapter, sessionID, finished.TurnID, rawRef); err != nil {
		return err
	}
	return appendCheckpoint(log, adapter, sessionID, finished.TurnID, primitives.CheckpointPhasePost, finished.Post, finished.GitSync, rawRef)
}

func appendTurnStart(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID, turnID primitives.TurnID, rawRef string) error {
	return turnevents.AppendTurnStart(log, adapter, sessionID, turnID, rawRef)
}

func appendTurnFinish(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID, turnID primitives.TurnID, rawRef string) error {
	return turnevents.AppendTurnFinish(log, adapter, sessionID, turnID, rawRef)
}

func appendCheckpoint(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase, created checkpoint.Checkpoint, gitSync *checkpoint.Snapshot, rawRef string) error {
	return turnevents.AppendCheckpointWithGitSync(log, adapter, sessionID, turnID, phase, created, gitSync, rawRef)
}

func appendPrompt(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID, turnID primitives.TurnID, rawRef, sourceID string, payload hookPayload, secrets agentconfig.Secrets) error {
	return appendPayloadEvent(log, eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypePromptUser,
		Adapter:   adapter,
		SourceID:  sourceID,
		RawRef:    rawRef,
		Payload: mustJSON(promptPayload{
			Text:           redactedText(payload.Prompt, secrets.StorePrompts),
			ProviderTurnID: payload.TurnID,
		}),
	})
}

func appendAssistant(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID, turnID primitives.TurnID, rawRef, sourceID string, payload hookPayload, secrets agentconfig.Secrets) error {
	return appendPayloadEvent(log, eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypeAssistantMessage,
		Adapter:   adapter,
		SourceID:  sourceID,
		RawRef:    rawRef,
		Payload: mustJSON(assistantPayload{
			Text:           redactedText(payload.LastAssistantMessage, secrets.StorePrompts),
			ProviderTurnID: payload.TurnID,
		}),
	})
}

func appendToolUse(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID, turnID primitives.TurnID, rawRef, sourceID string, payload hookPayload, secrets agentconfig.Secrets) error {
	callSourceID := sourceID + ":call"
	if seen, err := log.ContainsSourceID(sessionID, callSourceID); err != nil {
		return err
	} else if !seen {
		if err := appendPayloadEvent(log, eventlog.AppendInput{
			SessionID: sessionID,
			TurnID:    &turnID,
			Type:      primitives.EventTypeToolCall,
			Adapter:   adapter,
			SourceID:  callSourceID,
			RawRef:    rawRef,
			Payload: mustJSON(toolCallPayload{
				ToolName:       payload.ToolName,
				ToolUseID:      payload.ToolUseID,
				ProviderTurnID: payload.TurnID,
				Input:          redactedJSON(payload.ToolInput, secrets.StoreToolIO),
			}),
		}); err != nil {
			return err
		}
	}

	resultSourceID := sourceID + ":result"
	if seen, err := log.ContainsSourceID(sessionID, resultSourceID); err != nil {
		return err
	} else if !seen {
		return appendPayloadEvent(log, eventlog.AppendInput{
			SessionID: sessionID,
			TurnID:    &turnID,
			Type:      primitives.EventTypeToolResult,
			Adapter:   adapter,
			SourceID:  resultSourceID,
			RawRef:    rawRef,
			Payload: mustJSON(toolResultPayload{
				ToolName:       payload.ToolName,
				ToolUseID:      payload.ToolUseID,
				ProviderTurnID: payload.TurnID,
				Output:         redactedJSON(payload.ToolResponse, secrets.StoreToolIO),
			}),
		})
	}
	return nil
}

func appendAdapterRawEvent(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID, rawRef, sourceID, hookName string) error {
	return appendPayloadEvent(log, eventlog.AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypeAdapterRaw,
		Adapter:   adapter,
		SourceID:  sourceID,
		RawRef:    rawRef,
		Payload:   mustJSON(map[string]string{"hook": hookName}),
	})
}

func appendErrorEvent(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID, rawRef, hookName string, cause error) error {
	return appendPayloadEvent(log, eventlog.AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypeError,
		Adapter:   adapter,
		SourceID:  fmt.Sprintf("%s:error:%s:%s", adapter, rawRef, hookName),
		RawRef:    rawRef,
		Payload: mustJSON(errorPayload{
			Hook:    hookName,
			Message: cause.Error(),
		}),
	})
}

func appendPayloadEvent(log eventlog.Log, input eventlog.AppendInput) error {
	if input.SourceID != "" {
		seen, err := log.ContainsSourceID(input.SessionID, input.SourceID)
		if err != nil {
			return err
		}
		if seen {
			return nil
		}
	}
	_, err := log.Append(input)
	return err
}

func turnHasPrompt(events []eventlog.Event, turnID primitives.TurnID) bool {
	for _, event := range events {
		if event.TurnID == nil || *event.TurnID != turnID {
			continue
		}
		if event.Type == primitives.EventTypePromptUser {
			return true
		}
	}
	return false
}

func normalizeHookName(name string) string {
	name = strings.ReplaceAll(name, "_", "")
	name = strings.ReplaceAll(name, "-", "")
	return strings.ToLower(name)
}

func sourceIDFor(adapter primitives.AdapterName, hookName, sessionID, providerTurnID, toolUseID string, raw []byte) string {
	digest := sha256.Sum256(raw)
	parts := []string{adapter.String(), normalizeHookName(hookName), sessionID}
	if providerTurnID != "" {
		parts = append(parts, "turn", providerTurnID)
	}
	if toolUseID != "" {
		parts = append(parts, "tool", toolUseID)
	}
	parts = append(parts, hex.EncodeToString(digest[:]))
	return strings.Join(parts, ":")
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func defaultRawJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`null`)
	}
	return value
}

func redactedText(value string, store bool) string {
	if store {
		return value
	}
	return primitives.SecretsRedactionText
}

func redactedJSON(value json.RawMessage, store bool) json.RawMessage {
	if store {
		return defaultRawJSON(value)
	}
	return json.RawMessage(`{"redacted":true,"policy":"turnal.secrets"}`)
}

func redactRawHookPayload(raw []byte, secrets agentconfig.Secrets) []byte {
	if secrets.StorePrompts && secrets.StoreToolIO {
		return raw
	}
	if !json.Valid(raw) {
		return raw
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return raw
	}
	if !secrets.StorePrompts {
		for _, key := range []string{"prompt", "last_assistant_message"} {
			if _, ok := payload[key]; ok {
				payload[key] = redactedText("", false)
			}
		}
	}
	if !secrets.StoreToolIO {
		for _, key := range []string{"tool_input", "tool_response"} {
			if _, ok := payload[key]; ok {
				payload[key] = map[string]any{"redacted": true, "policy": "turnal.secrets"}
			}
		}
	}
	redacted, err := json.Marshal(payload)
	if err != nil {
		return raw
	}
	return redacted
}
