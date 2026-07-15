package cases

import (
	"fmt"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/runs"
	"github.com/AadiJo/turnal/internal/verifier"
)

type LinkAttemptRequest struct {
	CaseID    primitives.CaseID
	RunID     primitives.RunID
	AttemptID primitives.AttemptID
	Command   []string
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
		payload := caseAttemptLinkPayload{CaseID: definition.ID, Scope: scope, RunID: request.RunID, AttemptID: request.AttemptID, Source: definition.Source, Execution: &execution, Command: append([]string(nil), request.Command...)}
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
		payload := caseAttemptResultPayload{CaseID: definition.ID, Scope: definition.Scope, RunID: request.RunID, AttemptID: request.AttemptID, Source: definition.Source, PostRef: request.PostRef, PostCommit: request.PostCommit, Status: request.Status, ExitCode: cloneInt(request.ExitCode), Error: request.Error, Verification: request.Verification}
		if err := validateAttemptResultPayload(payload, definition, link); err != nil {
			return err
		}
		commit, err := repo.CheckpointCommit(request.PostRef)
		if err != nil {
			return fmt.Errorf("resolve attempt post checkpoint: %w", err)
		}
		if commit != request.PostCommit {
			return fmt.Errorf("attempt post ref %s points to %s, result records %s", request.PostRef, commit, request.PostCommit)
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
		projection, err := Rebuild(repo)
		if err != nil {
			return err
		}
		definition, link, err := projectionAttempt(projection, caseID, attemptID)
		if err != nil {
			return err
		}
		if link.Result == nil {
			return fmt.Errorf("attempt %s has no completed result", attemptID)
		}
		payload := caseAttemptSelectPayload{CaseID: caseID, Scope: definition.Scope, AttemptID: attemptID, Source: definition.Source}
		if _, err := appendRecord(repo, definition.Source, caseAdapter(definition), primitives.EventTypeCaseAttemptSelect, "", payload); err != nil {
			return err
		}
		updated, err := Rebuild(repo)
		if err != nil {
			return err
		}
		selected, _ = updated.Case(caseID)
		return nil
	})
	return selected, err
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
