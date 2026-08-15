package adapters

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/AadiJo/turnal/internal/checkpoint"
	agentconfig "github.com/AadiJo/turnal/internal/config"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/filelock"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/provenance"
	"github.com/AadiJo/turnal/internal/runs"
	"github.com/AadiJo/turnal/internal/turnevents"
	"github.com/AadiJo/turnal/internal/turns"
	adaptersdk "github.com/AadiJo/turnal/sdk/adapter"
)

type hookPayload struct {
	SessionID            string          `json:"session_id"`
	ParentSessionID      string          `json:"parent_session_id"`
	ParentToolUseID      string          `json:"parent_tool_use_id"`
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
	ToolError            string          `json:"error"`
	ToolFailed           bool            `json:"is_error"`
	ToolInterrupted      bool            `json:"is_interrupt"`
	ToolDurationMS       int64           `json:"duration_ms"`
	AgentID              string          `json:"agent_id"`
	AgentType            string          `json:"agent_type"`
	IntentCommand        bool            `json:"-"`
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
		AgentID:              payload.AgentID,
		AgentType:            payload.AgentType,
	}
}

type sessionPayload struct {
	ProviderSessionID string `json:"provider_session_id"`
	ParentSessionID   string `json:"parent_session_id,omitempty"`
	ParentToolUseID   string `json:"parent_tool_use_id,omitempty"`
	Model             string `json:"model,omitempty"`
	PermissionMode    string `json:"permission_mode,omitempty"`
	TranscriptPath    string `json:"transcript_path,omitempty"`
}

type promptPayload struct {
	Text           string `json:"text"`
	ProviderTurnID string `json:"provider_turn_id,omitempty"`
	Model          string `json:"model,omitempty"`
	Redacted       bool   `json:"redacted"`
}

type assistantPayload struct {
	Text           string `json:"text"`
	ProviderTurnID string `json:"provider_turn_id,omitempty"`
	Model          string `json:"model,omitempty"`
}

type toolCallPayload struct {
	ToolName          string                     `json:"tool_name"`
	ToolUseID         string                     `json:"tool_use_id,omitempty"`
	ProviderTurnID    string                     `json:"provider_turn_id,omitempty"`
	Input             json.RawMessage            `json:"input"`
	MutationCandidate bool                       `json:"mutation_candidate,omitempty"`
	PreSnapshot       *provenance.ActionSnapshot `json:"pre_snapshot,omitempty"`
	IntentEventSeq    *primitives.EventSeq       `json:"intent_event_seq,omitempty"`
	IntentCommand     bool                       `json:"intent_command,omitempty"`
	AgentID           string                     `json:"agent_id,omitempty"`
	AgentType         string                     `json:"agent_type,omitempty"`
}

type toolResultPayload struct {
	ToolName       string                     `json:"tool_name"`
	ToolUseID      string                     `json:"tool_use_id,omitempty"`
	ProviderTurnID string                     `json:"provider_turn_id,omitempty"`
	Output         json.RawMessage            `json:"output"`
	IsError        bool                       `json:"is_error,omitempty"`
	PostSnapshot   *provenance.ActionSnapshot `json:"post_snapshot,omitempty"`
}

type errorPayload struct {
	Hook    string `json:"hook"`
	Message string `json:"message"`
}

type intentHookOutput struct {
	HookSpecificOutput intentHookSpecificOutput `json:"hookSpecificOutput"`
}

type intentHookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// IntentHookOutput gives supported agents a turn-scoped instruction for
// recording operative intent before they mutate the workspace.
func IntentHookOutput(raw []byte) ([]byte, bool) {
	sessionID := sessionIDFromRawPayload(raw)
	if sessionID == "" {
		return nil, false
	}
	cwd, err := hookWorkspaceCWD(raw)
	if err != nil {
		return nil, false
	}
	context, ok := IntentInstructionForSession(cwd, sessionID.String())
	if !ok {
		return nil, false
	}
	output, err := json.Marshal(intentHookOutput{HookSpecificOutput: intentHookSpecificOutput{
		HookEventName:     "UserPromptSubmit",
		AdditionalContext: context,
	}})
	if err != nil {
		return nil, false
	}
	return append(output, '\n'), true
}

