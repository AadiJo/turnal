package checkpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/AadiJo/turnal/internal/primitives"
)

const checkpointJournalVersion = 2

type CheckpointJournal struct {
	Version          int                        `json:"version"`
	State            string                     `json:"state"`
	StartedAt        string                     `json:"started_at"`
	UpdatedAt        string                     `json:"updated_at"`
	SessionID        primitives.SessionID       `json:"session_id"`
	TurnID           primitives.TurnID          `json:"turn_id"`
	Phase            primitives.CheckpointPhase `json:"phase"`
	Adapter          primitives.AdapterName     `json:"adapter,omitempty"`
	RawRef           string                     `json:"raw_ref,omitempty"`
	WorktreeID       primitives.WorktreeID      `json:"worktree_id,omitempty"`
	StreamID         primitives.EventStreamID   `json:"stream_id,omitempty"`
	CheckpointID     primitives.CheckpointID    `json:"checkpoint_id,omitempty"`
	Ref              primitives.CheckpointRef   `json:"ref,omitempty"`
	CanonicalRef     primitives.CheckpointRef   `json:"canonical_ref,omitempty"`
	CommitSHA        primitives.CommitSHA       `json:"commit_sha,omitempty"`
	GitSyncRef       string                     `json:"git_sync_ref,omitempty"`
	GitSyncCommitSHA string                     `json:"git_sync_commit_sha,omitempty"`
	EventSeq         primitives.EventSeq        `json:"event_seq,omitempty"`
	EventHash        primitives.EventHash       `json:"event_hash,omitempty"`
}

func (repo *Repo) BeginCheckpointJournal(sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase, adapter primitives.AdapterName, rawRef string) error {
	checkpointID, err := primitives.NewCheckpointID()
	if err != nil {
		return err
	}
	streamID, err := repo.StreamID(sessionID)
	if err != nil {
		return err
	}
	journal := CheckpointJournal{
		Version:      checkpointJournalVersion,
		State:        "intent",
		StartedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		SessionID:    sessionID,
		TurnID:       turnID,
		Phase:        phase,
		Adapter:      adapter,
		RawRef:       rawRef,
		WorktreeID:   repo.WorktreeID,
		StreamID:     streamID,
		CheckpointID: checkpointID,
	}
	return repo.writeCheckpointJournal(journal)
}

func (repo *Repo) MarkCheckpointJournalCommitted(sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase, created Checkpoint) error {
	journal, err := repo.loadCheckpointJournal(sessionID, turnID, phase)
	if err != nil {
		return err
	}
	journal.State = "committed"
	journal.CheckpointID = created.ID
	journal.Ref = created.Ref
	journal.CanonicalRef = created.CanonicalRef
	journal.CommitSHA = created.Commit
	return repo.writeCheckpointJournal(journal)
}

func (repo *Repo) MarkCheckpointJournalGitSync(sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase, snapshot *Snapshot) error {
	if snapshot == nil {
		return nil
	}
	journal, err := repo.loadCheckpointJournal(sessionID, turnID, phase)
	if err != nil {
		return err
	}
	journal.GitSyncRef = snapshot.Ref
	journal.GitSyncCommitSHA = snapshot.Commit.String()
	return repo.writeCheckpointJournal(journal)
}

func (repo *Repo) FinalizeCheckpointJournal(sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase, eventSeq primitives.EventSeq, eventHash primitives.EventHash) error {
	journal, ok, err := repo.ReadCheckpointJournal(sessionID, turnID, phase)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	journal.State = "finalized"
	journal.EventSeq = eventSeq
	journal.EventHash = eventHash
	if err := repo.writeCheckpointJournal(journal); err != nil {
		return err
	}
	return repo.ClearCheckpointJournal(sessionID, turnID, phase)
}

func (repo *Repo) ClearCheckpointJournal(sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase) error {
	path, err := repo.CheckpointJournalPath(sessionID, turnID, phase)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("clear checkpoint journal: %w", err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync cleared checkpoint journal: %w", err)
	}
	return nil
}

func (repo *Repo) ReadCheckpointJournal(sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase) (CheckpointJournal, bool, error) {
	path, err := repo.CheckpointJournalPath(sessionID, turnID, phase)
	if err != nil {
		return CheckpointJournal{}, false, err
	}
	journal, err := readCheckpointJournalFile(path)
	if os.IsNotExist(err) {
		return CheckpointJournal{}, false, nil
	}
	if err != nil {
		return CheckpointJournal{}, false, err
	}
	return journal, true, nil
}

func (repo *Repo) ListCheckpointJournals() ([]CheckpointJournal, error) {
	dir := repo.checkpointJournalDir()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read checkpoint journal dir: %w", err)
	}

	var journals []CheckpointJournal
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		journal, err := readCheckpointJournalFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		journals = append(journals, journal)
	}
	return journals, nil
}

