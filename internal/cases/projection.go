package cases

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/config"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/fork"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/runs"
	verifierengine "github.com/AadiJo/turnal/internal/verifier"
)

// Rebuild derives Task and Case views exclusively from append-only events.
func Rebuild(repo *checkpoint.Repo) (Projection, error) {
	if repo == nil {
		return Projection{}, fmt.Errorf("task and case projection requires checkpoint repo")
	}
	streams, err := eventlog.ListDurableStreams(repo.MetadataDir)
	if err != nil {
		return Projection{}, err
	}
	inventory, err := runs.Inspect(repo)
	if err != nil {
		return Projection{}, err
	}
	attempts := make(map[primitives.AttemptID]attemptRecord)
	for _, run := range inventory.Runs {
		for _, attempt := range run.Attempts {
			if existing, ok := attempts[attempt.ID]; ok && (existing.RunID != run.ID || existing.Source.SessionID != attempt.SessionID || existing.Source.TurnID != attempt.TurnID) {
				return Projection{}, fmt.Errorf("attempt id %s has conflicting durable definitions", attempt.ID)
			}
			attempts[attempt.ID] = attemptRecord{
				RunID: run.ID, AttemptID: attempt.ID,
				Scope:  Scope{RepoID: run.RepoID, StoreID: run.StoreID, WorktreeID: run.WorktreeID},
				Source: SourceTurn{SessionID: attempt.SessionID, TurnID: attempt.TurnID, StreamID: attempt.Provenance.StreamID},
			}
		}
	}
	return project(streams, Scope{RepoID: repo.RepoID, StoreID: repo.StoreID, WorktreeID: repo.WorktreeID}, attempts)
}