// IntentInstructionForSession returns the same active-turn instruction used by
// built-in hooks. External adapters use it after prompt capture has opened the
// turn, so every provider records intent with the same command and semantics.
func IntentInstructionForSession(cwd, session string) (string, bool) {
	sessionID, err := primitives.ParseSessionID(session)
	if err != nil {
		return "", false
	}
	root, err := checkpoint.FindRoot(cwd)
	if err != nil {
		return "", false
	}
	repo, err := checkpoint.Open(root)
	if err != nil {
		return "", false
	}
	effective, _, err := agentconfig.ResolvePath(filepath.Join(repo.MetadataDir, "config.toml"), agentconfig.Overrides{})
	if err != nil {
		return "", false
	}
	active, ok, err := turns.NewManager(repo).Active(sessionID)
	if err != nil || !ok {
		return "", false
	}
	return fmt.Sprintf(`Turnal records your stated purpose for each file change. Before the first file-changing tool call, and whenever the problem changes, run:
%s intent --session %s --turn %s --problem "<code problem or goal>" [--scope <repo-relative file or directory, e.g. src/retry.go>] [--evidence <event:seq|path:path:line|test:name>]
Describe the defect or goal, not edit steps or hidden reasoning. Repeat --scope and --evidence when needed. This instruction applies to Turnal turn %s.`, effective.Hooks.Command, sessionID, active.TurnID, active.TurnID), true
}

func HandleHookPayload(adapter primitives.AdapterName, hookName string, raw []byte) error {
	return HandleHookPayloadWithRunID(adapter, hookName, raw, "")
}

func HandleHookPayloadWithRunID(adapter primitives.AdapterName, hookName string, raw []byte, runIDText string) error {
	if repo, sessionID, ok := hookSessionContext(raw); ok {
		unlock, err := AcquireSessionLock(repo, sessionID)
		if err != nil {
			return err
		}
		defer unlock()
		forceIntentRedaction, err := hookResultMatchesPriorIntent(repo, adapter, hookName, raw, sessionID)
		if err != nil {
			return err
		}
		rawRef, recordErr := recordHookPayload(adapter, hookName, raw, forceIntentRedaction)
		if recordErr != nil {
			return recordErr
		}
		if rawRef == "" {
			return nil
		}
		if err := processHookPayload(adapter, hookName, rawRef, raw, true); err != nil {
			return err
		}
		reconcileHookRun(repo, adapter, hookName, rawRef, raw, sessionID, runIDText)
		return nil
	}
	rawRef, recordErr := RecordHookPayload(adapter, hookName, raw)
	if recordErr != nil {
		return recordErr
	}
	if rawRef == "" {
		return nil
	}
	if err := processHookPayload(adapter, hookName, rawRef, raw, false); err != nil {
		return err
	}
	if repo, sessionID, ok := hookSessionContext(raw); ok {
		reconcileHookRun(repo, adapter, hookName, rawRef, raw, sessionID, runIDText)
	}
	return nil
}

func hookResultMatchesPriorIntent(repo *checkpoint.Repo, adapter primitives.AdapterName, hookName string, raw []byte, sessionID primitives.SessionID) (bool, error) {
	payload, err := decodeHookPayload(adapter, raw)
	if err != nil {
		return false, nil
	}
	if hookName == "CodexHook" && payload.HookEventName != "" {
		hookName = payload.HookEventName
	}
	normalizedHook := normalizeHookName(hookName)
	if normalizedHook != "posttooluse" && normalizedHook != "posttoolusefailure" {
		return false, nil
	}
	return sessionToolCallIsIntentCommand(repo.EventLog(), adapter, sessionID, payload.ToolUseID, payload.TurnID)
}

func reconcileHookRun(repo *checkpoint.Repo, adapter primitives.AdapterName, hookName, rawRef string, raw []byte, sessionID primitives.SessionID, runIDText string) {
	if strings.TrimSpace(runIDText) == "" {
		return
	}
	runID, err := primitives.ParseRunID(runIDText)
	if err == nil {
		err = runs.LinkCapture(repo, runID, runs.CaptureProvider, sessionID, adapter)
	}
	if err == nil && normalizeHookName(hookName) == "userpromptsubmit" {
		_, decodeErr := decodeHookPayload(adapter, raw)
		if decodeErr != nil {
			err = decodeErr
		} else {
			manager := turns.NewManager(repo)
			active, ok, activeErr := manager.Active(sessionID)
			if activeErr != nil {
				err = activeErr
			} else if !ok {
				err = fmt.Errorf("provider prompt has no active Turnal turn")
			} else {
				_, err = runs.EnsureAttempt(repo, runID, sessionID, active.TurnID, adapter)
			}
		}
	}
	if err != nil {
		_ = appendErrorEvent(repo.EventLog(), adapter, sessionID, rawRef, "run-correlation", err)
	}
}

