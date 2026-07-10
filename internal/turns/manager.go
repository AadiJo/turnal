package turns

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/config"
	"github.com/AadiJo/turnal/internal/gitsync"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/workspacegit"
)

const activeTurnStateVersion = 1

type Manager struct {
	Repo              *checkpoint.Repo
	GitSyncEnabled    *bool
	CheckpointAdapter primitives.AdapterName
	CheckpointRawRef  string
}

type StartResult struct {
	TurnID  primitives.TurnID
	Pre     checkpoint.Checkpoint
	GitSync *checkpoint.Snapshot
}

type FinishResult struct {
	TurnID  primitives.TurnID
	Post    checkpoint.Checkpoint
	GitSync *checkpoint.Snapshot
}

type ActiveTurn struct {
	TurnID    primitives.TurnID
	PreRef    primitives.CheckpointRef
	PreCommit primitives.CommitSHA
}

type activeTurnState struct {
	Version   int    `json:"version"`
	SessionID string `json:"session_id"`
	TurnID    uint64 `json:"turn_id"`
	PreRef    string `json:"pre_ref"`
	PreCommit string `json:"pre_commit"`
	StartedAt string `json:"started_at"`
}

type parsedActiveTurnState struct {
	TurnID    primitives.TurnID
	PreRef    primitives.CheckpointRef
	PreCommit primitives.CommitSHA
}

func NewManager(repo *checkpoint.Repo) Manager {
	return Manager{Repo: repo}
}

func (manager Manager) WithCheckpointEvents(adapter primitives.AdapterName, rawRef string) Manager {
	manager.CheckpointAdapter = adapter
	manager.CheckpointRawRef = rawRef
	return manager
}

func (manager Manager) Start(sessionID primitives.SessionID, requestedTurnID primitives.TurnID) (StartResult, error) {
	if err := manager.validate(); err != nil {
		return StartResult{}, err
	}
	var result StartResult
	err := manager.Repo.WithWorkspaceLock("start turn", func() error {
		var err error
		result, err = manager.startUnlocked(sessionID, requestedTurnID)
		return err
	})
	return result, err
}

func (manager Manager) startUnlocked(sessionID primitives.SessionID, requestedTurnID primitives.TurnID) (StartResult, error) {
	if err := manager.validate(); err != nil {
		return StartResult{}, err
	}

	active, err := manager.readActive(sessionID)
	if err != nil {
		return StartResult{}, err
	}
	if active != nil {
		return StartResult{}, fmt.Errorf("active turn already exists for session %s: turn %s", sessionID, active.TurnID)
	}

	turnID := requestedTurnID
	if turnID == 0 {
		turnID, err = manager.NextTurnID(sessionID)
		if err != nil {
			return StartResult{}, err
		}
	} else if _, err := primitives.NewTurnID(turnID.Uint64()); err != nil {
		return StartResult{}, err
	}

	refs, err := manager.Repo.ListCheckpointRefs(sessionID)
	if err != nil {
		return StartResult{}, err
	}
	if len(refsForTurn(refs, turnID)) > 0 {
		return StartResult{}, fmt.Errorf("turn %s already has checkpoint refs for session %s", turnID, sessionID)
	}

	journaled := manager.checkpointJournalingEnabled()
	if journaled {
		if err := manager.Repo.BeginCheckpointJournal(sessionID, turnID, primitives.CheckpointPhasePre, manager.CheckpointAdapter, manager.CheckpointRawRef); err != nil {
			return StartResult{}, err
		}
	}

	pre, err := manager.Repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		if journaled {
			_ = manager.Repo.ClearCheckpointJournal(sessionID, turnID, primitives.CheckpointPhasePre)
		}
		return StartResult{}, err
	}
	if journaled {
		if err := manager.Repo.MarkCheckpointJournalCommitted(sessionID, turnID, primitives.CheckpointPhasePre, pre); err != nil {
			if cleanupErr := manager.Repo.DeleteCheckpointRef(pre.Ref); cleanupErr != nil {
				return StartResult{}, errors.Join(err, fmt.Errorf("cleanup pre checkpoint after checkpoint journal failure: %w", cleanupErr))
			}
			return StartResult{}, err
		}
	}
	gitSync, err := manager.captureGitSyncIfEnabled(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		if journaled {
			_ = manager.Repo.ClearCheckpointJournal(sessionID, turnID, primitives.CheckpointPhasePre)
		}
		if cleanupErr := manager.Repo.DeleteCheckpointRef(pre.Ref); cleanupErr != nil {
			return StartResult{}, errors.Join(err, fmt.Errorf("cleanup pre checkpoint after git-sync capture failure: %w", cleanupErr))
		}
		return StartResult{}, err
	}
	if journaled {
		if err := manager.Repo.MarkCheckpointJournalGitSync(sessionID, turnID, primitives.CheckpointPhasePre, gitSync); err != nil {
			if cleanupErr := manager.Repo.DeleteCheckpointRef(pre.Ref); cleanupErr != nil {
				return StartResult{}, errors.Join(err, fmt.Errorf("cleanup pre checkpoint after checkpoint git-sync journal failure: %w", cleanupErr))
			}
			return StartResult{}, err
		}
	}
	if err := manager.writeActive(sessionID, turnID, pre); err != nil {
		if journaled {
			_ = manager.Repo.ClearCheckpointJournal(sessionID, turnID, primitives.CheckpointPhasePre)
		}
		if cleanupErr := manager.Repo.DeleteCheckpointRef(pre.Ref); cleanupErr != nil {
			return StartResult{}, errors.Join(err, fmt.Errorf("cleanup pre checkpoint after active turn state failure: %w", cleanupErr))
		}
		return StartResult{}, err
	}

	return StartResult{TurnID: turnID, Pre: pre, GitSync: gitSync}, nil
}