func project(streams []eventlog.DurableStream, expected Scope, attempts map[primitives.AttemptID]attemptRecord) (Projection, error) {
	if err := validateScope(expected); err != nil {
		return Projection{}, err
	}
	tasks := make(map[primitives.TaskID]*Task)
	revisions := make(map[primitives.TaskID]map[uint64]TaskRevision)
	casesByID := make(map[primitives.CaseID]*Case)
	var caseEvents []eventlog.Event
	var attemptEvents []eventlog.Event
	var resultEvents []eventlog.Event
	var selectionEvents []eventlog.Event
	var applicationEvents []eventlog.Event

	for _, stream := range streams {
		for _, event := range stream.Events {
			switch event.Type {
			case primitives.EventTypeTaskCreate:
				var payload taskCreatePayload
				if err := decodeRelationship(event, &payload); err != nil {
					return Projection{}, err
				}
				if _, exists := tasks[payload.TaskID]; exists {
					return Projection{}, relationshipError(event, fmt.Errorf("task %s has duplicate creation records", payload.TaskID))
				}
				if err := validateTaskCreate(event, payload, expected); err != nil {
					return Projection{}, err
				}
				tasks[payload.TaskID] = &Task{ID: payload.TaskID, Scope: payload.Scope, Created: provenance(event)}
				if revisions[payload.TaskID] == nil {
					revisions[payload.TaskID] = make(map[uint64]TaskRevision)
				}
				revisions[payload.TaskID][1] = TaskRevision{Number: 1, Scope: payload.Scope, Instruction: payload.InitialRevision.Instruction, Source: payload.InitialRevision.Source, Created: provenance(event)}
			case primitives.EventTypeTaskRevision:
				var payload taskRevisionPayload
				if err := decodeRelationship(event, &payload); err != nil {
					return Projection{}, err
				}
				if err := validateTaskRevision(event, payload, expected); err != nil {
					return Projection{}, err
				}
				if revisions[payload.TaskID] == nil {
					revisions[payload.TaskID] = make(map[uint64]TaskRevision)
				}
				if _, exists := revisions[payload.TaskID][payload.Revision]; exists {
					return Projection{}, relationshipError(event, fmt.Errorf("task %s revision %d is duplicated or reused", payload.TaskID, payload.Revision))
				}
				revisions[payload.TaskID][payload.Revision] = TaskRevision{Number: payload.Revision, Scope: payload.Scope, Instruction: payload.Instruction, Source: payload.Source, Created: provenance(event)}
			case primitives.EventTypeCaseCreate:
				caseEvents = append(caseEvents, event)
			case primitives.EventTypeCaseAttemptLink:
				attemptEvents = append(attemptEvents, event)
			case primitives.EventTypeCaseAttemptResult:
				resultEvents = append(resultEvents, event)
			case primitives.EventTypeCaseAttemptSelect:
				selectionEvents = append(selectionEvents, event)
			case primitives.EventTypeCaseAttemptApply:
				applicationEvents = append(applicationEvents, event)
			}
		}
	}

	for taskID, byNumber := range revisions {
		task, exists := tasks[taskID]
		if !exists {
			return Projection{}, fmt.Errorf("task revision references nonexistent task %s", taskID)
		}
		for number := uint64(1); number <= uint64(len(byNumber)); number++ {
			revision, ok := byNumber[number]
			if !ok {
				return Projection{}, fmt.Errorf("task %s revisions skip number %d", taskID, number)
			}
			if revision.Scope != task.Scope {
				return Projection{}, fmt.Errorf("task %s revision %d belongs to a different repository, store, or worktree", taskID, number)
			}
			task.Revisions = append(task.Revisions, revision)
		}
	}

	for _, event := range caseEvents {
		var payload caseCreatePayload
		if err := decodeRelationship(event, &payload); err != nil {
			return Projection{}, err
		}
		if _, exists := casesByID[payload.CaseID]; exists {
			return Projection{}, relationshipError(event, fmt.Errorf("case %s has duplicate or conflicting creation records", payload.CaseID))
		}
		task, exists := tasks[payload.TaskID]
		if !exists {
			return Projection{}, relationshipError(event, fmt.Errorf("case references nonexistent task %s", payload.TaskID))
		}
		if payload.TaskRevision == 0 || payload.TaskRevision > uint64(len(task.Revisions)) {
			return Projection{}, relationshipError(event, fmt.Errorf("case references nonexistent task %s revision %d", payload.TaskID, payload.TaskRevision))
		}
		if err := validateCaseCreate(event, payload, expected, *task); err != nil {
			return Projection{}, err
		}
		casesByID[payload.CaseID] = &Case{ID: payload.CaseID, TaskID: payload.TaskID, TaskRevision: payload.TaskRevision, Scope: payload.Scope, Source: payload.Source, Readiness: payload.Readiness, Verifiers: cloneVerifiers(payload.Verifiers), Limitations: append([]string(nil), payload.Limitations...), AttemptLinks: make([]AttemptLink, 0), Created: provenance(event)}
	}

	seenLinks := make(map[string]bool)
	for _, event := range attemptEvents {
		var payload caseAttemptLinkPayload
		if err := decodeRelationship(event, &payload); err != nil {
			return Projection{}, err
		}
		definition, exists := casesByID[payload.CaseID]
		if !exists {
			return Projection{}, relationshipError(event, fmt.Errorf("attempt link references nonexistent case %s", payload.CaseID))
		}
		attempt, exists := attempts[payload.AttemptID]
		if !exists {
			return Projection{}, relationshipError(event, fmt.Errorf("attempt %s does not exist in this Turnal store", payload.AttemptID))
		}
		if err := validateAttemptLink(event, payload, *definition, attempt); err != nil {
			return Projection{}, err
		}
		key := payload.CaseID.String() + "\x00" + payload.AttemptID.String()
		if seenLinks[key] {
			return Projection{}, relationshipError(event, fmt.Errorf("attempt %s is linked to case %s more than once", payload.AttemptID, payload.CaseID))
		}
		execution := payload.Source
		if payload.Execution != nil {
			execution = *payload.Execution
		}
		if execution.SessionID == "" {
			execution = payload.Source
		}
		definition.AttemptLinks = append(definition.AttemptLinks, AttemptLink{AttemptID: payload.AttemptID, RunID: payload.RunID, Source: payload.Source, Execution: execution, Command: append([]string(nil), payload.Command...), Created: provenance(event)})
		seenLinks[key] = true
	}

	for _, event := range resultEvents {
		var payload caseAttemptResultPayload
		if err := decodeRelationship(event, &payload); err != nil {
			return Projection{}, err
		}
		definition, link, err := linkedAttempt(casesByID, payload.CaseID, payload.AttemptID)
		if err != nil {
			return Projection{}, relationshipError(event, err)
		}
		if link.Result != nil {
			return Projection{}, relationshipError(event, fmt.Errorf("attempt %s has more than one result", payload.AttemptID))
		}
		if err := validateAttemptResult(event, payload, *definition, *link); err != nil {
			return Projection{}, err
		}
		link.Result = &AttemptResult{PostRef: payload.PostRef, PostCommit: payload.PostCommit, Status: payload.Status, ExitCode: cloneInt(payload.ExitCode), Error: payload.Error, Completed: provenance(event)}
	}

	for _, event := range selectionEvents {
		var payload caseAttemptSelectPayload
		if err := decodeRelationship(event, &payload); err != nil {
			return Projection{}, err
		}
		definition, link, err := linkedAttempt(casesByID, payload.CaseID, payload.AttemptID)
		if err != nil {
			return Projection{}, relationshipError(event, err)
		}
		if err := validateCaseMutationEnvelope(event, payload.Scope, payload.Source, *definition); err != nil {
			return Projection{}, err
		}
		if link.Result == nil {
			return Projection{}, relationshipError(event, fmt.Errorf("attempt %s cannot be selected before it has a result", payload.AttemptID))
		}
		definition.Selection = &AttemptSelection{AttemptID: payload.AttemptID, Selected: provenance(event)}
	}

	for _, event := range applicationEvents {
		var payload caseAttemptApplyPayload
		if err := decodeRelationship(event, &payload); err != nil {
			return Projection{}, err
		}
		definition, link, err := linkedAttempt(casesByID, payload.CaseID, payload.AttemptID)
		if err != nil {
			return Projection{}, relationshipError(event, err)
		}
		if err := validateCaseMutationEnvelope(event, payload.Scope, payload.Source, *definition); err != nil {
			return Projection{}, err
		}
		if definition.Selection == nil || definition.Selection.AttemptID != payload.AttemptID {
			return Projection{}, relationshipError(event, fmt.Errorf("attempt %s was applied without being selected", payload.AttemptID))
		}
		if link.Result == nil || link.Result.PostCommit != payload.PostCommit {
			return Projection{}, relationshipError(event, fmt.Errorf("applied commit does not match attempt %s result", payload.AttemptID))
		}
		if _, err := primitives.ParseCommitSHA(payload.SafetyCommit.String()); err != nil {
			return Projection{}, relationshipError(event, fmt.Errorf("apply safety commit: %w", err))
		}
		if payload.SafetyRef == "" || payload.Changes < 0 {
			return Projection{}, relationshipError(event, fmt.Errorf("attempt application is missing valid safety metadata"))
		}
		definition.Applications = append(definition.Applications, AttemptApplication{AttemptID: payload.AttemptID, PostCommit: payload.PostCommit, SafetyRef: payload.SafetyRef, SafetyCommit: payload.SafetyCommit, Changes: payload.Changes, Applied: provenance(event)})
	}

	result := Projection{Version: JSONVersion, Tasks: make([]Task, 0, len(tasks)), Cases: make([]Case, 0, len(casesByID))}
	for _, task := range tasks {
		result.Tasks = append(result.Tasks, *task)
	}
	for _, definition := range casesByID {
		sort.Slice(definition.AttemptLinks, func(i, j int) bool {
			return definition.AttemptLinks[i].AttemptID < definition.AttemptLinks[j].AttemptID
		})
		result.Cases = append(result.Cases, *definition)
	}
	sort.Slice(result.Tasks, func(i, j int) bool { return result.Tasks[i].ID < result.Tasks[j].ID })
	sort.Slice(result.Cases, func(i, j int) bool { return result.Cases[i].ID < result.Cases[j].ID })
	return result, nil
}