func ProcessHookPayload(adapter primitives.AdapterName, hookName, rawRef string, raw []byte) error {
	return processHookPayload(adapter, hookName, rawRef, raw, false)
}

// HandleNormalizedEvents applies provider-neutral output from an external
// adapter. Core Turnal still owns raw retention, redaction, serialization,
// durable events, and checkpoints.
func HandleNormalizedEvents(adapter primitives.AdapterName, hookName string, raw []byte, normalized []adaptersdk.Event) error {
	return HandleNormalizedEventsWithRunID(adapter, hookName, raw, normalized, "")
}

// HandleNormalizedEventsWithRunID applies normalized provider events and links
// their session and prompt attempt to a turnal run when the provider inherited
// TURNAL_RUN_ID from a wrapper process.
func HandleNormalizedEventsWithRunID(adapter primitives.AdapterName, hookName string, raw []byte, normalized []adaptersdk.Event, runIDText string) error {
	parsedAdapter, err := primitives.ParseAdapterName(adapter.String())
	if err != nil {
		return err
	}
	if len(normalized) == 0 {
		return nil
	}
	for _, event := range normalized {
		if err := adaptersdk.ValidateEvent(event); err != nil {
			return fmt.Errorf("external adapter event: %w", err)
		}
	}
	sessionID, err := primitives.ParseSessionID(normalized[0].SessionID)
	if err != nil {
		return err
	}
	cwd := normalized[0].CWD
	for _, event := range normalized[1:] {
		eventSession, parseErr := primitives.ParseSessionID(event.SessionID)
		if parseErr != nil || eventSession != sessionID || event.CWD != cwd {
			return fmt.Errorf("external adapter batch must use one session_id and cwd")
		}
	}
	root, err := checkpoint.FindRoot(cwd)
	if err != nil {
		return nil
	}
	repo, err := checkpoint.Open(root)
	if err != nil {
		return nil
	}
	unlock, err := AcquireSessionLock(repo, sessionID)
	if err != nil {
		return err
	}
	defer unlock()
	effective, _, err := agentconfig.ResolvePath(filepath.Join(repo.MetadataDir, "config.toml"), agentconfig.Overrides{})
	if err != nil {
		return err
	}
	log := repo.EventLog()
	if err := turnevents.RecoverCheckpointJournals(log, repo); err != nil {
		return err
	}
	manager := turns.NewManager(repo)
	forceRawIntentRedaction, err := normalizedBatchHasPriorIntentResult(log, parsedAdapter, sessionID, normalized)
	if err != nil {
		return err
	}
	rawRef, err := recordExternalHookPayload(parsedAdapter, hookName, raw, cwd, sessionID, forceRawIntentRedaction)
	if err != nil || rawRef == "" {
		return err
	}
	for index, event := range normalized {
		if event.ParentSessionID != "" {
			parentSessionID, err := primitives.ParseSessionID(event.ParentSessionID)
			if err != nil {
				return err
			}
			event.ParentSessionID = parentSessionID.String()
		}
		if err := processNormalizedEvent(log, manager, parsedAdapter, rawRef, raw, index, sessionID, event, effective); err != nil {
			_ = appendErrorEvent(log, parsedAdapter, sessionID, rawRef, string(event.Type), err)
			return err
		}
	}
	reconcileNormalizedRun(repo, parsedAdapter, rawRef, sessionID, normalized, runIDText)
	return nil
}

func reconcileNormalizedRun(repo *checkpoint.Repo, adapter primitives.AdapterName, rawRef string, sessionID primitives.SessionID, normalized []adaptersdk.Event, runIDText string) {
	if strings.TrimSpace(runIDText) == "" {
		return
	}
	runID, err := primitives.ParseRunID(runIDText)
	if err == nil {
		err = runs.LinkCapture(repo, runID, runs.CaptureProvider, sessionID, adapter)
	}
	hasPrompt := false
	for _, event := range normalized {
		if event.Type == adaptersdk.EventPromptUser {
			hasPrompt = true
			break
		}
	}
	if err == nil && hasPrompt {
		active, ok, activeErr := turns.NewManager(repo).Active(sessionID)
		switch {
		case activeErr != nil:
			err = activeErr
		case !ok:
			err = fmt.Errorf("provider prompt has no active Turnal turn")
		default:
			_, err = runs.EnsureAttempt(repo, runID, sessionID, active.TurnID, adapter)
		}
	}
	if err != nil {
		_ = appendErrorEvent(repo.EventLog(), adapter, sessionID, rawRef, "run-correlation", err)
	}
}

