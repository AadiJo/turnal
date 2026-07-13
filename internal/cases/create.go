package cases

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/config"
	eventlog "github.com/AadiJo/turnal/internal/events"
	forkengine "github.com/AadiJo/turnal/internal/fork"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/runs"
)

type CreateRequest struct {
	SessionID primitives.SessionID
	TurnID    primitives.TurnID
	TaskID    primitives.TaskID
}

type CreateResult struct {
	TaskCreated bool `json:"task_created"`
	Task        Task `json:"task"`
	Case        Case `json:"case"`
}

// Create promotes a recorded turn into an immutable Case without materializing
// its checkpoint or changing the active workspace.
func Create(repo *checkpoint.Repo, request CreateRequest) (CreateResult, error) {
	if repo == nil {
		return CreateResult{}, fmt.Errorf("case creation requires checkpoint repo")
	}
	if _, err := primitives.ParseSessionID(request.SessionID.String()); err != nil {
		return CreateResult{}, err
	}
	if _, err := primitives.NewTurnID(request.TurnID.Uint64()); err != nil {
		return CreateResult{}, err
	}
	if request.TaskID != "" {
		if _, err := primitives.ParseTaskID(request.TaskID.String()); err != nil {
			return CreateResult{}, err
		}
	}

	var result CreateResult
	err := repo.WithWorkspaceLock("create case", func() error {
		var err error
		result, err = createLocked(repo, request)
		return err
	})
	return result, err
}

func createLocked(repo *checkpoint.Repo, request CreateRequest) (CreateResult, error) {
	readiness, err := forkengine.NewAnalyzer(repo).Inspect(request.SessionID, request.TurnID)
	if err != nil {
		return CreateResult{}, err
	}
	if readiness.Base.Status != "available" {
		return CreateResult{}, fmt.Errorf("case creation requires a recorded pre-turn checkpoint for %s:%s", request.SessionID, request.TurnID)
	}
	if readiness.Source.WorktreeID != repo.WorktreeID {
		return CreateResult{}, fmt.Errorf("source turn belongs to worktree %s, expected %s", readiness.Source.WorktreeID, repo.WorktreeID)
	}

	projection, err := Rebuild(repo)
	if err != nil {
		return CreateResult{}, err
	}
	scope := Scope{RepoID: repo.RepoID, StoreID: repo.StoreID, WorktreeID: repo.WorktreeID}
	source := SourceTurn{SessionID: readiness.Source.SessionID, TurnID: readiness.Source.TurnID, StreamID: readiness.Source.StreamID}
	if err := validateSourceStream(repo, source); err != nil {
		return CreateResult{}, err
	}
	adapter := readiness.Source.MetadataAdapter
	if adapter == "" {
		adapter = readiness.Instruction.Adapter
	}
	if adapter == "" && len(readiness.Source.Adapters) > 0 {
		adapter = readiness.Source.Adapters[0]
	}

	taskCreated := false
	taskID := request.TaskID
	var task Task
	var revision TaskRevision
	var newTaskPayload *taskCreatePayload
	if taskID == "" {
		taskID, err = primitives.NewTaskID()
		if err != nil {
			return CreateResult{}, err
		}
		taskCreated = true
		payload := taskCreatePayload{
			TaskID: taskID, Scope: scope,
			InitialRevision: revisionDefinition{Revision: 1, Instruction: readiness.Instruction, Source: source},
		}
		newTaskPayload = &payload
		task = Task{ID: taskID, Scope: scope}
		revision = TaskRevision{Number: 1, Scope: scope, Instruction: readiness.Instruction, Source: source}
	} else {
		var exists bool
		task, exists = projection.Task(taskID)
		if !exists {
			return CreateResult{}, fmt.Errorf("task %s does not exist in this Turnal store", taskID)
		}
		if task.Scope != scope {
			return CreateResult{}, scopeMismatch("task", taskID)
		}
		if len(task.Revisions) == 0 {
			return CreateResult{}, fmt.Errorf("task %s has no applicable revision", taskID)
		}
		revision = task.Revisions[len(task.Revisions)-1]
		if !sameObservableInstruction(revision.Instruction, readiness.Instruction) {
			return CreateResult{}, fmt.Errorf("source turn instruction does not match task %s revision %d; instruction editing requires a new task revision", taskID, revision.Number)
		}
	}

	verifiers, limitations, err := snapshotRepositoryContract(repo, readiness)
	if err != nil {
		return CreateResult{}, err
	}
	caseID, err := primitives.NewCaseID()
	if err != nil {
		return CreateResult{}, err
	}
	casePayload := caseCreatePayload{
		CaseID: caseID, TaskID: taskID, TaskRevision: revision.Number,
		Scope: scope, Source: source, Readiness: readiness,
		Verifiers: verifiers, Limitations: limitations,
	}
	linkedAttempts, err := sourceAttempts(repo, source, scope)
	if err != nil {
		return CreateResult{}, err
	}
	if newTaskPayload != nil {
		if _, err := appendRecord(repo, source, adapter, primitives.EventTypeTaskCreate, "task:"+taskID.String()+":create", *newTaskPayload); err != nil {
			return CreateResult{}, err
		}
	}
	if _, err := appendRecord(repo, source, adapter, primitives.EventTypeCaseCreate, "case:"+caseID.String()+":create", casePayload); err != nil {
		return CreateResult{}, err
	}

	for _, attempt := range linkedAttempts {
		payload := caseAttemptLinkPayload{CaseID: caseID, Scope: scope, RunID: attempt.RunID, AttemptID: attempt.AttemptID, Source: source}
		if _, err := appendRecord(repo, source, adapter, primitives.EventTypeCaseAttemptLink, fmt.Sprintf("case:%s:attempt:%s", caseID, attempt.AttemptID), payload); err != nil {
			return CreateResult{}, err
		}
	}

	projection, err = Rebuild(repo)
	if err != nil {
		return CreateResult{}, err
	}
	createdTask, ok := projection.Task(taskID)
	if !ok {
		return CreateResult{}, fmt.Errorf("created task %s is missing from durable projection", taskID)
	}
	createdCase, ok := projection.Case(caseID)
	if !ok {
		return CreateResult{}, fmt.Errorf("created case %s is missing from durable projection", caseID)
	}
	return CreateResult{TaskCreated: taskCreated, Task: createdTask, Case: createdCase}, nil
}

