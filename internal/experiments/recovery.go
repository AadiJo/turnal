package experiments

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/AadiJo/turnal/internal/cases"
	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/runs"
)

func RecoverAbandoned(repo *checkpoint.Repo) error {
	if repo == nil {
		return nil
	}
	if err := runs.RecoverAbandoned(repo); err != nil {
		return err
	}
	if err := cases.ReconcileAbandonedAttempts(repo); err != nil {
		return err
	}
	projection, err := cases.Rebuild(repo)
	if err != nil {
		return err
	}
	for _, definition := range projection.Cases {
		for _, link := range definition.AttemptLinks {
			if link.Result == nil || link.Result.Status != cases.AttemptStatusIncomplete || link.Result.PostRef != "" {
				continue
			}
			if link.Workspace != "" && !link.Keep {
				expected, err := managedForkWorkspacePath(repo, link.RunID)
				if err != nil {
					return err
				}
				if filepath.Clean(link.Workspace) != expected {
					return fmt.Errorf("abandoned fork workspace invariant failed for attempt %s: recorded %s, expected %s", link.AttemptID, link.Workspace, expected)
				}
				if err := os.RemoveAll(expected); err != nil {
					return fmt.Errorf("remove abandoned fork workspace for attempt %s: %w", link.AttemptID, err)
				}
			}
			verification, err := managedVerificationWorkspacePath(repo, link.AttemptID)
			if err != nil {
				return err
			}
			if err := os.RemoveAll(verification); err != nil {
				return fmt.Errorf("remove abandoned verification workspace for attempt %s: %w", link.AttemptID, err)
			}
		}
	}
	return nil
}

func createManagedForkWorkspace(repo *checkpoint.Repo, runID primitives.RunID) (string, error) {
	path, err := managedForkWorkspacePath(repo, runID)
	if err != nil {
		return "", err
	}
	return createManagedWorkspace(path, "fork")
}

func createManagedVerificationWorkspace(repo *checkpoint.Repo, attemptID primitives.AttemptID) (string, error) {
	path, err := managedVerificationWorkspacePath(repo, attemptID)
	if err != nil {
		return "", err
	}
	return createManagedWorkspace(path, "verification")
}

func managedForkWorkspacePath(repo *checkpoint.Repo, runID primitives.RunID) (string, error) {
	return managedWorkspacePath(repo, "run-"+runID.String())
}

func managedVerificationWorkspacePath(repo *checkpoint.Repo, attemptID primitives.AttemptID) (string, error) {
	return managedWorkspacePath(repo, "verify-"+attemptID.String())
}

func managedWorkspacePath(repo *checkpoint.Repo, name string) (string, error) {
	tempRoot, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return "", fmt.Errorf("resolve system temporary directory: %w", err)
	}
	tempRoot, err = filepath.Abs(tempRoot)
	if err != nil {
		return "", fmt.Errorf("resolve system temporary directory: %w", err)
	}
	return filepath.Join(tempRoot, "turnal-forks-"+repo.StoreID.String(), name), nil
}

func createManagedWorkspace(path, purpose string) (string, error) {
	parent := filepath.Dir(path)
	if err := os.Mkdir(parent, 0o700); err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("create managed %s workspace directory: %w", purpose, err)
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return "", fmt.Errorf("inspect managed %s workspace directory: %w", purpose, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("managed %s workspace parent is not a real directory: %s", purpose, parent)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		return "", fmt.Errorf("secure managed %s workspace directory: %w", purpose, err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return "", fmt.Errorf("create managed %s workspace: %w", purpose, err)
	}
	return path, nil
}