func processNormalizedEvent(log eventlog.Log, manager turns.Manager, adapter primitives.AdapterName, rawRef string, raw []byte, index int, sessionID primitives.SessionID, event adaptersdk.Event, effective agentconfig.Effective) error {
	payload := hookPayload{
		SessionID:            event.SessionID,
		ParentSessionID:      event.ParentSessionID,
		ParentToolUseID:      event.ParentToolUseID,
		TurnID:               event.ProviderTurnID,
		TranscriptPath:       event.TranscriptPath,
		CWD:                  event.CWD,
		Model:                event.Model,
		PermissionMode:       event.PermissionMode,
		Prompt:               event.Text,
		LastAssistantMessage: event.Text,
		ToolName:             event.ToolName,
		ToolInput:            event.Input,
		ToolUseID:            event.ToolUseID,
		ToolResponse:         event.Output,
		ToolFailed:           event.IsError,
	}
	if !effective.Secrets.StorePrompts && (event.Type == adaptersdk.EventToolCall || event.Type == adaptersdk.EventToolResult) && rawContainsIntentCommand(raw, effective.Hooks.Command) {
		// A partial normalized input must not override core's observation that
		// the provider payload contains prompt-like intent arguments. Keep the
		// normalized form only when prompt storage is enabled.
		payload.ToolInput = json.RawMessage(raw)
	}
	sourceID := event.SourceID
	if sourceID == "" {
		sourceID = sourceIDFor(adapter, fmt.Sprintf("%s:%d", event.Type, index), event.SessionID, event.ProviderTurnID, event.ToolUseID, raw)
	} else {
		sourceID = sourceIDFor(adapter, string(event.Type), event.SessionID, event.ProviderTurnID, event.ToolUseID, []byte(sourceID))
	}
	if seen, err := log.ContainsSourceID(sessionID, sourceID); err != nil {
		return err
	} else if seen {
		return nil
	}
	switch event.Type {
	case adaptersdk.EventSessionStart:
		return appendSessionStart(log, adapter, sessionID, rawRef, sourceID, payload)
	case adaptersdk.EventPromptUser:
		if err := ensureSessionStarted(log, adapter, sessionID, rawRef, payload); err != nil {
			return err
		}
		model, err := resolveTurnModel(log, adapter, sessionID, payload.Model)
		if err != nil {
			return err
		}
		payload.Model = model
		turnID, err := startPromptTurn(log, manager, adapter, sessionID, rawRef, payload)
		if err != nil {
			return err
		}
		return appendPrompt(log, adapter, sessionID, turnID, rawRef, sourceID, payload, effective.Secrets)
	case adaptersdk.EventToolCall, adaptersdk.EventToolResult:
		if err := ensureSessionStarted(log, adapter, sessionID, rawRef, payload); err != nil {
			return err
		}
		turnID, err := ensureActiveTurn(log, manager, adapter, sessionID, rawRef)
		if err != nil {
			return err
		}
		if event.Type == adaptersdk.EventToolCall {
			return captureAndAppendToolCall(log, manager.Repo, adapter, sessionID, turnID, rawRef, sourceID, payload, effective)
		}
		payload.IntentCommand, err = sessionToolCallIsIntentCommand(log, adapter, sessionID, payload.ToolUseID, payload.TurnID)
		if err != nil {
			return err
		}
		return captureAndAppendToolResult(log, manager.Repo, adapter, sessionID, turnID, rawRef, sourceID, payload, effective)
	case adaptersdk.EventAssistantMessage, adaptersdk.EventTurnFinish:
		active, ok, err := manager.Active(sessionID)
		if err != nil || !ok {
			return err
		}
		if event.Type == adaptersdk.EventAssistantMessage {
			if err := appendAssistant(log, adapter, sessionID, active.TurnID, rawRef, sourceID, payload, effective.Secrets); err != nil {
				return err
			}
		}
		return finishTurn(log, manager, adapter, sessionID, active.TurnID, rawRef)
	default:
		return fmt.Errorf("unsupported normalized event type %q", event.Type)
	}
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
	if err := processHook(log, manager, parsedAdapter, hookName, rawRef, raw, sessionID, payload, effective); err != nil {
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
	if len(payload.ToolResponse) == 0 && strings.TrimSpace(payload.ToolError) != "" {
		payload.ToolResponse = mustJSON(struct {
			Error       string `json:"error"`
			Interrupted bool   `json:"is_interrupt,omitempty"`
			DurationMS  int64  `json:"duration_ms,omitempty"`
		}{
			Error:       payload.ToolError,
			Interrupted: payload.ToolInterrupted,
			DurationMS:  payload.ToolDurationMS,
		})
	}
	return payload, nil
}

func processHook(log eventlog.Log, manager turns.Manager, adapter primitives.AdapterName, hookName, rawRef string, raw []byte, sessionID primitives.SessionID, payload hookPayload, effective agentconfig.Effective) error {
	sourceID := sourceIDFor(adapter, hookName, payload.SessionID, payload.TurnID, payload.ToolUseID, raw)
	if seen, err := log.ContainsSourceID(sessionID, sourceID); err != nil {
		return err
	} else if seen {
		return nil
	}

	normalizedHook := normalizeHookName(hookName)
	if normalizedHook == "posttoolusefailure" {
		payload.ToolFailed = true
	}
	switch normalizedHook {
	case "sessionstart":
		return appendSessionStart(log, adapter, sessionID, rawRef, sourceID, payload)
	case "userpromptsubmit":
		if err := ensureSessionStarted(log, adapter, sessionID, rawRef, payload); err != nil {
			return err
		}
		model, err := resolveTurnModel(log, adapter, sessionID, payload.Model)
		if err != nil {
			return err
		}
		payload.Model = model
		turnID, err := startPromptTurn(log, manager, adapter, sessionID, rawRef, payload)
		if err != nil {
			return err
		}
		return appendPrompt(log, adapter, sessionID, turnID, rawRef, sourceID, payload, effective.Secrets)
	case "pretooluse":
		if err := ensureSessionStarted(log, adapter, sessionID, rawRef, payload); err != nil {
			return err
		}
		if seen, err := sessionHasToolEvent(log, adapter, sessionID, payload, primitives.EventTypeToolCall); err != nil {
			return err
		} else if seen {
			return nil
		}
		turnID, err := ensureActiveTurn(log, manager, adapter, sessionID, rawRef)
		if err != nil {
			return err
		}
		return captureAndAppendToolCall(log, manager.Repo, adapter, sessionID, turnID, rawRef, sourceID, payload, effective)
	case "posttooluse", "posttoolusefailure":
		if err := ensureSessionStarted(log, adapter, sessionID, rawRef, payload); err != nil {
			return err
		}
		if seen, err := sessionHasToolEvent(log, adapter, sessionID, payload, primitives.EventTypeToolResult); err != nil {
			return err
		} else if seen {
			return nil
		}
		turnID, err := ensureActiveTurn(log, manager, adapter, sessionID, rawRef)
		if err != nil {
			return err
		}
		return captureAndAppendToolResult(log, manager.Repo, adapter, sessionID, turnID, rawRef, sourceID, payload, effective)
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
		if payload.Model = strings.TrimSpace(payload.Model); payload.Model == "" && adapter == primitives.AdapterClaudeCode {
			payload.Model = claudeCompletedTurnModel(payload)
		}
		if err := appendAssistant(log, adapter, sessionID, active.TurnID, rawRef, sourceID, payload, effective.Secrets); err != nil {
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
			ParentSessionID:   payload.ParentSessionID,
			ParentToolUseID:   payload.ParentToolUseID,
			Model:             payload.Model,
			PermissionMode:    payload.PermissionMode,
			TranscriptPath:    payload.TranscriptPath,
		}),
	})
}

