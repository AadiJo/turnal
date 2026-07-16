package experiments

import (
	"fmt"

	"github.com/AadiJo/turnal/internal/cases"
	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
	rollbackengine "github.com/AadiJo/turnal/internal/rollback"
)

type ApplyRequest struct {
	CaseID    primitives.CaseID
	AttemptID primitives.AttemptID
	DryRun    bool
}

type ApplyResult struct {
	Version       int                        `json:"version"`
	CaseID        primitives.CaseID          `json:"case_id"`
	AttemptID     primitives.AttemptID       `json:"attempt_id"`
	BaseCommit    primitives.CommitSHA       `json:"base_commit"`
	PostCommit    primitives.CommitSHA       `json:"post_commit"`
	WorkspaceTree string                     `json:"workspace_tree"`
	Changes       []checkpoint.RestoreChange `json:"changes"`
	SafetyRef     string                     `json:"safety_ref,omitempty"`
	SafetyCommit  primitives.CommitSHA       `json:"safety_commit,omitempty"`
	DryRun        bool                       `json:"dry_run"`
}

func Apply(repo *checkpoint.Repo, request ApplyRequest) (ApplyResult, error) {
	if repo == nil {
		return ApplyResult{}, fmt.Errorf("apply requires checkpoint repo")
	}
	if err := RecoverAbandoned(repo); err != nil {
		return ApplyResult{}, fmt.Errorf("recover abandoned fork attempts: %w", err)
	}
	var result ApplyResult
	err := repo.WithWorkspaceLock("apply case attempt", func() error {
		projection, err := cases.Rebuild(repo)
		if err != nil {
			return err
		}
		definition, ok := projection.Case(request.CaseID)
		if !ok {
			return fmt.Errorf("case %s does not exist in this Turnal store", request.CaseID)
		}
		if definition.Scope.RepoID != repo.RepoID || definition.Scope.StoreID != repo.StoreID || definition.Scope.WorktreeID != repo.WorktreeID {
			return fmt.Errorf("case %s belongs to a different repository, store, or worktree", definition.ID)
		}
		attemptID := request.AttemptID
		if attemptID == "" {
			if definition.Selection == nil {
				return fmt.Errorf("case %s has no selected attempt; run turnal select first", definition.ID)
			}
			attemptID = definition.Selection.AttemptID
		}
		var link *cases.AttemptLink
		for index := range definition.AttemptLinks {
			if definition.AttemptLinks[index].AttemptID == attemptID {
				link = &definition.AttemptLinks[index]
				break
			}
		}
		if link == nil {
			return fmt.Errorf("attempt %s is not linked to case %s", attemptID, definition.ID)
		}
		if link.Result == nil || link.Result.PostRef == "" || link.Result.PostCommit == "" {
			return fmt.Errorf("attempt %s has no completed result", attemptID)
		}
		baseCommit, err := repo.CheckpointCommit(definition.Readiness.Base.Ref)
		if err != nil {
			return fmt.Errorf("resolve case base checkpoint: %w", err)
		}
		if baseCommit != definition.Readiness.Base.CommitSHA {
			return fmt.Errorf("case base checkpoint invariant failed: ref %s points to %s, case records %s", definition.Readiness.Base.Ref, baseCommit, definition.Readiness.Base.CommitSHA)
		}
		postCommit, err := repo.CheckpointCommit(link.Result.PostRef)
		if err != nil {
			return fmt.Errorf("resolve attempt result checkpoint: %w", err)
		}
		if postCommit != link.Result.PostCommit {
			return fmt.Errorf("attempt result invariant failed: ref %s points to %s, result records %s", link.Result.PostRef, postCommit, link.Result.PostCommit)
		}
		basePlan, err := repo.PlanRestoreCommit(baseCommit)
		if err != nil {
			return fmt.Errorf("compare workspace to case base: %w", err)
		}
		if len(basePlan.Changes) != 0 {
			return fmt.Errorf("workspace does not match case %s base on the captured surface (%d changes); apply is exact-base only, so restore or replay the base before applying this attempt", definition.ID, len(basePlan.Changes))
		}
		target, err := primitives.NewTargetRef(link.Execution.SessionID, link.Execution.TurnID, primitives.CheckpointPhasePost)
		if err != nil {
			return err
		}
		resolved := rollbackengine.ResolvedTarget{Target: target, CheckpointRef: link.Result.PostRef, Commit: postCommit, SessionID: link.Execution.SessionID, TurnID: link.Execution.TurnID, Phase: primitives.CheckpointPhasePost}
		rollbackRequest := rollbackengine.Request{Resolved: &resolved, DryRun: request.DryRun, ExpectedWorkspaceTree: basePlan.WorkspaceTree}
		if !request.DryRun {
			if definition.Selection == nil || definition.Selection.AttemptID != attemptID {
				if _, err := cases.SelectAttemptLocked(repo, definition.ID, attemptID); err != nil {
					return fmt.Errorf("record applied attempt selection: %w", err)
				}
			}
			rollbackRequest.Application = &rollbackengine.ApplicationMetadata{CaseID: definition.ID, AttemptID: attemptID, PostCommit: postCommit}
		}
		rollbackResult, err := rollbackengine.New(repo).RunLocked(rollbackRequest)
		if err != nil {
			return err
		}
		result = ApplyResult{Version: 1, CaseID: definition.ID, AttemptID: attemptID, BaseCommit: baseCommit, PostCommit: postCommit, WorkspaceTree: rollbackResult.Plan.WorkspaceTree, Changes: append([]checkpoint.RestoreChange(nil), rollbackResult.Plan.Changes...), DryRun: request.DryRun}
		if request.DryRun {
			return nil
		}
		if rollbackResult.Safety == nil {
			return fmt.Errorf("apply invariant failed: rollback completed without a safety checkpoint")
		}
		result.SafetyRef, result.SafetyCommit = rollbackResult.Safety.Ref, rollbackResult.Safety.Commit
		return nil
	})
	return result, err
}