func (manager Manager) Finish(sessionID primitives.SessionID, requestedTurnID primitives.TurnID) (FinishResult, error) {
	if err := manager.validate(); err != nil {
		return FinishResult{}, err
	}
	var result FinishResult
	err := manager.Repo.WithWorkspaceLock("finish turn", func() error {
		var err error
		result, err = manager.finishUnlocked(sessionID, requestedTurnID)
		return err
	})
	return result, err
}

func (manager Manager) finishUnlocked(sessionID primitives.SessionID, requestedTurnID primitives.TurnID) (FinishResult, error) {
	if err := manager.validate(); err != nil {
		return FinishResult{}, err
	}

	active, err := manager.readActive(sessionID)
	if err != nil {
		return FinishResult{}, err
	}

	turnID := requestedTurnID
	switch {
	case turnID == 0 && active == nil:
		return FinishResult{}, fmt.Errorf("no active turn for session %s", sessionID)
	case turnID == 0:
		turnID = active.TurnID
	case active != nil && active.TurnID != turnID:
		return FinishResult{}, fmt.Errorf("active turn mismatch for session %s: active turn %s, requested turn %s", sessionID, active.TurnID, turnID)
	default:
		if _, err := primitives.NewTurnID(turnID.Uint64()); err != nil {
			return FinishResult{}, err
		}
	}

	refs, err := manager.Repo.ListCheckpointRefs(sessionID)
	if err != nil {
		return FinishResult{}, err
	}
	turnRefs := refsForTurn(refs, turnID)
	if !hasPhase(turnRefs, primitives.CheckpointPhasePre) {
		return FinishResult{}, fmt.Errorf("pre checkpoint missing for session %s turn %s", sessionID, turnID)
	}
	if hasPhase(turnRefs, primitives.CheckpointPhasePost) {
		return FinishResult{}, fmt.Errorf("post checkpoint already exists for session %s turn %s", sessionID, turnID)
	}

	journaled := manager.checkpointJournalingEnabled()
	if journaled {
		if err := manager.Repo.BeginCheckpointJournal(sessionID, turnID, primitives.CheckpointPhasePost, manager.CheckpointAdapter, manager.CheckpointRawRef); err != nil {
			return FinishResult{}, err
		}
	}

	post, err := manager.Repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost)
	if err != nil {
		if journaled {
			_ = manager.Repo.ClearCheckpointJournal(sessionID, turnID, primitives.CheckpointPhasePost)
		}
		return FinishResult{}, err
	}
	if journaled {
		if err := manager.Repo.MarkCheckpointJournalCommitted(sessionID, turnID, primitives.CheckpointPhasePost, post); err != nil {
			if cleanupErr := manager.Repo.DeleteCheckpointRef(post.Ref); cleanupErr != nil {
				return FinishResult{}, errors.Join(err, fmt.Errorf("cleanup post checkpoint after checkpoint journal failure: %w", cleanupErr))
			}
			return FinishResult{}, err
		}
	}
	gitSync, err := manager.captureGitSyncIfEnabled(sessionID, turnID, primitives.CheckpointPhasePost)
	if err != nil {
		if journaled {
			_ = manager.Repo.ClearCheckpointJournal(sessionID, turnID, primitives.CheckpointPhasePost)
		}
		if cleanupErr := manager.Repo.DeleteCheckpointRef(post.Ref); cleanupErr != nil {
			return FinishResult{}, errors.Join(err, fmt.Errorf("cleanup post checkpoint after git-sync capture failure: %w", cleanupErr))
		}
		return FinishResult{}, err
	}
	if journaled {
		if err := manager.Repo.MarkCheckpointJournalGitSync(sessionID, turnID, primitives.CheckpointPhasePost, gitSync); err != nil {
			if cleanupErr := manager.Repo.DeleteCheckpointRef(post.Ref); cleanupErr != nil {
				return FinishResult{}, errors.Join(err, fmt.Errorf("cleanup post checkpoint after checkpoint git-sync journal failure: %w", cleanupErr))
			}
			return FinishResult{}, err
		}
	}
	if active != nil {
		if err := manager.clearActive(sessionID); err != nil {
			return FinishResult{}, err
		}
	}

	return FinishResult{TurnID: turnID, Post: post, GitSync: gitSync}, nil
}