// Codex reports the active model on each prompt hook. Claude Code may report it
// on SessionStart, so prompt turns inherit the latest observed session model.
func resolveTurnModel(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID, reported string) (string, error) {
	if reported = strings.TrimSpace(reported); reported != "" {
		return reported, nil
	}
	events, err := log.Read(sessionID)
	if err != nil {
		return "", err
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != primitives.EventTypeSessionStart || event.Adapter != adapter {
			continue
		}
		var payload sessionPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return "", fmt.Errorf("decode session model for %s: %w", sessionID, err)
		}
		if model := strings.TrimSpace(payload.Model); model != "" {
			return model, nil
		}
	}
	return "", nil
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
			Model:          payload.Model,
			Redacted:       !secrets.StorePrompts,
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
			Model:          payload.Model,
		}),
	})
}

func appendToolCall(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID, turnID primitives.TurnID, rawRef, sourceID string, payload hookPayload, effective agentconfig.Effective, preSnapshot *provenance.ActionSnapshot, intentSeq *primitives.EventSeq) error {
	return appendPayloadEvent(log, eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypeToolCall,
		Adapter:   adapter,
		SourceID:  sourceID,
		RawRef:    rawRef,
		Payload: mustJSON(toolCallPayload{
			ToolName:          payload.ToolName,
			ToolUseID:         payload.ToolUseID,
			ProviderTurnID:    payload.TurnID,
			Input:             redactedToolInput(payload.ToolInput, effective),
			MutationCandidate: shouldCaptureAction(payload.ToolName),
			PreSnapshot:       preSnapshot,
			IntentEventSeq:    intentSeq,
			IntentCommand:     rawContainsIntentCommand(payload.ToolInput, effective.Hooks.Command),
			AgentID:           payload.AgentID,
			AgentType:         payload.AgentType,
		}),
	})
}

