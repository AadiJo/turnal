package experiments

import (
	"fmt"

	"github.com/AadiJo/turnal/internal/cases"
	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
)

const CompareVersion = 1

type FileStat struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary"`
}

type AttemptComparison struct {
	AttemptID   primitives.AttemptID     `json:"attempt_id"`
	RunID       primitives.RunID         `json:"run_id"`
	Status      string                   `json:"status"`
	ExitCode    *int                     `json:"exit_code,omitempty"`
	PostRef     primitives.CheckpointRef `json:"post_ref,omitempty"`
	PostCommit  primitives.CommitSHA     `json:"post_commit,omitempty"`
	Selected    bool                     `json:"selected"`
	Command     []string                 `json:"command,omitempty"`
	Files       []FileStat               `json:"files,omitempty"`
	Additions   int                      `json:"additions"`
	Deletions   int                      `json:"deletions"`
	BinaryFiles int                      `json:"binary_files"`
	Patch       string                   `json:"patch,omitempty"`
}

type Comparison struct {
	Version    int                      `json:"version"`
	CaseID     primitives.CaseID        `json:"case_id"`
	BaseRef    primitives.CheckpointRef `json:"base_ref"`
	BaseCommit primitives.CommitSHA     `json:"base_commit"`
	Attempts   []AttemptComparison      `json:"attempts"`
}

func Compare(repo *checkpoint.Repo, caseID primitives.CaseID, patchAttempt primitives.AttemptID) (Comparison, error) {
	if repo == nil {
		return Comparison{}, fmt.Errorf("compare requires checkpoint repo")
	}
	projection, err := cases.Rebuild(repo)
	if err != nil {
		return Comparison{}, err
	}
	definition, ok := projection.Case(caseID)
	if !ok {
		return Comparison{}, fmt.Errorf("case %s does not exist in this Turnal store", caseID)
	}
	baseCommit, err := repo.CheckpointCommit(definition.Readiness.Base.Ref)
	if err != nil {
		return Comparison{}, fmt.Errorf("resolve case base checkpoint: %w", err)
	}
	if baseCommit != definition.Readiness.Base.CommitSHA {
		return Comparison{}, fmt.Errorf("case base checkpoint invariant failed: ref %s points to %s, case records %s", definition.Readiness.Base.Ref, baseCommit, definition.Readiness.Base.CommitSHA)
	}
	comparison := Comparison{Version: CompareVersion, CaseID: definition.ID, BaseRef: definition.Readiness.Base.Ref, BaseCommit: baseCommit}
	patchFound := patchAttempt == ""
	for _, link := range definition.AttemptLinks {
		attempt := AttemptComparison{AttemptID: link.AttemptID, RunID: link.RunID, Status: cases.AttemptStatusRunning, Command: append([]string(nil), link.Command...)}
		attempt.Selected = definition.Selection != nil && definition.Selection.AttemptID == link.AttemptID
		if link.Result != nil {
			attempt.Status = link.Result.Status
			attempt.ExitCode = cloneInt(link.Result.ExitCode)
			attempt.PostRef = link.Result.PostRef
			attempt.PostCommit = link.Result.PostCommit
			commit, err := repo.CheckpointCommit(link.Result.PostRef)
			if err != nil {
				return Comparison{}, fmt.Errorf("resolve attempt %s result checkpoint: %w", link.AttemptID, err)
			}
			if commit != link.Result.PostCommit {
				return Comparison{}, fmt.Errorf("attempt %s result invariant failed: ref %s points to %s, result records %s", link.AttemptID, link.Result.PostRef, commit, link.Result.PostCommit)
			}
			summary, err := repo.DiffStatRefs(definition.Readiness.Base.Ref, link.Result.PostRef)
			if err != nil {
				return Comparison{}, fmt.Errorf("compare attempt %s to case base: %w", link.AttemptID, err)
			}
			attempt.Additions, attempt.Deletions, attempt.BinaryFiles = summary.Additions, summary.Deletions, summary.BinaryFiles
			for _, file := range summary.Files {
				attempt.Files = append(attempt.Files, FileStat{Path: file.Path, Additions: file.Additions, Deletions: file.Deletions, Binary: file.Binary})
			}
			if patchAttempt == link.AttemptID {
				patch, err := repo.DiffRefs(definition.Readiness.Base.Ref, link.Result.PostRef)
				if err != nil {
					return Comparison{}, fmt.Errorf("render attempt %s patch: %w", link.AttemptID, err)
				}
				attempt.Patch = string(patch)
				patchFound = true
			}
		}
		comparison.Attempts = append(comparison.Attempts, attempt)
	}
	if !patchFound {
		return Comparison{}, fmt.Errorf("attempt %s is not a completed result in case %s", patchAttempt, caseID)
	}
	return comparison, nil
}
