package cases

import (
	"fmt"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
)

// Delete appends a tombstone for an active Case. Its source and attempt
// sessions remain protected until this record is durable.
func Delete(repo *checkpoint.Repo, caseID primitives.CaseID) (Case, error) {
	if repo == nil {
		return Case{}, fmt.Errorf("delete case requires checkpoint repo")
	}
	var deleted Case
	err := repo.WithWorkspaceLock("delete case", func() error {
		projection, err := Rebuild(repo)
		if err != nil {
			return err
		}
		definition, ok := projection.Case(caseID)
		if !ok {
			return fmt.Errorf("case %s does not exist in this Turnal store", caseID)
		}
		if err := validateCaseRepoScope(repo, definition); err != nil {
			return err
		}
		for _, link := range definition.AttemptLinks {
			if link.Result == nil {
				return fmt.Errorf("case %s cannot be deleted while attempt %s is still running", caseID, link.AttemptID)
			}
		}
		payload := caseDeletePayload{CaseID: caseID, Scope: definition.Scope, Source: definition.Source}
		if _, err := appendRecord(repo, definition.Source, caseAdapter(definition), primitives.EventTypeCaseDelete, fmt.Sprintf("case:%s:delete", caseID), payload); err != nil {
			return err
		}
		updated, err := Rebuild(repo)
		if err != nil {
			return err
		}
		if _, exists := updated.Case(caseID); exists {
			return fmt.Errorf("deleted case %s remains in durable case projection", caseID)
		}
		deleted = definition
		return nil
	})
	return deleted, err
}