func appendToolResult(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID, turnID primitives.TurnID, rawRef, sourceID string, payload hookPayload, effective agentconfig.Effective, postSnapshot *provenance.ActionSnapshot) error {
	return appendPayloadEvent(log, eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypeToolResult,
		Adapter:   adapter,
		SourceID:  sourceID,
		RawRef:    rawRef,
		Payload: mustJSON(toolResultPayload{
			ToolName:       payload.ToolName,
			ToolUseID:      payload.ToolUseID,
			ProviderTurnID: payload.TurnID,
			Output:         redactedToolOutput(payload, effective),
			IsError:        payload.ToolFailed,
			PostSnapshot:   postSnapshot,
		}),
	})
}

func captureAndAppendToolCall(log eventlog.Log, repo *checkpoint.Repo, adapter primitives.AdapterName, sessionID primitives.SessionID, turnID primitives.TurnID, rawRef, sourceID string, payload hookPayload, effective agentconfig.Effective) error {
	record := func(snapshot *provenance.ActionSnapshot) error {
		events, err := log.Read(sessionID)
		if err != nil {
			return err
		}
		if turnHasToolCall(events, turnID, payload.ToolUseID) {
			return nil
		}
		intentSeq, err := latestIntentEventSeq(log, sessionID, turnID)
		if err != nil {
			return err
		}
		return appendToolCall(log, adapter, sessionID, turnID, rawRef, sourceID, payload, effective, snapshot, intentSeq)
	}
	if !shouldCaptureAction(payload.ToolName) {
		return record(nil)
	}
	return repo.WithWorkspaceLock("record action pre snapshot", func() error {
		snapshot, err := captureActionSnapshotLocked(repo, sessionID, turnID, payload.ToolUseID, provenance.ActionSnapshotPhasePre)
		if err != nil {
			return err
		}
		return record(snapshot)
	})
}

func captureAndAppendToolResult(log eventlog.Log, repo *checkpoint.Repo, adapter primitives.AdapterName, sessionID primitives.SessionID, turnID primitives.TurnID, rawRef, sourceID string, payload hookPayload, effective agentconfig.Effective) error {
	record := func(snapshot *provenance.ActionSnapshot) error {
		events, err := log.Read(sessionID)
		if err != nil {
			return err
		}
		if !turnHasToolCall(events, turnID, payload.ToolUseID) {
			intentSeq, err := latestIntentEventSeq(log, sessionID, turnID)
			if err != nil {
				return err
			}
			if err := appendToolCall(log, adapter, sessionID, turnID, rawRef, sourceID+":call", payload, effective, nil, intentSeq); err != nil {
				return err
			}
		}
		if turnHasToolResult(events, turnID, payload.ToolUseID) {
			return nil
		}
		payload.IntentCommand = toolCallIsIntentCommand(events, adapter, payload.ToolUseID, payload.TurnID)
		return appendToolResult(log, adapter, sessionID, turnID, rawRef, sourceID, payload, effective, snapshot)
	}
	if !shouldCaptureAction(payload.ToolName) {
		return record(nil)
	}
	return repo.WithWorkspaceLock("record action post snapshot", func() error {
		snapshot, err := captureActionSnapshotLocked(repo, sessionID, turnID, payload.ToolUseID, provenance.ActionSnapshotPhasePost)
		if err != nil {
			return err
		}
		return record(snapshot)
	})
}

