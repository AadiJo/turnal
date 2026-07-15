package cases

import (
	"github.com/AadiJo/turnal/internal/fork"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/verifier"
)

type taskCreatePayload struct {
	TaskID          primitives.TaskID  `json:"task_id"`
	Scope           Scope              `json:"scope"`
	InitialRevision revisionDefinition `json:"initial_revision"`
}

type revisionDefinition struct {
	Revision    uint64           `json:"revision"`
	Instruction fork.Instruction `json:"observable_problem"`
	Source      SourceTurn       `json:"source"`
}

type taskRevisionPayload struct {
	TaskID primitives.TaskID `json:"task_id"`
	Scope  Scope             `json:"scope"`
	revisionDefinition
}

type caseCreatePayload struct {
	CaseID       primitives.CaseID `json:"case_id"`
	TaskID       primitives.TaskID `json:"task_id"`
	TaskRevision uint64            `json:"task_revision"`
	Scope        Scope             `json:"scope"`
	Source       SourceTurn        `json:"source"`
	Readiness    fork.Report       `json:"fork_readiness"`
	Verifiers    []Verifier        `json:"verifiers"`
	Limitations  []string          `json:"limitations"`
}

type caseDeletePayload struct {
	CaseID primitives.CaseID `json:"case_id"`
	Scope  Scope             `json:"scope"`
	Source SourceTurn        `json:"source"`
}

type caseAttemptLinkPayload struct {
	CaseID    primitives.CaseID    `json:"case_id"`
	Scope     Scope                `json:"scope"`
	RunID     primitives.RunID     `json:"run_id"`
	AttemptID primitives.AttemptID `json:"attempt_id"`
	Source    SourceTurn           `json:"source"`
	Execution *SourceTurn          `json:"execution,omitempty"`
	Command   []string             `json:"command,omitempty"`
}

type caseAttemptResultPayload struct {
	CaseID       primitives.CaseID        `json:"case_id"`
	Scope        Scope                    `json:"scope"`
	RunID        primitives.RunID         `json:"run_id"`
	AttemptID    primitives.AttemptID     `json:"attempt_id"`
	Source       SourceTurn               `json:"source"`
	PostRef      primitives.CheckpointRef `json:"post_ref"`
	PostCommit   primitives.CommitSHA     `json:"post_commit"`
	Status       string                   `json:"status"`
	ExitCode     *int                     `json:"exit_code,omitempty"`
	Error        string                   `json:"error,omitempty"`
	Verification *verifier.Report         `json:"verification,omitempty"`
}

type caseAttemptSelectPayload struct {
	CaseID    primitives.CaseID    `json:"case_id"`
	Scope     Scope                `json:"scope"`
	AttemptID primitives.AttemptID `json:"attempt_id"`
	Source    SourceTurn           `json:"source"`
}

type caseAttemptApplyPayload struct {
	CaseID       primitives.CaseID    `json:"case_id"`
	Scope        Scope                `json:"scope"`
	AttemptID    primitives.AttemptID `json:"attempt_id"`
	Source       SourceTurn           `json:"source"`
	PostCommit   primitives.CommitSHA `json:"post_commit"`
	SafetyRef    string               `json:"safety_ref"`
	SafetyCommit primitives.CommitSHA `json:"safety_commit"`
	Changes      int                  `json:"changes"`
}

type attemptRecord struct {
	RunID     primitives.RunID
	AttemptID primitives.AttemptID
	Scope     Scope
	Source    SourceTurn
}