func (repo *Repo) CheckpointJournalPath(sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase) (string, error) {
	sessionID, err := primitives.ParseSessionID(sessionID.String())
	if err != nil {
		return "", err
	}
	turnID, err = primitives.NewTurnID(turnID.Uint64())
	if err != nil {
		return "", err
	}
	phase, err = primitives.ParseCheckpointPhase(phase.String())
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-turn-%s-%s.json", sessionID, turnID.RefSegment(), phase)
	if repo.ScopedRefs {
		streamID, err := repo.StreamID(sessionID)
		if err != nil {
			return "", err
		}
		name = fmt.Sprintf("%s-%s-%s", repo.WorktreeID, streamID, name)
	}
	return filepath.Join(repo.checkpointJournalDir(), name), nil
}

func (repo *Repo) checkpointJournalDir() string {
	return filepath.Join(repo.TmpDir, "checkpoints")
}

func (repo *Repo) loadCheckpointJournal(sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase) (CheckpointJournal, error) {
	journal, ok, err := repo.ReadCheckpointJournal(sessionID, turnID, phase)
	if err != nil {
		return CheckpointJournal{}, err
	}
	if !ok {
		return CheckpointJournal{}, fmt.Errorf("checkpoint journal missing for session %s turn %s %s", sessionID, turnID, phase)
	}
	return journal, nil
}

func (repo *Repo) writeCheckpointJournal(journal CheckpointJournal) error {
	if err := validateCheckpointJournal(journal); err != nil {
		return err
	}
	path, err := repo.CheckpointJournalPath(journal.SessionID, journal.TurnID, journal.Phase)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create checkpoint journal dir: %w", err)
	}
	journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal checkpoint journal: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".checkpoint-journal-*")
	if err != nil {
		return fmt.Errorf("create checkpoint journal: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure checkpoint journal: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write checkpoint journal: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync checkpoint journal: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close checkpoint journal: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("commit checkpoint journal: %w", err)
	}
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("sync checkpoint journal dir: %w", err)
	}
	return nil
}

func readCheckpointJournalFile(path string) (CheckpointJournal, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return CheckpointJournal{}, err
	}
	var journal CheckpointJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return CheckpointJournal{}, fmt.Errorf("checkpoint invariant failed: unreadable checkpoint journal at %s: %w", path, err)
	}
	if err := validateCheckpointJournal(journal); err != nil {
		return CheckpointJournal{}, fmt.Errorf("checkpoint invariant failed: invalid checkpoint journal at %s: %w", path, err)
	}
	return journal, nil
}

func validateCheckpointJournal(journal CheckpointJournal) error {
	if journal.Version != 1 && journal.Version != checkpointJournalVersion {
		return fmt.Errorf("unsupported version %d", journal.Version)
	}
	switch journal.State {
	case "intent", "committed", "finalized":
	default:
		return fmt.Errorf("unknown state %q", journal.State)
	}
	if _, err := primitives.ParseSessionID(journal.SessionID.String()); err != nil {
		return err
	}
	if _, err := primitives.NewTurnID(journal.TurnID.Uint64()); err != nil {
		return err
	}
	if _, err := primitives.ParseCheckpointPhase(journal.Phase.String()); err != nil {
		return err
	}
	if journal.Adapter != "" {
		if _, err := primitives.ParseAdapterName(journal.Adapter.String()); err != nil {
			return err
		}
	}
	if journal.Ref != "" {
		if _, err := primitives.ParseCheckpointRef(journal.Ref.String()); err != nil {
			return err
		}
	}
	if journal.CanonicalRef != "" {
		if _, err := primitives.ParseCheckpointRef(journal.CanonicalRef.String()); err != nil {
			return err
		}
	}
	if journal.WorktreeID != "" {
		if _, err := primitives.ParseWorktreeID(journal.WorktreeID.String()); err != nil {
			return err
		}
	}
	if journal.StreamID != "" {
		if _, err := primitives.ParseEventStreamID(journal.StreamID.String()); err != nil {
			return err
		}
	}
	if journal.CheckpointID != "" {
		if _, err := primitives.ParseCheckpointID(journal.CheckpointID.String()); err != nil {
			return err
		}
	}
	if journal.CommitSHA != "" {
		if _, err := primitives.ParseCommitSHA(journal.CommitSHA.String()); err != nil {
			return err
		}
	}
	if journal.EventSeq != 0 {
		if _, err := primitives.NewEventSeq(journal.EventSeq.Uint64()); err != nil {
			return err
		}
	}
	if journal.EventHash != "" {
		if _, err := primitives.ParseEventHash(journal.EventHash.String()); err != nil {
			return err
		}
	}
	return nil
}