func captureActionSnapshotLocked(repo *checkpoint.Repo, sessionID primitives.SessionID, turnID primitives.TurnID, toolUseID string, phase provenance.ActionSnapshotPhase) (*provenance.ActionSnapshot, error) {
	toolUseID = strings.TrimSpace(toolUseID)
	if toolUseID == "" {
		return nil, nil
	}
	streamID, err := repo.StreamID(sessionID)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(repo.WorktreeID.String() + "\x00" + streamID.String() + "\x00" + sessionID.String() + "\x00" + turnID.String() + "\x00" + toolUseID))
	actionID := hex.EncodeToString(digest[:12])
	ref := fmt.Sprintf("refs/agent-vcs/actions/%s/worktree/%s/turn/%s/%s/%s", sessionID, repo.WorktreeID, turnID.RefSegment(), actionID, phase)
	snapshot, err := repo.CreateSnapshotRefIfAbsentLocked(ref, fmt.Sprintf("turnal action %s turn %s %s", sessionID, turnID, phase))
	if err != nil {
		return nil, err
	}
	return &provenance.ActionSnapshot{Ref: snapshot.Ref, Commit: snapshot.Commit}, nil
}

func latestIntentEventSeq(log eventlog.Log, sessionID primitives.SessionID, turnID primitives.TurnID) (*primitives.EventSeq, error) {
	events, err := log.Read(sessionID)
	if err != nil {
		return nil, err
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.TurnID != nil && *event.TurnID == turnID && event.Type == primitives.EventTypeAgentIntent {
			seq := event.Seq
			return &seq, nil
		}
	}
	return nil, nil
}

func turnHasToolCall(events []eventlog.Event, turnID primitives.TurnID, toolUseID string) bool {
	return turnHasToolEvent(events, turnID, toolUseID, primitives.EventTypeToolCall)
}

func turnHasToolResult(events []eventlog.Event, turnID primitives.TurnID, toolUseID string) bool {
	return turnHasToolEvent(events, turnID, toolUseID, primitives.EventTypeToolResult)
}

func turnHasToolEvent(events []eventlog.Event, turnID primitives.TurnID, toolUseID string, eventType primitives.EventType) bool {
	if strings.TrimSpace(toolUseID) == "" {
		return false
	}
	for _, event := range events {
		if event.TurnID == nil || *event.TurnID != turnID || event.Type != eventType {
			continue
		}
		var payload struct {
			ToolUseID string `json:"tool_use_id"`
		}
		if json.Unmarshal(event.Payload, &payload) == nil && payload.ToolUseID == toolUseID {
			return true
		}
	}
	return false
}

func normalizedBatchHasPriorIntentResult(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID, normalized []adaptersdk.Event) (bool, error) {
	events, err := log.Read(sessionID)
	if err != nil {
		return false, err
	}
	for _, event := range normalized {
		if event.Type == adaptersdk.EventToolResult && toolCallIsIntentCommand(events, adapter, event.ToolUseID, event.ProviderTurnID) {
			return true, nil
		}
	}
	return false, nil
}

func sessionToolCallIsIntentCommand(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID, toolUseID, providerTurnID string) (bool, error) {
	events, err := log.Read(sessionID)
	if err != nil {
		return false, err
	}
	return toolCallIsIntentCommand(events, adapter, toolUseID, providerTurnID), nil
}

func toolCallIsIntentCommand(events []eventlog.Event, adapter primitives.AdapterName, toolUseID, providerTurnID string) bool {
	if strings.TrimSpace(toolUseID) == "" {
		return false
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != primitives.EventTypeToolCall || event.Adapter != adapter {
			continue
		}
		var payload toolCallPayload
		if json.Unmarshal(event.Payload, &payload) != nil || payload.ToolUseID != toolUseID {
			continue
		}
		if providerTurnID != "" && payload.ProviderTurnID != "" && payload.ProviderTurnID != providerTurnID {
			continue
		}
		if payload.IntentCommand {
			return true
		}
	}
	return false
}