func validateTaskCreate(event eventlog.Event, payload taskCreatePayload, expected Scope) error {
	if _, err := primitives.ParseTaskID(payload.TaskID.String()); err != nil {
		return relationshipError(event, err)
	}
	if payload.Scope.RepoID != expected.RepoID || payload.Scope.StoreID != expected.StoreID {
		return relationshipError(event, scopeMismatch("task", payload.TaskID))
	}
	if payload.InitialRevision.Revision != 1 {
		return relationshipError(event, fmt.Errorf("initial task revision must be 1"))
	}
	if err := validateInstruction(payload.InitialRevision.Instruction); err != nil {
		return relationshipError(event, err)
	}
	if err := validateEnvelope(event, payload.Scope, payload.InitialRevision.Source); err != nil {
		return relationshipError(event, err)
	}
	return nil
}

func validateTaskRevision(event eventlog.Event, payload taskRevisionPayload, expected Scope) error {
	if _, err := primitives.ParseTaskID(payload.TaskID.String()); err != nil {
		return relationshipError(event, err)
	}
	if payload.Revision == 0 {
		return relationshipError(event, fmt.Errorf("task revision must be greater than zero"))
	}
	if payload.Scope.RepoID != expected.RepoID || payload.Scope.StoreID != expected.StoreID {
		return relationshipError(event, scopeMismatch("task", payload.TaskID))
	}
	if err := validateInstruction(payload.Instruction); err != nil {
		return relationshipError(event, err)
	}
	if err := validateEnvelope(event, payload.Scope, payload.Source); err != nil {
		return relationshipError(event, err)
	}
	return nil
}