func (manager Manager) NextTurnID(sessionID primitives.SessionID) (primitives.TurnID, error) {
	if err := manager.validate(); err != nil {
		return 0, err
	}

	refs, err := manager.Repo.ListCheckpointRefs(sessionID)
	if err != nil {
		return 0, err
	}

	var maxTurn uint64
	for _, ref := range refs {
		parts, err := ref.Parts()
		if err != nil {
			return 0, err
		}
		if parts.TurnID.Uint64() > maxTurn {
			maxTurn = parts.TurnID.Uint64()
		}
	}
	if maxTurn == math.MaxUint64 {
		return 0, fmt.Errorf("next turn id overflow for session %s", sessionID)
	}
	return primitives.NewTurnID(maxTurn + 1)
}

func (manager Manager) Active(sessionID primitives.SessionID) (ActiveTurn, bool, error) {
	if err := manager.validate(); err != nil {
		return ActiveTurn{}, false, err
	}
	active, err := manager.readActive(sessionID)
	if err != nil {
		return ActiveTurn{}, false, err
	}
	if active == nil {
		return ActiveTurn{}, false, nil
	}
	return ActiveTurn{
		TurnID:    active.TurnID,
		PreRef:    active.PreRef,
		PreCommit: active.PreCommit,
	}, true, nil
}

func (manager Manager) validate() error {
	if manager.Repo == nil {
		return errors.New("turn manager requires checkpoint repo")
	}
	return nil
}

func (manager Manager) checkpointJournalingEnabled() bool {
	return manager.CheckpointAdapter != ""
}

func (manager Manager) gitSyncEnabled() (bool, error) {
	if manager.GitSyncEnabled != nil {
		return *manager.GitSyncEnabled, nil
	}
	effective, _, err := config.ResolvePath(filepath.Join(manager.Repo.MetadataDir, "config.toml"), config.Overrides{})
	if err != nil {
		return false, err
	}
	return effective.GitSync.Enabled, nil
}

func (manager Manager) captureGitSyncIfEnabled(sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase) (*checkpoint.Snapshot, error) {
	enabled, err := manager.gitSyncEnabled()
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, nil
	}
	ref, err := manager.Repo.GitSyncRefFor(sessionID, turnID, phase)
	if err != nil {
		return nil, err
	}
	capture, err := workspacegit.Open(manager.Repo.WorkspaceRoot).Capture()
	if err != nil {
		return nil, fmt.Errorf("capture workspace git state for %s: %w", ref, err)
	}
	snapshot, err := gitsync.SavePrivate(manager.Repo, ref, capture, fmt.Sprintf("turnal git-sync %s turn %s %s", sessionID, turnID, phase))
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (manager Manager) activeStatePath(sessionID primitives.SessionID) string {
	name := sessionID.String() + ".json"
	if manager.Repo.ScopedRefs {
		streamID, err := manager.Repo.StreamID(sessionID)
		if err == nil {
			name = manager.Repo.WorktreeID.String() + "-" + streamID.String() + "-" + name
		}
	}
	return filepath.Join(manager.Repo.TmpDir, "turns", name)
}

