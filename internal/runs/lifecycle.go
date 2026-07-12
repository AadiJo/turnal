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
	"github.com/AadiJo/turnal/internal/processidentity"
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
	OwnerPID   int                   `json:"owner_pid"`
	OwnerStart string                `json:"owner_process_start"`
}

// Begin records recoverable lifecycle intent before creating the durable Run.
// The returned release function must remain deferred for the process lifetime.
func Begin(repo *checkpoint.Repo, runID primitives.RunID, sessionID primitives.SessionID, command []string) (func(), error) {
	return begin(repo, runID, sessionID, command, start)
}

func begin(repo *checkpoint.Repo, runID primitives.RunID, sessionID primitives.SessionID, command []string, startRun func(*checkpoint.Repo, primitives.RunID, primitives.SessionID, []string, processidentity.Identity) error) (func(), error) {
	if err := validateRepoAndRun(repo, runID); err != nil {
		return nil, err
	}
	unlockMutation, err := acquireRunMutation(repo, runID)
	if err != nil {
		return nil, err
	}
	defer unlockMutation()
	if _, err := Read(repo, runID); err == nil {
		return nil, fmt.Errorf("run %s already exists", runID)
	} else if !strings.Contains(err.Error(), "does not exist") {
		return nil, err
	}
	lock, err := filelock.Acquire(lifecycleLockPath(repo, runID), 0)
	if err != nil {
		return nil, fmt.Errorf("acquire run lifecycle lock: %w", err)
	}
	release := func() { _ = lock.Release() }
	owner, err := processidentity.Current()
	if err != nil {
		release()
		return nil, err
	}
	journal := lifecycleJournal{Version: lifecycleJournalVersion, RunID: runID, SessionID: sessionID, RepoID: repo.RepoID, StoreID: repo.StoreID, WorktreeID: repo.WorktreeID, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), OwnerPID: owner.PID, OwnerStart: owner.Started}
	if err := writeLifecycleJournal(repo, journal); err != nil {
		release()
		return nil, err
	}
	if err := startRun(repo, runID, sessionID, command, owner); err != nil {
		// The event append may already be durable even when a later derived-state
		// update fails. Keep the intent so recovery can inspect durable history.
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
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if journal.RepoID == repo.RepoID && journal.StoreID == repo.StoreID && journal.WorktreeID != repo.WorktreeID {
			// Lifecycle journals share the store, but only their owning worktree
			// may recover or mutate the corresponding Run.
			continue
		}
		unlock, err := acquireRunMutation(repo, journal.RunID)
		if err != nil {
			return err
		}
		err = recoverJournalLocked(repo, journal)
		unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

func recoverJournalLocked(repo *checkpoint.Repo, journal lifecycleJournal) error {
	if journal.RepoID != repo.RepoID || journal.StoreID != repo.StoreID || journal.WorktreeID != repo.WorktreeID {
		return fmt.Errorf("run lifecycle invariant failed for %s: repository store or worktree identity mismatch", journal.RunID)
	}
	projection, err := Read(repo, journal.RunID)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return clearLifecycleJournal(repo, journal.RunID)
		}
		return err
	}
	if journal.SessionID != projection.Start.SessionID {
		return fmt.Errorf("run lifecycle invariant failed for %s: journal session %s does not match run-start session %s", journal.RunID, journal.SessionID, projection.Start.SessionID)
	}
	if journal.OwnerPID != projection.OwnerPID || journal.OwnerStart != projection.OwnerStart {
		return fmt.Errorf("run lifecycle invariant failed for %s: journal wrapper identity does not match run start", journal.RunID)
	}
	held, err := filelock.Held(lifecycleLockPath(repo, journal.RunID))
	if err != nil {
		return err
	}
	ownerAlive, err := processidentity.Matches(projection.OwnerPID, projection.OwnerStart)
	if err != nil {
		return err
	}
	if held && ownerAlive {
		return nil
	}
	if projection.Status == StatusRunning {
		if err := finish(repo, projection, journal.SessionID, StatusIncomplete, "recovered abandoned run after its owner process exited"); err != nil {
			return err
		}
	}
	return clearLifecycleJournal(repo, journal.RunID)
}

func recoverRunLocked(repo *checkpoint.Repo, runID primitives.RunID) error {
	journal, err := readLifecycleJournal(lifecycleJournalPath(repo, runID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return recoverJournalLocked(repo, journal)
}

func requireLockedLifecycle(repo *checkpoint.Repo, projection Projection) error {
	journal, err := readLifecycleJournal(lifecycleJournalPath(repo, projection.ID))
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("run %s has no active lifecycle journal", projection.ID)
		}
		return err
	}
	if journal.RunID != projection.ID || journal.SessionID != projection.Start.SessionID || journal.RepoID != projection.RepoID || journal.StoreID != projection.StoreID || journal.WorktreeID != projection.WorktreeID {
		return fmt.Errorf("run lifecycle invariant failed for %s: journal does not match run start", projection.ID)
	}
	if journal.OwnerPID != projection.OwnerPID || journal.OwnerStart != projection.OwnerStart {
		return fmt.Errorf("run lifecycle invariant failed for %s: journal wrapper identity does not match run start", projection.ID)
	}
	held, err := filelock.Held(lifecycleLockPath(repo, projection.ID))
	if err != nil {
		return err
	}
	if !held {
		return fmt.Errorf("run %s lifecycle is not currently locked", projection.ID)
	}
	ownerAlive, err := processidentity.Matches(projection.OwnerPID, projection.OwnerStart)
	if err != nil {
		return err
	}
	if !ownerAlive {
		return fmt.Errorf("run %s original wrapper process is no longer alive", projection.ID)
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
func mutationLockPath(repo *checkpoint.Repo, runID primitives.RunID) string {
	return filepath.Join(lifecycleDir(repo), runID.String()+".mutation.lock")
}

func acquireRunMutation(repo *checkpoint.Repo, runID primitives.RunID) (func(), error) {
	lock, err := filelock.Acquire(mutationLockPath(repo, runID), 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("acquire run %s mutation lock: %w", runID, err)
	}
	return func() { _ = lock.Release() }, nil
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
	if journal.OwnerPID <= 0 || journal.OwnerStart == "" {
		return lifecycleJournal{}, fmt.Errorf("run lifecycle invariant failed at %s: missing original lock owner", path)
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