func validateCaseCreate(event eventlog.Event, payload caseCreatePayload, expected Scope, task Task) error {
	if _, err := primitives.ParseCaseID(payload.CaseID.String()); err != nil {
		return relationshipError(event, err)
	}
	if payload.Scope.RepoID != expected.RepoID || payload.Scope.StoreID != expected.StoreID || payload.Scope != task.Scope {
		return relationshipError(event, scopeMismatch("case", payload.CaseID))
	}
	if err := validateEnvelope(event, payload.Scope, payload.Source); err != nil {
		return relationshipError(event, err)
	}
	if payload.Readiness.Version != fork.ReportVersion {
		return relationshipError(event, fmt.Errorf("unsupported fork-readiness version %d", payload.Readiness.Version))
	}
	if payload.Readiness.Target != fmt.Sprintf("%s:turn:%s:pre", payload.Source.SessionID, payload.Source.TurnID) {
		return relationshipError(event, fmt.Errorf("fork-readiness target does not match case source"))
	}
	if payload.Readiness.Source.SessionID != payload.Source.SessionID || payload.Readiness.Source.TurnID != payload.Source.TurnID || payload.Readiness.Source.WorktreeID != payload.Scope.WorktreeID || payload.Readiness.Source.StreamID != payload.Source.StreamID {
		return relationshipError(event, fmt.Errorf("fork-readiness source does not match case source"))
	}
	if payload.Readiness.Base.Status != "available" || payload.Readiness.Base.Phase != primitives.CheckpointPhasePre || payload.Readiness.Base.Ref == "" || payload.Readiness.Base.CommitSHA == "" {
		return relationshipError(event, fmt.Errorf("case requires an available pre-turn checkpoint"))
	}
	parts, err := payload.Readiness.Base.Ref.Parts()
	if err != nil {
		return relationshipError(event, fmt.Errorf("case base checkpoint ref: %w", err))
	}
	if !parts.HasPhase || parts.SessionID != payload.Source.SessionID || parts.TurnID != payload.Source.TurnID || parts.Phase != primitives.CheckpointPhasePre || (parts.Scoped && (parts.WorktreeID != payload.Scope.WorktreeID || parts.StreamID != payload.Source.StreamID)) {
		return relationshipError(event, fmt.Errorf("case base checkpoint ref does not match source turn"))
	}
	if !sameObservableInstruction(payload.Readiness.Instruction, task.Revisions[payload.TaskRevision-1].Instruction) {
		return relationshipError(event, fmt.Errorf("case instruction does not match task revision %d", payload.TaskRevision))
	}
	if err := validateVerifierSnapshots(payload.Verifiers); err != nil {
		return relationshipError(event, fmt.Errorf("case contains an invalid verifier contract: %w", err))
	}
	return nil
}

