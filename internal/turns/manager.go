package turns

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"agent-vcs-again/internal/checkpoint"
	"agent-vcs-again/internal/primitives"
)

const activeTurnStateVersion = 1

type Manager struct {
	Repo *checkpoint.Repo
}

type StartResult struct {
	TurnID primitives.TurnID
	Pre    checkpoint.Checkpoint
}

type FinishResult struct {
	TurnID primitives.TurnID
	Post   checkpoint.Checkpoint
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

func (manager Manager) Start(sessionID primitives.SessionID, requestedTurnID primitives.TurnID) (StartResult, error) {
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

	pre, err := manager.Repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		return StartResult{}, err
	}
	if err := manager.writeActive(sessionID, turnID, pre); err != nil {
		if cleanupErr := manager.Repo.DeleteCheckpointRef(pre.Ref); cleanupErr != nil {
			return StartResult{}, errors.Join(err, fmt.Errorf("cleanup pre checkpoint after active turn state failure: %w", cleanupErr))
		}
		return StartResult{}, err
	}

	return StartResult{TurnID: turnID, Pre: pre}, nil
}

func (manager Manager) Finish(sessionID primitives.SessionID, requestedTurnID primitives.TurnID) (FinishResult, error) {
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

	post, err := manager.Repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost)
	if err != nil {
		return FinishResult{}, err
	}
	if active != nil {
		if err := manager.clearActive(sessionID); err != nil {
			return FinishResult{}, err
		}
	}

	return FinishResult{TurnID: turnID, Post: post}, nil
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

func (manager Manager) validate() error {
	if manager.Repo == nil {
		return errors.New("turn manager requires checkpoint repo")
	}
	return nil
}

func (manager Manager) activeStatePath(sessionID primitives.SessionID) string {
	return filepath.Join(manager.Repo.TmpDir, "turns", sessionID.String()+".json")
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
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
