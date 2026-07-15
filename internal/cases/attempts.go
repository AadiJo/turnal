package cases

import (
	"fmt"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/runs"
	"github.com/AadiJo/turnal/internal/turns"
	"github.com/AadiJo/turnal/internal/verifier"
)

type LinkAttemptRequest struct {
	CaseID    primitives.CaseID
	RunID     primitives.RunID
	AttemptID primitives.AttemptID
	Command   []string
	Workspace string
	Keep      bool
}

type RecordAttemptResultRequest struct {
	CaseID       primitives.CaseID
	RunID        primitives.RunID
	AttemptID    primitives.AttemptID
	PostRef      primitives.CheckpointRef
	PostCommit   primitives.CommitSHA
	Status       string
	ExitCode     *int
	Error        string
	Verification *verifier.Report
}

type RecordApplyRequest struct {
	CaseID       primitives.CaseID
	AttemptID    primitives.AttemptID
	PostCommit   primitives.CommitSHA
	SafetyRef    string
	SafetyCommit primitives.CommitSHA
	Changes      int
}

func LinkAttempt(repo *checkpoint.Repo, request LinkAttemptRequest) (AttemptLink, error) {
	if repo == nil {
		return AttemptLink{}, fmt.Errorf("link attempt requires checkpoint repo")
	}
	var linked AttemptLink
	err := repo.WithWorkspaceLock("link case attempt", func() error {
		projection, err := Rebuild(repo)
		if err != nil {
			return err
		}
		definition, ok := projection.Case(request.CaseID)
		if !ok {
			return fmt.Errorf("case %s does not exist in this Turnal store", request.CaseID)
		}
		if err := validateCaseRepoScope(repo, definition); err != nil {
			return err
		}
		for _, existing := range definition.AttemptLinks {
			if existing.AttemptID == request.AttemptID {
				linked = existing
				return nil
			}
		}
		run, err := runs.Read(repo, request.RunID)
		if err != nil {
			return err
		}
		var execution SourceTurn
		for _, attempt := range run.Attempts {
			if attempt.ID == request.AttemptID {
				execution = SourceTurn{SessionID: attempt.SessionID, TurnID: attempt.TurnID, StreamID: attempt.Provenance.StreamID}
				break
			}
		}
		if execution.SessionID == "" {
			return fmt.Errorf("attempt %s does not belong to run %s", request.AttemptID, request.RunID)
		}
		scope := Scope{RepoID: repo.RepoID, StoreID: repo.StoreID, WorktreeID: repo.WorktreeID}
		payload := caseAttemptLinkPayload{CaseID: definition.ID, Scope: scope, RunID: request.RunID, AttemptID: request.AttemptID, Source: definition.Source, Execution: &execution, Command: append([]string(nil), request.Command...), Workspace: request.Workspace, Keep: request.Keep}
		if _, err := appendRecord(repo, definition.Source, caseAdapter(definition), primitives.EventTypeCaseAttemptLink, fmt.Sprintf("case:%s:attempt:%s", definition.ID, request.AttemptID), payload); err != nil {
			return err
		}
		updated, err := Rebuild(repo)
		if err != nil {
			return err
		}
		definition, _ = updated.Case(request.CaseID)
		for _, candidate := range definition.AttemptLinks {
			if candidate.AttemptID == request.AttemptID {
				linked = candidate
				return nil
			}
		}
		return fmt.Errorf("linked attempt %s is missing from durable case projection", request.AttemptID)
	})
	return linked, err
}