func sameObservableInstruction(left, right fork.Instruction) bool {
	return left.Status == right.Status && left.Text == right.Text
}

func validateVerifierSnapshots(snapshots []Verifier) error {
	definitions := make([]config.Verifier, 0, len(snapshots))
	for _, snapshot := range snapshots {
		timeout, err := time.ParseDuration(snapshot.Timeout)
		if err != nil {
			return fmt.Errorf("verifier %q timeout: %w", snapshot.Name, err)
		}
		definitions = append(definitions, config.Verifier{Name: snapshot.Name, Command: snapshot.Command, Args: append([]string(nil), snapshot.Args...), Timeout: timeout})
	}
	if len(definitions) == 0 {
		return nil
	}
	return verifierengine.ValidateDefinitions(definitions)
}

func validateAttemptLink(event eventlog.Event, payload caseAttemptLinkPayload, definition Case, attempt attemptRecord) error {
	if _, err := primitives.ParseAttemptID(payload.AttemptID.String()); err != nil {
		return relationshipError(event, err)
	}
	if _, err := primitives.ParseRunID(payload.RunID.String()); err != nil {
		return relationshipError(event, err)
	}
	if payload.Scope != definition.Scope || payload.Scope != attempt.Scope {
		return relationshipError(event, scopeMismatch("attempt", payload.AttemptID))
	}
	execution := payload.Source
	if payload.Execution != nil {
		execution = *payload.Execution
	}
	if execution.SessionID == "" {
		execution = payload.Source
	}
	if err := validateSource(execution); err != nil {
		return relationshipError(event, fmt.Errorf("attempt execution source: %w", err))
	}
	if payload.RunID != attempt.RunID || execution != attempt.Source || payload.Source != definition.Source {
		return relationshipError(event, fmt.Errorf("attempt %s does not match its run execution or case source", payload.AttemptID))
	}
	if err := validateEnvelope(event, payload.Scope, payload.Source); err != nil {
		return relationshipError(event, err)
	}
	return nil
}

