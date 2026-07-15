package experiments

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AadiJo/turnal/internal/cases"
	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/runs"
)

var removeManagedWorkspace = os.RemoveAll

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
	referenced := make(map[string]bool)
	for _, definition := range projection.Cases {
		for _, link := range definition.AttemptLinks {
			if link.Workspace != "" && (link.Keep || link.Result == nil) {
				referenced[filepath.Clean(link.Workspace)] = true
			}
			if link.Result == nil {
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
				if err := removeManagedWorkspace(expected); err != nil {
					return fmt.Errorf("remove abandoned fork workspace for attempt %s: %w", link.AttemptID, err)
				}
			}
			verification, err := managedVerificationWorkspacePath(repo, link.AttemptID)
			if err != nil {
				return err
			}
			if err := removeManagedWorkspace(verification); err != nil {
				return fmt.Errorf("remove abandoned verification workspace for attempt %s: %w", link.AttemptID, err)
			}
		}
	}
	if err := removeOrphanedManagedWorkspaces(repo, referenced); err != nil {
		return err
	}
	return nil
}

func removeOrphanedManagedWorkspaces(repo *checkpoint.Repo, referenced map[string]bool) error {
	probe, err := managedWorkspacePath(repo, "probe")
	if err != nil {
		return err
	}
	parent := filepath.Dir(probe)
	entries, err := os.ReadDir(parent)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("scan managed fork workspaces: %w", err)
	}
	for _, entry := range entries {
		path := filepath.Join(parent, entry.Name())
		if referenced[path] {
			continue
		}
		remove := false
		switch {
		case strings.HasPrefix(entry.Name(), "verify-attempt_"):
			remove = true
		case strings.HasPrefix(entry.Name(), "run-run_"):
			runID, parseErr := primitives.ParseRunID(strings.TrimPrefix(entry.Name(), "run-"))
			if parseErr != nil {
				continue
			}
			marker := keepMarkerPath(repo, runID)
			if _, markerErr := os.Stat(marker); markerErr == nil {
				if _, workspaceErr := os.Stat(path); workspaceErr == nil {
					continue
				}
				_ = os.Remove(marker)
			}
			run, readErr := runs.Read(repo, runID)
			remove = readErr != nil || run.Status != runs.StatusRunning
		}
		if remove {
			if err := removeManagedWorkspace(path); err != nil {
				return fmt.Errorf("remove orphaned managed workspace %s: %w", path, err)
			}
		}
	}
	return nil
}

func keepMarkerPath(repo *checkpoint.Repo, runID primitives.RunID) string {
	path, _ := managedForkWorkspacePath(repo, runID)
	return filepath.Join(filepath.Dir(path), ".keep-"+runID.String())
}

func persistKeepMarker(repo *checkpoint.Repo, runID primitives.RunID) error {
	path := keepMarkerPath(repo, runID)
	if err := os.WriteFile(path, []byte("kept by turnal fork --keep\n"), 0o600); err != nil {
		return fmt.Errorf("persist kept fork workspace ownership: %w", err)
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
