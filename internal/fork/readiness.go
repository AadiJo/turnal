package fork

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/recall"
)

const reportVersion = 1

type Readiness string

const (
	ReadinessReady            Readiness = "ready"
	ReadinessNeedsContext     Readiness = "needs_context"
	ReadinessNeedsInstruction Readiness = "needs_instruction"
	ReadinessUnavailable      Readiness = "unavailable"
)

type InstructionStatus string

const (
	InstructionAvailable InstructionStatus = "available"
	InstructionRedacted  InstructionStatus = "redacted"
	InstructionMissing   InstructionStatus = "missing"
)

type Condition struct {
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type Conditions struct {
	WorkspaceFiles      Condition `json:"workspace_files"`
	WorkspaceVCS        Condition `json:"workspace_vcs"`
	ConversationContext Condition `json:"conversation_context"`
	Toolchain           Condition `json:"toolchain"`
	Secrets             Condition `json:"secrets"`
	Network             Condition `json:"network"`
	Evaluators          Condition `json:"evaluators"`
}

type Source struct {
	SessionID       primitives.SessionID     `json:"session_id"`
	TurnID          primitives.TurnID        `json:"turn_id"`
	WorktreeID      primitives.WorktreeID    `json:"worktree_id,omitempty"`
	StreamID        primitives.EventStreamID `json:"stream_id,omitempty"`
	Adapters        []primitives.AdapterName `json:"adapters,omitempty"`
	MetadataAdapter primitives.AdapterName   `json:"metadata_adapter,omitempty"`
	Model           string                   `json:"model,omitempty"`
	PermissionMode  string                   `json:"permission_mode,omitempty"`
	Complete        bool                     `json:"complete"`
}

type Base struct {
	Status        string                     `json:"status"`
	Phase         primitives.CheckpointPhase `json:"phase"`
	Ref           primitives.CheckpointRef   `json:"ref,omitempty"`
	CommitSHA     primitives.CommitSHA       `json:"commit_sha,omitempty"`
	CapturedFiles int                        `json:"captured_files"`
}

type Instruction struct {
	Status  InstructionStatus      `json:"status"`
	Text    string                 `json:"text,omitempty"`
	Adapter primitives.AdapterName `json:"adapter,omitempty"`
}

type Report struct {
	Version       int         `json:"version"`
	Target        string      `json:"target"`
	Readiness     Readiness   `json:"readiness"`
	FidelityLevel string      `json:"fidelity_level"`
	Source        Source      `json:"source"`
	Base          Base        `json:"base"`
	Instruction   Instruction `json:"instruction"`
	Conditions    Conditions  `json:"conditions"`
	Limitations   []string    `json:"limitations"`
}

type Analyzer struct {
	Repo *checkpoint.Repo
}

func NewAnalyzer(repo *checkpoint.Repo) Analyzer {
	return Analyzer{Repo: repo}
}

func (analyzer Analyzer) Inspect(sessionID primitives.SessionID, turnID primitives.TurnID) (Report, error) {
	if analyzer.Repo == nil {
		return Report{}, fmt.Errorf("fork readiness requires checkpoint repo")
	}
	reader := recall.NewScopedReader(analyzer.Repo.MetadataDir, analyzer.Repo.WorktreeID)
	turn, err := reader.RecallTurn(sessionID, turnID, recall.Options{WorktreeID: analyzer.Repo.WorktreeID})
	if err != nil {
		return Report{}, err
	}
	instruction, err := inspectInstruction(turn.Events)
	if err != nil {
		return Report{}, fmt.Errorf("fork readiness integrity failed: %w", err)
	}
	metadataAdapter := metadataAdapterFor(instruction, turn.Adapters)
	model, permissionMode, hasMetadata, err := sessionMetadata(turn.SessionEvents, metadataAdapter)
	if err != nil {
		return Report{}, fmt.Errorf("fork readiness integrity failed: %w", err)
	}
	if turn.PreCheckpoint != nil {
		if err := verifyCheckpoint(analyzer.Repo, *turn.PreCheckpoint, "pre-turn"); err != nil {
			return Report{}, err
		}
	}
	if turn.PostCheckpoint != nil {
		if err := verifyCheckpoint(analyzer.Repo, *turn.PostCheckpoint, "post-turn"); err != nil {
			return Report{}, err
		}
	}

	report := Report{
		Version:       reportVersion,
		Target:        fmt.Sprintf("%s:turn:%s:pre", turn.SessionID, turn.TurnID),
		Readiness:     ReadinessUnavailable,
		FidelityLevel: "L0",
		Source: Source{
			SessionID:  turn.SessionID,
			TurnID:     turn.TurnID,
			WorktreeID: turn.WorktreeID,
			StreamID:   turn.StreamID,
			Adapters:   turn.Adapters,
			Complete:   turn.Complete,
		},
		Base: Base{
			Status: "missing",
			Phase:  primitives.CheckpointPhasePre,
		},
		Instruction: instruction,
		Conditions: Conditions{
			WorkspaceFiles:      Condition{Status: "unavailable", Detail: "No pre-turn checkpoint was recorded."},
			WorkspaceVCS:        Condition{Status: "not_recorded", Detail: "Workspace Git context was not recorded with the pre-turn checkpoint."},
			ConversationContext: Condition{Status: "not_recorded", Detail: "Prior messages, tool results, and system or developer instructions cannot currently be reconstructed."},
			Toolchain:           Condition{Status: "not_recorded", Detail: "Turnal does not currently pin or reconstruct the toolchain."},
			Secrets:             Condition{Status: "reauthorization_required", Detail: "Secret values are not replayed automatically."},
			Network:             Condition{Status: "live", Detail: "Network responses and external services may differ."},
			Evaluators:          Condition{Status: "not_configured", Detail: "No repository evaluator contract is attached to this turn."},
		},
		Limitations: []string{
			"Git-ignored and secrets-denied paths are outside the captured workspace surface.",
			"Empty directories are not represented by checkpoint trees and must be recreated when required.",
			"Model output is nondeterministic even when the captured files are identical.",
		},
	}
	report.Source.Model = model
	report.Source.PermissionMode = permissionMode
	if hasMetadata {
		report.Source.MetadataAdapter = metadataAdapter
	}

	if turn.PreCheckpoint == nil {
		return report, nil
	}
	tree, err := analyzer.Repo.ListCommitTree(turn.PreCheckpoint.CommitSHA)
	if err != nil {
		return Report{}, fmt.Errorf("inspect pre-turn checkpoint tree: %w", err)
	}
	report.Base = Base{
		Status:        "available",
		Phase:         primitives.CheckpointPhasePre,
		Ref:           turn.PreCheckpoint.Ref,
		CommitSHA:     turn.PreCheckpoint.CommitSHA,
		CapturedFiles: len(tree),
	}
	report.FidelityLevel = "L1"
	report.Conditions.WorkspaceFiles = Condition{
		Status: "exact",
		Detail: fmt.Sprintf("%d captured files can be materialized byte-exactly from the pre-turn checkpoint.", len(tree)),
	}
	report.Conditions.WorkspaceVCS = workspaceVCSCondition(*turn.PreCheckpoint)

	switch report.Instruction.Status {
	case InstructionAvailable:
		report.Readiness = ReadinessNeedsContext
	case InstructionRedacted, InstructionMissing:
		report.Readiness = ReadinessNeedsInstruction
	}
	return report, nil
}

func metadataAdapterFor(instruction Instruction, adapters []primitives.AdapterName) primitives.AdapterName {
	if instruction.Adapter != "" {
		return instruction.Adapter
	}
	if len(adapters) == 1 {
		return adapters[0]
	}
	return ""
}

func workspaceVCSCondition(recorded recall.Checkpoint) Condition {
	if recorded.UserGit == nil {
		return Condition{Status: "not_recorded", Detail: "Workspace Git context was not recorded with the pre-turn checkpoint."}
	}
	if !recorded.UserGit.Exists {
		return Condition{Status: "not_applicable", Detail: "The workspace was recorded as not being inside a Git worktree."}
	}
	if recorded.GitSyncRef == "" {
		return Condition{
			Status: "metadata_only",
			Detail: "Git branch, HEAD, upstream, and dirty status were recorded, but no Git-sync restoration snapshot was captured.",
		}
	}
	return Condition{
		Status: "recorded",
		Detail: "Workspace Git context and a verified Git-sync restoration snapshot were recorded; restoration remains policy-bound.",
	}
}

func verifyCheckpoint(repo *checkpoint.Repo, recorded recall.Checkpoint, label string) error {
	resolvedCommit, err := repo.CheckpointCommit(recorded.Ref)
	if err != nil {
		return fmt.Errorf("resolve %s checkpoint ref: %w", label, err)
	}
	if resolvedCommit != recorded.CommitSHA {
		return fmt.Errorf(
			"fork readiness invariant failed: %s checkpoint ref %s points to %s, event records %s",
			label,
			recorded.Ref,
			resolvedCommit,
			recorded.CommitSHA,
		)
	}
	if recorded.CanonicalRef != "" {
		canonicalCommit, err := repo.CheckpointCommit(recorded.CanonicalRef)
		if err != nil {
			return fmt.Errorf("resolve canonical %s checkpoint ref: %w", label, err)
		}
		if canonicalCommit != recorded.CommitSHA {
			return fmt.Errorf(
				"fork readiness invariant failed: canonical %s checkpoint ref %s points to %s, event records %s",
				label,
				recorded.CanonicalRef,
				canonicalCommit,
				recorded.CommitSHA,
			)
		}
	}
	if recorded.GitSyncRef != "" {
		gitSyncCommit, err := repo.RefCommit(recorded.GitSyncRef)
		if err != nil {
			return fmt.Errorf("resolve %s Git-sync ref: %w", label, err)
		}
		if recorded.GitSyncCommitSHA != "" && gitSyncCommit != recorded.GitSyncCommitSHA {
			return fmt.Errorf(
				"fork readiness invariant failed: %s Git-sync ref %s points to %s, event records %s",
				label,
				recorded.GitSyncRef,
				gitSyncCommit,
				recorded.GitSyncCommitSHA,
			)
		}
	}
	return nil
}

func inspectInstruction(events []eventlog.Event) (Instruction, error) {
	instruction := Instruction{Status: InstructionMissing}
	promptSeen := false
	for _, event := range events {
		if event.Type != primitives.EventTypePromptUser {
			continue
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return Instruction{}, malformedPayloadError(event, err)
		}
		if payload == nil {
			return Instruction{}, malformedPayloadError(event, fmt.Errorf("payload must be an object"))
		}
		rawText, ok := payload["text"]
		if !ok {
			return Instruction{}, malformedPayloadError(event, fmt.Errorf("text is required"))
		}
		textValue, err := payloadString(rawText, "text")
		if err != nil {
			return Instruction{}, malformedPayloadError(event, err)
		}
		if _, err := optionalPayloadString(payload, "provider_turn_id"); err != nil {
			return Instruction{}, malformedPayloadError(event, err)
		}
		redacted, hasRedacted, err := optionalPayloadBool(payload, "redacted")
		if err != nil {
			return Instruction{}, malformedPayloadError(event, err)
		}
		if promptSeen {
			return Instruction{}, fmt.Errorf("multiple prompt.user events for one turn; duplicate at event %s", event.Seq)
		}
		promptSeen = true
		instruction.Adapter = event.Adapter
		text := strings.TrimSpace(textValue)
		if redacted || (!hasRedacted && text == primitives.SecretsRedactionText) {
			instruction = Instruction{Status: InstructionRedacted, Adapter: event.Adapter}
			continue
		}
		switch text {
		case "":
			continue
		default:
			instruction = Instruction{Status: InstructionAvailable, Text: text, Adapter: event.Adapter}
		}
	}
	return instruction, nil
}

func sessionMetadata(events []eventlog.Event, adapter primitives.AdapterName) (string, string, bool, error) {
	var model, permissionMode string
	found := false
	seenAdapters := map[primitives.AdapterName]primitives.EventSeq{}
	for _, event := range events {
		if event.Type != primitives.EventTypeSessionStart {
			continue
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return "", "", false, malformedPayloadError(event, err)
		}
		if payload == nil {
			return "", "", false, malformedPayloadError(event, fmt.Errorf("payload must be an object"))
		}
		if _, err := requiredPayloadString(payload, "provider_session_id"); err != nil {
			return "", "", false, malformedPayloadError(event, err)
		}
		parsedModel, err := optionalPayloadString(payload, "model")
		if err != nil {
			return "", "", false, malformedPayloadError(event, err)
		}
		parsedPermissionMode, err := optionalPayloadString(payload, "permission_mode")
		if err != nil {
			return "", "", false, malformedPayloadError(event, err)
		}
		if _, err := optionalPayloadString(payload, "transcript_path"); err != nil {
			return "", "", false, malformedPayloadError(event, err)
		}
		if _, err := optionalPayloadString(payload, "source"); err != nil {
			return "", "", false, malformedPayloadError(event, err)
		}
		if err := optionalPayloadStringList(payload, "command"); err != nil {
			return "", "", false, malformedPayloadError(event, err)
		}
		if previous, ok := seenAdapters[event.Adapter]; ok {
			return "", "", false, fmt.Errorf("multiple session.start events for adapter %s at events %s and %s", event.Adapter, previous, event.Seq)
		}
		seenAdapters[event.Adapter] = event.Seq
		if adapter != "" && !found && event.Adapter == adapter {
			model = strings.TrimSpace(parsedModel)
			permissionMode = strings.TrimSpace(parsedPermissionMode)
			found = true
		}
	}
	return model, permissionMode, found, nil
}

func optionalPayloadString(payload map[string]json.RawMessage, name string) (string, error) {
	raw, ok := payload[name]
	if !ok {
		return "", nil
	}
	return payloadString(raw, name)
}

func requiredPayloadString(payload map[string]json.RawMessage, name string) (string, error) {
	raw, ok := payload[name]
	if !ok {
		return "", fmt.Errorf("%s is required", name)
	}
	return payloadString(raw, name)
}

func optionalPayloadBool(payload map[string]json.RawMessage, name string) (bool, bool, error) {
	raw, ok := payload[name]
	if !ok {
		return false, false, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, true, fmt.Errorf("%s is invalid: %w", name, err)
	}
	boolean, ok := value.(bool)
	if !ok {
		return false, true, fmt.Errorf("%s must be a boolean", name)
	}
	return boolean, true, nil
}

func optionalPayloadStringList(payload map[string]json.RawMessage, name string) error {
	raw, ok := payload[name]
	if !ok {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("%s is invalid: %w", name, err)
	}
	items, ok := value.([]any)
	if !ok {
		return fmt.Errorf("%s must be an array of strings", name)
	}
	for _, item := range items {
		if _, ok := item.(string); !ok {
			return fmt.Errorf("%s must be an array of strings", name)
		}
	}
	return nil
}

func payloadString(raw json.RawMessage, name string) (string, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("%s is invalid: %w", name, err)
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", name)
	}
	return text, nil
}

func malformedPayloadError(event eventlog.Event, err error) error {
	return fmt.Errorf("malformed %s payload at event %s: %w", event.Type, event.Seq, err)
}