func validateAttemptResult(event eventlog.Event, payload caseAttemptResultPayload, definition Case, link AttemptLink) error {
	if payload.Scope != definition.Scope || payload.RunID != link.RunID || payload.Source != definition.Source {
		return relationshipError(event, fmt.Errorf("attempt %s result does not match its case link", payload.AttemptID))
	}
	if err := validateEnvelope(event, payload.Scope, payload.Source); err != nil {
		return relationshipError(event, err)
	}
	if payload.Status != AttemptStatusSucceeded && payload.Status != AttemptStatusFailed && payload.Status != AttemptStatusIncomplete {
		return relationshipError(event, fmt.Errorf("invalid attempt result status %q", payload.Status))
	}
	if _, err := primitives.ParseCommitSHA(payload.PostCommit.String()); err != nil {
		return relationshipError(event, fmt.Errorf("attempt result commit: %w", err))
	}
	parts, err := payload.PostRef.Parts()
	if err != nil {
		return relationshipError(event, fmt.Errorf("attempt result ref: %w", err))
	}
	if !parts.HasPhase || parts.SessionID != link.Execution.SessionID || parts.TurnID != link.Execution.TurnID || parts.Phase != primitives.CheckpointPhasePost {
		return relationshipError(event, fmt.Errorf("attempt result ref does not match execution checkpoint"))
	}
	if payload.Status == AttemptStatusSucceeded && payload.ExitCode != nil && *payload.ExitCode != 0 {
		return relationshipError(event, fmt.Errorf("successful attempt has nonzero exit code %d", *payload.ExitCode))
	}
	return nil
}

func validateCaseMutationEnvelope(event eventlog.Event, scope Scope, source SourceTurn, definition Case) error {
	if scope != definition.Scope || source != definition.Source {
		return relationshipError(event, fmt.Errorf("case mutation does not match immutable case scope or source"))
	}
	if err := validateEnvelope(event, scope, source); err != nil {
		return relationshipError(event, err)
	}
	return nil
}

func linkedAttempt(casesByID map[primitives.CaseID]*Case, caseID primitives.CaseID, attemptID primitives.AttemptID) (*Case, *AttemptLink, error) {
	definition, exists := casesByID[caseID]
	if !exists {
		return nil, nil, fmt.Errorf("attempt relationship references nonexistent case %s", caseID)
	}
	for index := range definition.AttemptLinks {
		if definition.AttemptLinks[index].AttemptID == attemptID {
			return definition, &definition.AttemptLinks[index], nil
		}
	}
	return nil, nil, fmt.Errorf("attempt %s is not linked to case %s", attemptID, caseID)
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func validateInstruction(instruction fork.Instruction) error {
	switch instruction.Status {
	case fork.InstructionAvailable:
		if instruction.Text == "" {
			return fmt.Errorf("available instruction text must not be empty")
		}
	case fork.InstructionRedacted, fork.InstructionMissing:
		if instruction.Text != "" {
			return fmt.Errorf("%s instruction must not contain text", instruction.Status)
		}
	default:
		return fmt.Errorf("invalid instruction status %q", instruction.Status)
	}
	return nil
}

func validateEnvelope(event eventlog.Event, scope Scope, source SourceTurn) error {
	if err := validateScope(scope); err != nil {
		return err
	}
	if event.RepoID != scope.RepoID || event.WorktreeID != scope.WorktreeID {
		return fmt.Errorf("event envelope does not match repository or worktree scope")
	}
	return validateEventSource(event, source)
}

func validateEventSource(event eventlog.Event, source SourceTurn) error {
	if err := validateSource(source); err != nil {
		return err
	}
	if event.SessionID != source.SessionID || event.TurnID != nil {
		return fmt.Errorf("event envelope does not match source turn")
	}
	return nil
}

func decodeRelationship(event eventlog.Event, destination any) error {
	if err := json.Unmarshal(event.Payload, destination); err != nil {
		return relationshipError(event, err)
	}
	return nil
}

func relationshipError(event eventlog.Event, cause error) error {
	return fmt.Errorf("task/case relationship invariant failed for session %s event %s (%s): %w", event.SessionID, event.Seq, event.Type, cause)
}

func provenance(event eventlog.Event) Provenance {
	return Provenance{SessionID: event.SessionID, TurnID: event.TurnID, StreamID: event.StreamID, EventSeq: event.Seq, EventType: event.Type}
}

func cloneVerifiers(verifiers []Verifier) []Verifier {
	result := make([]Verifier, len(verifiers))
	for index, verifier := range verifiers {
		result[index] = verifier
		result[index].Args = append([]string(nil), verifier.Args...)
	}
	return result
}
