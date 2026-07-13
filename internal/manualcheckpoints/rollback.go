package manualcheckpoints

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
)

type RollbackPayload struct {
	Mode            string `json:"mode,omitempty"`
	Target          string `json:"target"`
	Ref             string `json:"ref"`
	CommitSHA       string `json:"commit_sha"`
	SafetyRef       string `json:"safety_ref"`
	SafetyCommitSHA string `json:"safety_commit_sha"`
}

type Rollback struct {
	Payload      RollbackPayload
	Target       primitives.CommitSHA
	Ref          primitives.CheckpointRef
	WorktreeID   primitives.WorktreeID
	SafetyCommit primitives.CommitSHA
	Mode         primitives.RollbackMode
}

func ParseRollbackEvent(event eventlog.Event) (Rollback, error) {
	if event.Type != primitives.EventTypeRollback || event.TurnID != nil || event.Adapter != primitives.AdapterManual {
		return Rollback{}, fmt.Errorf("workspace rollback event %s provenance invariant failed", event.Seq)
	}
	var payload RollbackPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return Rollback{}, fmt.Errorf("workspace rollback event %s payload malformed: %w", event.Seq, err)
	}
	targetText := strings.TrimSpace(payload.Target)
	if targetText == "" {
		targetText = strings.TrimSpace(event.RawRef)
	}
	target, err := primitives.ParseCommitSHA(targetText)
	if err != nil {
		return Rollback{}, fmt.Errorf("workspace rollback event %s target invariant failed: %w", event.Seq, err)
	}
	commit, err := primitives.ParseCommitSHA(payload.CommitSHA)
	if err != nil || commit != target {
		return Rollback{}, fmt.Errorf("workspace rollback event %s commit invariant failed: target and commit must match", event.Seq)
	}
	ref, err := primitives.ParseCheckpointRef(payload.Ref)
	if err != nil {
		return Rollback{}, fmt.Errorf("workspace rollback event %s ref invariant failed: %w", event.Seq, err)
	}
	parts, err := ref.Parts()
	if err != nil || !parts.Manual {
		return Rollback{}, fmt.Errorf("workspace rollback event %s ref invariant failed: manual checkpoint ref required", event.Seq)
	}
	if event.WorktreeID != "" && event.WorktreeID != parts.WorktreeID {
		return Rollback{}, fmt.Errorf("workspace rollback event %s worktree invariant failed: event=%s ref=%s", event.Seq, event.WorktreeID, parts.WorktreeID)
	}
	modeText := strings.TrimSpace(payload.Mode)
	if modeText == "" {
		modeText = primitives.RollbackModeCheckpoint.String()
	}
	mode, err := primitives.ParseRollbackMode(modeText)
	if err != nil || mode != primitives.RollbackModeCheckpoint {
		return Rollback{}, fmt.Errorf("workspace rollback event %s mode invariant failed: manual checkpoints require checkpoint mode", event.Seq)
	}
	if !strings.HasPrefix(payload.SafetyRef, "refs/agent-vcs/rollback-safety/") {
		return Rollback{}, fmt.Errorf("workspace rollback event %s safety ref invariant failed", event.Seq)
	}
	safetyCommit, err := primitives.ParseCommitSHA(payload.SafetyCommitSHA)
	if err != nil {
		return Rollback{}, fmt.Errorf("workspace rollback event %s safety commit invariant failed: %w", event.Seq, err)
	}
	if event.RawRef != target.String() {
		return Rollback{}, fmt.Errorf("workspace rollback event %s raw ref invariant failed", event.Seq)
	}
	wantSourceID := fmt.Sprintf("turnal:rollback:%s:%s:%s", mode, target, safetyCommit)
	if event.SourceID != wantSourceID {
		return Rollback{}, fmt.Errorf("workspace rollback event %s source invariant failed", event.Seq)
	}
	return Rollback{
		Payload: payload, Target: target, Ref: ref, WorktreeID: parts.WorktreeID,
		SafetyCommit: safetyCommit, Mode: mode,
	}, nil
}

func ValidateRollbackEvent(repo *checkpoint.Repo, event eventlog.Event) (Rollback, error) {
	if repo == nil {
		return Rollback{}, fmt.Errorf("workspace rollback event validation requires repo")
	}
	rollback, err := ParseRollbackEvent(event)
	if err != nil {
		return Rollback{}, err
	}
	commit, err := repo.CheckpointCommit(rollback.Ref)
	if err != nil {
		return Rollback{}, fmt.Errorf("workspace rollback event %s checkpoint ref invariant failed: %w", event.Seq, err)
	}
	if commit != rollback.Target {
		return Rollback{}, fmt.Errorf("workspace rollback event %s checkpoint ref invariant failed: ref points to %s, target is %s", event.Seq, commit, rollback.Target)
	}
	return rollback, nil
}