func sessionHasToolEvent(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID, incoming hookPayload, eventType primitives.EventType) (bool, error) {
	if strings.TrimSpace(incoming.ToolUseID) == "" {
		return false, nil
	}
	events, err := log.Read(sessionID)
	if err != nil {
		return false, err
	}
	for _, event := range events {
		if event.Type != eventType || event.Adapter != adapter {
			continue
		}
		var payload struct {
			ToolUseID      string `json:"tool_use_id"`
			ProviderTurnID string `json:"provider_turn_id"`
		}
		if json.Unmarshal(event.Payload, &payload) != nil || payload.ToolUseID != incoming.ToolUseID {
			continue
		}
		if incoming.TurnID == "" || payload.ProviderTurnID == "" || payload.ProviderTurnID == incoming.TurnID {
			return true, nil
		}
	}
	return false, nil
}

func shouldCaptureAction(toolName string) bool {
	name := strings.ToLower(strings.TrimSpace(toolName))
	if index := strings.LastIndexAny(name, ".:/"); index >= 0 {
		name = name[index+1:]
	}
	switch name {
	case "read", "grep", "glob", "ls", "find", "open", "search_query", "image_query", "screenshot", "view_image", "webfetch", "websearch", "finance", "weather", "sports", "time":
		return false
	default:
		return true
	}
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

func redactedToolInput(value json.RawMessage, effective agentconfig.Effective) json.RawMessage {
	if !effective.Secrets.StoreToolIO {
		return redactedJSON(value, false)
	}
	if !effective.Secrets.StorePrompts && rawContainsIntentCommand(value, effective.Hooks.Command) {
		return json.RawMessage(`{"redacted":true,"policy":"turnal.secrets","content":"agent.intent"}`)
	}
	return defaultRawJSON(value)
}

func redactedToolOutput(payload hookPayload, effective agentconfig.Effective) json.RawMessage {
	if !effective.Secrets.StorePrompts && (payload.IntentCommand || rawContainsIntentCommand(payload.ToolInput, effective.Hooks.Command)) {
		return json.RawMessage(`{"redacted":true,"policy":"turnal.secrets","content":"agent.intent"}`)
	}
	return redactedJSON(payload.ToolResponse, effective.Secrets.StoreToolIO)
}

func redactRawHookPayload(raw []byte, secrets agentconfig.Secrets, hookCommand string, forceIntentResultRedaction bool) []byte {
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
	intentCommand := forceIntentResultRedaction
	if !secrets.StorePrompts {
		for _, key := range []string{"prompt", "last_assistant_message"} {
			if _, ok := payload[key]; ok {
				payload[key] = redactedText("", false)
			}
		}
		if value, ok := payload["tool_input"]; ok && valueContainsIntentCommand(value, hookCommand) {
			intentCommand = true
			payload["tool_input"] = map[string]any{"redacted": true, "policy": "turnal.secrets", "content": "agent.intent"}
		}
		if intentCommand {
			for _, key := range []string{"tool_response", "error"} {
				if _, ok := payload[key]; ok {
					payload[key] = map[string]any{"redacted": true, "policy": "turnal.secrets", "content": "agent.intent"}
				}
			}
		}
	}
	if !secrets.StoreToolIO {
		for _, key := range []string{"tool_input", "tool_response", "error"} {
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

func rawContainsIntentCommand(value json.RawMessage, hookCommand string) bool {
	return textContainsIntentCommand(string(defaultRawJSON(value)), hookCommand)
}

func valueContainsIntentCommand(value any, hookCommand string) bool {
	data, err := json.Marshal(value)
	if err != nil {
		return false
	}
	return textContainsIntentCommand(string(data), hookCommand)

}

func textContainsIntentCommand(value, hookCommand string) bool {
	tokens := intentCommandTokens(value)
	for index := 0; index+1 < len(tokens); index++ {
		if (tokens[index] == "turnal" || tokens[index] == "turnal.exe") && tokens[index+1] == "intent" {
			return true
		}
	}
	commandTokens := intentCommandTokens(hookCommand)
	if len(commandTokens) == 0 || len(tokens) <= len(commandTokens) {
		return false
	}
	for index := 0; index+len(commandTokens) < len(tokens); index++ {
		matches := true
		for commandIndex := range commandTokens {
			if tokens[index+commandIndex] != commandTokens[commandIndex] {
				matches = false
				break
			}
		}
		if matches && tokens[index+len(commandTokens)] == "intent" {
			return true
		}
	}
	return false
}

func intentCommandTokens(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.' && r != '_' && r != '-'
	})
}