func (manager Manager) ClearActiveForRecovery(sessionID primitives.SessionID) error {
	return manager.clearActive(sessionID)
}

func (manager Manager) readActive(sessionID primitives.SessionID) (*parsedActiveTurnState, error) {
	path := manager.activeStatePath(sessionID)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read active turn state: %w", err)
	}

	var state activeTurnState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("active turn state invariant failed: malformed JSON in %s: %w", path, err)
	}
	if state.Version != activeTurnStateVersion {
		return nil, fmt.Errorf("active turn state invariant failed: unsupported version %d in %s", state.Version, path)
	}

	stateSessionID, err := primitives.ParseSessionID(state.SessionID)
	if err != nil {
		return nil, fmt.Errorf("active turn state invariant failed: %w", err)
	}
	if stateSessionID != sessionID {
		return nil, fmt.Errorf("active turn state invariant failed: state session %s does not match requested session %s", stateSessionID, sessionID)
	}

	turnID, err := primitives.NewTurnID(state.TurnID)
	if err != nil {
		return nil, fmt.Errorf("active turn state invariant failed: %w", err)
	}
	preRef, err := primitives.ParseCheckpointRef(state.PreRef)
	if err != nil {
		return nil, fmt.Errorf("active turn state invariant failed: %w", err)
	}
	preRefParts, err := preRef.Parts()
	if err != nil {
		return nil, fmt.Errorf("active turn state invariant failed: %w", err)
	}
	if preRefParts.SessionID != sessionID || preRefParts.TurnID != turnID || preRefParts.Phase != primitives.CheckpointPhasePre {
		return nil, fmt.Errorf("active turn state invariant failed: pre ref %s does not match session %s turn %s pre", preRef, sessionID, turnID)
	}

	preCommit, err := primitives.ParseCommitSHA(state.PreCommit)
	if err != nil {
		return nil, fmt.Errorf("active turn state invariant failed: %w", err)
	}
	if _, err := time.Parse(time.RFC3339Nano, state.StartedAt); err != nil {
		return nil, fmt.Errorf("active turn state invariant failed: invalid started_at: %w", err)
	}

	return &parsedActiveTurnState{
		TurnID:    turnID,
		PreRef:    preRef,
		PreCommit: preCommit,
	}, nil
}

func (manager Manager) writeActive(sessionID primitives.SessionID, turnID primitives.TurnID, pre checkpoint.Checkpoint) error {
	dir := filepath.Dir(manager.activeStatePath(sessionID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create active turn state dir: %w", err)
	}

	state := activeTurnState{
		Version:   activeTurnStateVersion,
		SessionID: sessionID.String(),
		TurnID:    turnID.Uint64(),
		PreRef:    pre.Ref.String(),
		PreCommit: pre.Commit.String(),
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode active turn state: %w", err)
	}
	data = append(data, '\n')

	path := manager.activeStatePath(sessionID)
	tempFile, err := os.CreateTemp(dir, sessionID.String()+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary active turn state: %w", err)
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)

	if _, err := tempFile.Write(data); err != nil {
		_ = tempFile.Close()
		return fmt.Errorf("write temporary active turn state: %w", err)
	}
	if err := tempFile.Sync(); err != nil {
		_ = tempFile.Close()
		return fmt.Errorf("sync temporary active turn state: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("close temporary active turn state: %w", err)
	}

	if err := os.Link(tempPath, path); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("active turn already exists for session %s", sessionID)
		}
		return fmt.Errorf("create active turn state: %w", err)
	}
	return nil
}

func (manager Manager) clearActive(sessionID primitives.SessionID) error {
	if err := os.Remove(manager.activeStatePath(sessionID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear active turn state: %w", err)
	}
	return nil
}

func refsForTurn(refs []primitives.CheckpointRef, turnID primitives.TurnID) []primitives.CheckpointRef {
	var matches []primitives.CheckpointRef
	for _, ref := range refs {
		parts, err := ref.Parts()
		if err != nil {
			continue
		}
		if parts.TurnID == turnID {
			matches = append(matches, ref)
		}
	}
	return matches
}

func hasPhase(refs []primitives.CheckpointRef, phase primitives.CheckpointPhase) bool {
	for _, ref := range refs {
		parts, err := ref.Parts()
		if err != nil {
			continue
		}
		if parts.HasPhase && parts.Phase == phase {
			return true
		}
	}
	return false
}