func RecordAttemptResult(repo *checkpoint.Repo, request RecordAttemptResultRequest) (AttemptResult, error) {
	if repo == nil {
		return AttemptResult{}, fmt.Errorf("record attempt result requires checkpoint repo")
	}
	var result AttemptResult
	err := repo.WithWorkspaceLock("record case attempt result", func() error {
		projection, err := Rebuild(repo)
		if err != nil {
			return err
		}
		definition, link, err := projectionAttempt(projection, request.CaseID, request.AttemptID)
		if err != nil {
			return err
		}
		if err := validateCaseRepoScope(repo, definition); err != nil {
			return err
		}
		if link.RunID != request.RunID {
			return fmt.Errorf("attempt %s does not belong to run %s", request.AttemptID, request.RunID)
		}
		if link.Result != nil {
			if link.Result.PostRef != request.PostRef || link.Result.PostCommit != request.PostCommit || link.Result.Status != request.Status {
				return fmt.Errorf("attempt %s already has a different durable result", request.AttemptID)
			}
			result = *link.Result
			return nil
		}
		payload := caseAttemptResultPayload{CaseID: definition.ID, Scope: definition.Scope, RunID: request.RunID, AttemptID: request.AttemptID, Source: definition.Source, Status: request.Status, ExitCode: cloneInt(request.ExitCode), Error: request.Error, Verification: request.Verification}
		if request.PostRef != "" || request.PostCommit != "" {
			postRef := request.PostRef
			postCommit := request.PostCommit
			payload.PostRef = &postRef
			payload.PostCommit = &postCommit
		}
		if err := validateAttemptResultPayload(payload, definition, link); err != nil {
			return err
		}
		if request.PostRef != "" {
			commit, err := repo.CheckpointCommit(request.PostRef)
			if err != nil {
				return fmt.Errorf("resolve attempt post checkpoint: %w", err)
			}
			if commit != request.PostCommit {
				return fmt.Errorf("attempt post ref %s points to %s, result records %s", request.PostRef, commit, request.PostCommit)
			}
		}
		if _, err := appendRecord(repo, definition.Source, caseAdapter(definition), primitives.EventTypeCaseAttemptResult, fmt.Sprintf("case:%s:attempt:%s:result", definition.ID, request.AttemptID), payload); err != nil {
			return err
		}
		updated, err := Rebuild(repo)
		if err != nil {
			return err
		}
		_, updatedLink, err := projectionAttempt(updated, request.CaseID, request.AttemptID)
		if err != nil || updatedLink.Result == nil {
			return fmt.Errorf("recorded result for attempt %s is missing from durable case projection", request.AttemptID)
		}
		result = *updatedLink.Result
		return nil
	})
	return result, err
}

func SelectAttempt(repo *checkpoint.Repo, caseID primitives.CaseID, attemptID primitives.AttemptID) (Case, error) {
	if repo == nil {
		return Case{}, fmt.Errorf("select attempt requires checkpoint repo")
	}
	var selected Case
	err := repo.WithWorkspaceLock("select case attempt", func() error {
		var err error
		selected, err = SelectAttemptLocked(repo, caseID, attemptID)
		return err
	})
	return selected, err
}

// SelectAttemptLocked records a selection while the caller holds the workspace
// lock as part of a larger atomic operation.
func SelectAttemptLocked(repo *checkpoint.Repo, caseID primitives.CaseID, attemptID primitives.AttemptID) (Case, error) {
	projection, err := Rebuild(repo)
	if err != nil {
		return Case{}, err
	}
	definition, link, err := projectionAttempt(projection, caseID, attemptID)
	if err != nil {
		return Case{}, err
	}
	if err := validateCaseRepoScope(repo, definition); err != nil {
		return Case{}, err
	}
	if link.Result == nil || link.Result.PostRef == "" || link.Result.PostCommit == "" {
		return Case{}, fmt.Errorf("attempt %s has no completed result", attemptID)
	}
	payload := caseAttemptSelectPayload{CaseID: caseID, Scope: definition.Scope, AttemptID: attemptID, Source: definition.Source}
	if _, err := appendRecord(repo, definition.Source, caseAdapter(definition), primitives.EventTypeCaseAttemptSelect, "", payload); err != nil {
		return Case{}, err
	}
	updated, err := Rebuild(repo)
	if err != nil {
		return Case{}, err
	}
	selected, _ := updated.Case(caseID)
	return selected, nil
}

// ReconcileAbandonedAttempts terminalizes Case links whose durable Run has
// already been recovered as incomplete.
func ReconcileAbandonedAttempts(repo *checkpoint.Repo) error {
	if repo == nil {
		return nil
	}
	projection, err := Rebuild(repo)
	if err != nil {
		return err
	}
	for _, definition := range projection.Cases {
		if definition.Scope.WorktreeID != repo.WorktreeID {
			continue
		}
		for _, link := range definition.AttemptLinks {
			if link.Result != nil {
				continue
			}
			run, err := runs.Read(repo, link.RunID)
			if err != nil {
				return fmt.Errorf("inspect Case attempt %s run during recovery: %w", link.AttemptID, err)
			}
			if run.Status == runs.StatusRunning {
				continue
			}
			if err := turns.NewManager(repo).ClearActiveForRecovery(link.Execution.SessionID, link.Execution.TurnID); err != nil {
				return fmt.Errorf("clear abandoned Case attempt %s active turn: %w", link.AttemptID, err)
			}
			message := "attempt abandoned before its result checkpoint was captured"
			if run.Error != "" {
				message += ": " + run.Error
			}
			request := RecordAttemptResultRequest{CaseID: definition.ID, RunID: link.RunID, AttemptID: link.AttemptID, Status: AttemptStatusIncomplete, Error: message}
			infos, err := repo.ListAllCheckpointRefInfos()
			if err != nil {
				return fmt.Errorf("inspect attempt %s checkpoints during recovery: %w", link.AttemptID, err)
			}
			for _, info := range infos {
				if info.SessionID == link.Execution.SessionID && info.TurnID == link.Execution.TurnID && info.Phase == primitives.CheckpointPhasePost && info.WorktreeID == definition.Scope.WorktreeID && (link.Execution.StreamID == "" || info.StreamID == link.Execution.StreamID) {
					if request.PostCommit != "" && request.PostCommit != info.Commit {
						return fmt.Errorf("attempt %s has conflicting post checkpoints during recovery", link.AttemptID)
					}
					request.PostRef, request.PostCommit = info.Ref, info.Commit
				}
			}
			if _, err := RecordAttemptResult(repo, request); err != nil {
				return fmt.Errorf("terminalize abandoned Case attempt %s: %w", link.AttemptID, err)
			}
		}
	}
	return nil
}

