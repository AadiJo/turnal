package runs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/filelock"
	"github.com/AadiJo/turnal/internal/primitives"
)

const lifecycleJournalVersion = 1

type lifecycleJournal struct {
	Version    int                   `json:"version"`
	RunID      primitives.RunID      `json:"run_id"`
	SessionID  primitives.SessionID  `json:"session_id"`
	RepoID     primitives.RepoID     `json:"repo_id"`
	StoreID    primitives.StoreID    `json:"store_id"`
	WorktreeID primitives.WorktreeID `json:"worktree_id"`
	StartedAt  string                `json:"started_at"`
}

// Begin records recoverable lifecycle intent before creating the durable Run.
// The returned release function must remain deferred for the process lifetime.
func Begin(repo *checkpoint.Repo, runID primitives.RunID, sessionID primitives.SessionID, command []string) (func(), error) {
	if err := validateRepoAndRun(repo, runID); err != nil {
		return nil, err
	}
	lock, err := filelock.Acquire(lifecycleLockPath(repo, runID), 0)
	if err != nil {
		return nil, fmt.Errorf("acquire run lifecycle lock: %w", err)
	}
	release := func() { _ = lock.Release() }
	journal := lifecycleJournal{Version: lifecycleJournalVersion, RunID: runID, SessionID: sessionID, RepoID: repo.RepoID, StoreID: repo.StoreID, WorktreeID: repo.WorktreeID, StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := writeLifecycleJournal(repo, journal); err != nil {
		release()
		return nil, err
	}
	if err := Start(repo, runID, sessionID, command); err != nil {
		_ = clearLifecycleJournal(repo, runID)
		release()
		return nil, err
	}
	return release, nil
}

// RecoverAbandoned terminalizes Runs whose lifecycle journal remains but whose
// owning process no longer holds its lock. Live concurrent Runs are untouched.
func RecoverAbandoned(repo *checkpoint.Repo) error {
	if repo == nil {
		return nil
	}
	dir := lifecycleDir(repo)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read run lifecycle journals: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		journal, err := readLifecycleJournal(path)
		if err != nil {
			return err
		}
		held, err := filelock.Held(lifecycleLockPath(repo, journal.RunID))
		if err != nil {
			return err
		}
		if held {
			continue
		}
		if journal.RepoID != repo.RepoID || journal.StoreID != repo.StoreID || journal.WorktreeID != repo.WorktreeID {
			return fmt.Errorf("run lifecycle invariant failed for %s: repository store or worktree identity mismatch", journal.RunID)
		}
		projection, err := Read(repo, journal.RunID)
		if err != nil {
			if strings.Contains(err.Error(), "does not exist") {
				if err := clearLifecycleJournal(repo, journal.RunID); err != nil {
					return err
				}
				continue
			}
			return err
		}
		if projection.Status == StatusRunning {
			if err := finish(repo, projection, journal.SessionID, StatusIncomplete, "recovered abandoned run after its owner process exited"); err != nil {
				return err
			}
		}
		if err := clearLifecycleJournal(repo, journal.RunID); err != nil {
			return err
		}
	}
	return nil
}

func lifecycleDir(repo *checkpoint.Repo) string { return filepath.Join(repo.TmpDir, "runs") }
func lifecycleJournalPath(repo *checkpoint.Repo, runID primitives.RunID) string {
	return filepath.Join(lifecycleDir(repo), runID.String()+".json")
}
func lifecycleLockPath(repo *checkpoint.Repo, runID primitives.RunID) string {
	return filepath.Join(lifecycleDir(repo), runID.String()+".lock")
}

func writeLifecycleJournal(repo *checkpoint.Repo, journal lifecycleJournal) error {
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := lifecycleDir(repo)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create run lifecycle dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".run-*.tmp")
	if err != nil {
		return fmt.Errorf("create run lifecycle journal: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, lifecycleJournalPath(repo, journal.RunID)); err != nil {
		return fmt.Errorf("commit run lifecycle journal: %w", err)
	}
	return nil
}

func readLifecycleJournal(path string) (lifecycleJournal, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return lifecycleJournal{}, err
	}
	var journal lifecycleJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return lifecycleJournal{}, fmt.Errorf("run lifecycle invariant failed at %s: %w", path, err)
	}
	if journal.Version != lifecycleJournalVersion {
		return lifecycleJournal{}, fmt.Errorf("run lifecycle invariant failed at %s: unsupported version %d", path, journal.Version)
	}
	if _, err := primitives.ParseRunID(journal.RunID.String()); err != nil {
		return lifecycleJournal{}, err
	}
	if _, err := primitives.ParseSessionID(journal.SessionID.String()); err != nil {
		return lifecycleJournal{}, err
	}
	return journal, nil
}

func clearLifecycleJournal(repo *checkpoint.Repo, runID primitives.RunID) error {
	err := os.Remove(lifecycleJournalPath(repo, runID))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear run lifecycle journal: %w", err)
	}
	return nil
}
