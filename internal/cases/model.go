// Package cases records and rebuilds durable Task and Case identity.
package cases

import (
	"fmt"
	"sort"

	"github.com/AadiJo/turnal/internal/fork"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/verifier"
)

const JSONVersion = 1

const (
	AttemptStatusRunning    = "running"
	AttemptStatusSucceeded  = "succeeded"
	AttemptStatusFailed     = "failed"
	AttemptStatusIncomplete = "incomplete"
)

type Scope struct {
	RepoID     primitives.RepoID     `json:"repo_id"`
	StoreID    primitives.StoreID    `json:"store_id"`
	WorktreeID primitives.WorktreeID `json:"worktree_id"`
}

type SourceTurn struct {
	SessionID primitives.SessionID     `json:"session_id"`
	TurnID    primitives.TurnID        `json:"turn_id"`
	StreamID  primitives.EventStreamID `json:"stream_id,omitempty"`
}

type Provenance struct {
	SessionID primitives.SessionID     `json:"session_id"`
	TurnID    *primitives.TurnID       `json:"turn_id,omitempty"`
	StreamID  primitives.EventStreamID `json:"stream_id,omitempty"`
	EventSeq  primitives.EventSeq      `json:"event_seq"`
	EventType primitives.EventType     `json:"event_type"`
}

type Task struct {
	ID        primitives.TaskID `json:"task_id"`
	Scope     Scope             `json:"scope"`
	Revisions []TaskRevision    `json:"revisions"`
	Created   Provenance        `json:"created"`
}

type TaskRevision struct {
	Number      uint64           `json:"number"`
	Scope       Scope            `json:"scope"`
	Instruction fork.Instruction `json:"observable_problem"`
	Source      SourceTurn       `json:"source"`
	Created     Provenance       `json:"created"`
}

type Verifier struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Timeout string   `json:"timeout"`
}

type AttemptLink struct {
	AttemptID primitives.AttemptID `json:"attempt_id"`
	RunID     primitives.RunID     `json:"run_id"`
	Source    SourceTurn           `json:"source"`
	Execution SourceTurn           `json:"execution"`
	Command   []string             `json:"command,omitempty"`
	Result    *AttemptResult       `json:"result,omitempty"`
	Created   Provenance           `json:"created"`
}

type AttemptResult struct {
	PostRef      primitives.CheckpointRef `json:"post_ref"`
	PostCommit   primitives.CommitSHA     `json:"post_commit"`
	Status       string                   `json:"status"`
	ExitCode     *int                     `json:"exit_code,omitempty"`
	Error        string                   `json:"error,omitempty"`
	Verification *verifier.Report         `json:"verification,omitempty"`
	Completed    Provenance               `json:"completed"`
}

type AttemptSelection struct {
	AttemptID primitives.AttemptID `json:"attempt_id"`
	Selected  Provenance           `json:"selected"`
}

type AttemptApplication struct {
	AttemptID    primitives.AttemptID `json:"attempt_id"`
	PostCommit   primitives.CommitSHA `json:"post_commit"`
	SafetyRef    string               `json:"safety_ref"`
	SafetyCommit primitives.CommitSHA `json:"safety_commit"`
	Changes      int                  `json:"changes"`
	Applied      Provenance           `json:"applied"`
}

type Case struct {
	ID           primitives.CaseID    `json:"case_id"`
	TaskID       primitives.TaskID    `json:"task_id"`
	TaskRevision uint64               `json:"task_revision"`
	Scope        Scope                `json:"scope"`
	Source       SourceTurn           `json:"source"`
	Readiness    fork.Report          `json:"fork_readiness"`
	Verifiers    []Verifier           `json:"verifiers"`
	Limitations  []string             `json:"limitations"`
	AttemptLinks []AttemptLink        `json:"attempt_links"`
	Selection    *AttemptSelection    `json:"selection,omitempty"`
	Applications []AttemptApplication `json:"applications,omitempty"`
	Created      Provenance           `json:"created"`
}

type Projection struct {
	Version int    `json:"version"`
	Tasks   []Task `json:"tasks"`
	Cases   []Case `json:"cases"`
}

func (projection Projection) Task(id primitives.TaskID) (Task, bool) {
	index := sort.Search(len(projection.Tasks), func(i int) bool { return projection.Tasks[i].ID >= id })
	if index == len(projection.Tasks) || projection.Tasks[index].ID != id {
		return Task{}, false
	}
	return projection.Tasks[index], true
}

func (projection Projection) Case(id primitives.CaseID) (Case, bool) {
	index := sort.Search(len(projection.Cases), func(i int) bool { return projection.Cases[i].ID >= id })
	if index == len(projection.Cases) || projection.Cases[index].ID != id {
		return Case{}, false
	}
	return projection.Cases[index], true
}

func validateScope(scope Scope) error {
	if _, err := primitives.ParseRepoID(scope.RepoID.String()); err != nil {
		return err
	}
	if _, err := primitives.ParseStoreID(scope.StoreID.String()); err != nil {
		return err
	}
	if _, err := primitives.ParseWorktreeID(scope.WorktreeID.String()); err != nil {
		return err
	}
	return nil
}

func validateSource(source SourceTurn) error {
	if _, err := primitives.ParseSessionID(source.SessionID.String()); err != nil {
		return err
	}
	if _, err := primitives.NewTurnID(source.TurnID.Uint64()); err != nil {
		return err
	}
	if source.StreamID != "" {
		if _, err := primitives.ParseEventStreamID(source.StreamID.String()); err != nil {
			return err
		}
	}
	return nil
}

func scopeMismatch(kind string, id fmt.Stringer) error {
	return fmt.Errorf("%s %s belongs to a different repository, store, or worktree", kind, id)
}