func RecordApply(repo *checkpoint.Repo, request RecordApplyRequest) (AttemptApplication, error) {
	if repo == nil {
		return AttemptApplication{}, fmt.Errorf("record attempt apply requires checkpoint repo")
	}
	var application AttemptApplication
	err := repo.WithWorkspaceLock("record case attempt apply", func() error {
		projection, err := Rebuild(repo)
		if err != nil {
			return err
		}
		definition, link, err := projectionAttempt(projection, request.CaseID, request.AttemptID)
		if err != nil {
			return err
		}
		if err := validateCaseRepoScope(repo, definition); err != nil {
			return err
		}
		if definition.Selection == nil || definition.Selection.AttemptID != request.AttemptID {
			return fmt.Errorf("attempt %s must be selected before it can be applied", request.AttemptID)
		}
		payload := caseAttemptApplyPayload{CaseID: request.CaseID, Scope: definition.Scope, AttemptID: request.AttemptID, Source: definition.Source, PostCommit: request.PostCommit, SafetyRef: request.SafetyRef, SafetyCommit: request.SafetyCommit, Changes: request.Changes}
		if err := validateAttemptApplyPayload(payload, definition, link); err != nil {
			return err
		}
		safetyCommit, err := repo.RefCommit(request.SafetyRef)
		if err != nil {
			return fmt.Errorf("resolve attempt application safety ref: %w", err)
		}
		if safetyCommit != request.SafetyCommit {
			return fmt.Errorf("attempt application safety ref %s points to %s, application records %s", request.SafetyRef, safetyCommit, request.SafetyCommit)
		}
		if _, err := appendRecord(repo, definition.Source, caseAdapter(definition), primitives.EventTypeCaseAttemptApply, "", payload); err != nil {
			return err
		}
		updated, err := Rebuild(repo)
		if err != nil {
			return err
		}
		updatedCase, _ := updated.Case(request.CaseID)
		application = updatedCase.Applications[len(updatedCase.Applications)-1]
		return nil
	})
	return application, err
}

func projectionAttempt(projection Projection, caseID primitives.CaseID, attemptID primitives.AttemptID) (Case, AttemptLink, error) {
	definition, ok := projection.Case(caseID)
	if !ok {
		return Case{}, AttemptLink{}, fmt.Errorf("case %s does not exist in this Turnal store", caseID)
	}
	for _, link := range definition.AttemptLinks {
		if link.AttemptID == attemptID {
			return definition, link, nil
		}
	}
	return Case{}, AttemptLink{}, fmt.Errorf("attempt %s is not linked to case %s", attemptID, caseID)
}

func caseAdapter(definition Case) primitives.AdapterName {
	if definition.Readiness.Source.MetadataAdapter != "" {
		return definition.Readiness.Source.MetadataAdapter
	}
	if definition.Readiness.Instruction.Adapter != "" {
		return definition.Readiness.Instruction.Adapter
	}
	if len(definition.Readiness.Source.Adapters) > 0 {
		return definition.Readiness.Source.Adapters[0]
	}
	return primitives.AdapterCodex
}

func validateCaseRepoScope(repo *checkpoint.Repo, definition Case) error {
	if definition.Scope.RepoID != repo.RepoID || definition.Scope.StoreID != repo.StoreID || definition.Scope.WorktreeID != repo.WorktreeID {
		return fmt.Errorf("case %s belongs to a different repository, store, or worktree", definition.ID)
	}
	return nil
}
