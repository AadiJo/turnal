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
	SessionID      primitives.SessionID     `json:"session_id"`
	TurnID         primitives.TurnID        `json:"turn_id"`
	WorktreeID     primitives.WorktreeID    `json:"worktree_id,omitempty"`
	StreamID       primitives.EventStreamID `json:"stream_id,omitempty"`
	Adapters       []primitives.AdapterName `json:"adapters,omitempty"`
	Model          string                   `json:"model,omitempty"`
	PermissionMode string                   `json:"permission_mode,omitempty"`
	Complete       bool                     `json:"complete"`
}

type Base struct {
	Status        string                     `json:"status"`
	Phase         primitives.CheckpointPhase `json:"phase"`
	Ref           primitives.CheckpointRef   `json:"ref,omitempty"`
	CommitSHA     primitives.CommitSHA       `json:"commit_sha,omitempty"`
	CapturedFiles int                        `json:"captured_files"`
}

type Instruction struct {
	Status InstructionStatus `json:"status"`
	Text   string            `json:"text,omitempty"`
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
	model, permissionMode, err := sessionMetadata(turn.SessionEvents)
	if err != nil {
		return Report{}, fmt.Errorf("fork readiness integrity failed: %w", err)
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
			"Model output is nondeterministic even when the captured files are identical.",
		},
	}
	report.Source.Model = model
	report.Source.PermissionMode = permissionMode

	if turn.PreCheckpoint == nil {
		return report, nil
	}
	resolvedCommit, err := analyzer.Repo.CheckpointCommit(turn.PreCheckpoint.Ref)
	if err != nil {
		return Report{}, fmt.Errorf("resolve pre-turn checkpoint ref: %w", err)
	}
	if resolvedCommit != turn.PreCheckpoint.CommitSHA {
		return Report{}, fmt.Errorf(
			"fork readiness invariant failed: pre-turn checkpoint ref %s points to %s, event records %s",
			turn.PreCheckpoint.Ref,
			resolvedCommit,
			turn.PreCheckpoint.CommitSHA,
		)
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
	if turn.PreCheckpoint.UserGit != nil {
		report.Conditions.WorkspaceVCS = Condition{
			Status: "recorded",
			Detail: "Workspace Git branch, HEAD, index, and dirty context were recorded where available; restoration remains policy-bound.",
		}
	}

	switch report.Instruction.Status {
	case InstructionAvailable:
		report.Readiness = ReadinessNeedsContext
	case InstructionRedacted, InstructionMissing:
		report.Readiness = ReadinessNeedsInstruction
	}
	return report, nil
}

func inspectInstruction(events []eventlog.Event) (Instruction, error) {
	instruction := Instruction{Status: InstructionMissing}
	for _, event := range events {
		if event.Type != primitives.EventTypePromptUser {
			continue
		}
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return Instruction{}, malformedPayloadError(event, err)
		}
		if string(event.Payload) == "null" {
			return Instruction{}, malformedPayloadError(event, fmt.Errorf("payload must be an object"))
		}
		text := strings.TrimSpace(payload.Text)
		if instruction.Status != InstructionMissing {
			continue
		}
		switch text {
		case "":
			continue
		case primitives.SecretsRedactionText:
			instruction = Instruction{Status: InstructionRedacted}
		default:
			instruction = Instruction{Status: InstructionAvailable, Text: text}
		}
	}
	return instruction, nil
}

func sessionMetadata(events []eventlog.Event) (string, string, error) {
	var model, permissionMode string
	found := false
	for _, event := range events {
		if event.Type != primitives.EventTypeSessionStart {
			continue
		}
		var payload struct {
			Model          string `json:"model"`
			PermissionMode string `json:"permission_mode"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return "", "", malformedPayloadError(event, err)
		}
		if string(event.Payload) == "null" {
			return "", "", malformedPayloadError(event, fmt.Errorf("payload must be an object"))
		}
		if !found {
			model = strings.TrimSpace(payload.Model)
			permissionMode = strings.TrimSpace(payload.PermissionMode)
			found = true
		}
	}
	return model, permissionMode, nil
}

func malformedPayloadError(event eventlog.Event, err error) error {
	return fmt.Errorf("malformed %s payload at event %s: %w", event.Type, event.Seq, err)
}
