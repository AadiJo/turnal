package cases

import (
	"github.com/AadiJo/turnal/internal/fork"
	"github.com/AadiJo/turnal/internal/primitives"
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

type caseAttemptLinkPayload struct {
	CaseID    primitives.CaseID    `json:"case_id"`
	Scope     Scope                `json:"scope"`
	RunID     primitives.RunID     `json:"run_id"`
	AttemptID primitives.AttemptID `json:"attempt_id"`
	Source    SourceTurn           `json:"source"`
}

type attemptRecord struct {
	RunID     primitives.RunID
	AttemptID primitives.AttemptID
	Scope     Scope
	Source    SourceTurn
}