func validateSourceStream(repo *checkpoint.Repo, source SourceTurn) error {
	streams, err := eventlog.ListDurableStreams(repo.MetadataDir)
	if err != nil {
		return err
	}
	for _, stream := range streams {
		if stream.SessionID != source.SessionID {
			continue
		}
		if source.StreamID != "" && stream.StreamID != source.StreamID {
			continue
		}
		if source.StreamID == "" && !stream.Legacy {
			continue
		}
		if stream.RepoID != repo.RepoID || (stream.WorktreeID != "" && stream.WorktreeID != repo.WorktreeID) {
			return fmt.Errorf("source turn belongs to a different repository or worktree")
		}
		for _, event := range stream.Events {
			if event.TurnID != nil && *event.TurnID == source.TurnID {
				return nil
			}
		}
	}
	return fmt.Errorf("source turn %s:%s does not exist in the expected durable event stream", source.SessionID, source.TurnID)
}

func snapshotRepositoryContract(repo *checkpoint.Repo, readiness forkengine.Report) ([]Verifier, []string, error) {
	effective, origins, err := config.ResolvePath(filepath.Join(repo.MetadataDir, "config.toml"), config.Overrides{})
	if err != nil {
		return nil, nil, fmt.Errorf("validate repository verifier contract: %w", err)
	}
	var definitions []Verifier
	if origins["verify"] == config.OriginWorkspace {
		for _, definition := range effective.Verify {
			definitions = append(definitions, Verifier{Name: definition.Name, Command: definition.Command, Args: append([]string(nil), definition.Args...), Timeout: definition.Timeout.String()})
		}
	}
	limitations := append([]string(nil), readiness.Limitations...)
	limitations = append(limitations,
		"Git-ignored files are outside the captured workspace surface.",
		"Secret values require explicit reauthorization and are never replayed from the Case.",
		"External services and network state remain live unless separately controlled.",
	)
	if len(effective.Secrets.SnapshotDenyGlobs) > 0 {
		limitations = append(limitations, "Secrets-denied path patterns at Case creation: "+strings.Join(effective.Secrets.SnapshotDenyGlobs, ", "))
	}
	return definitions, deduplicateStrings(limitations), nil
}

func sourceAttempts(repo *checkpoint.Repo, source SourceTurn, scope Scope) ([]attemptRecord, error) {
	inventory, err := runs.Inspect(repo)
	if err != nil {
		return nil, err
	}
	var matches []attemptRecord
	for _, run := range inventory.Runs {
		if run.RepoID != scope.RepoID || run.StoreID != scope.StoreID || run.WorktreeID != scope.WorktreeID {
			continue
		}
		for _, attempt := range run.Attempts {
			candidate := SourceTurn{SessionID: attempt.SessionID, TurnID: attempt.TurnID, StreamID: attempt.Provenance.StreamID}
			if candidate == source {
				matches = append(matches, attemptRecord{RunID: run.ID, AttemptID: attempt.ID, Scope: scope, Source: source})
			}
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].AttemptID < matches[j].AttemptID })
	return matches, nil
}

func appendRecord(repo *checkpoint.Repo, source SourceTurn, adapter primitives.AdapterName, eventType primitives.EventType, sourceID string, payload any) (eventlog.Event, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return eventlog.Event{}, err
	}
	return repo.EventLog().Append(eventlog.AppendInput{SessionID: source.SessionID, Type: eventType, Adapter: adapter, SourceID: sourceID, Payload: encoded})
}

func deduplicateStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			result = append(result, value)
			seen[value] = true
		}
	}
	return result
}
