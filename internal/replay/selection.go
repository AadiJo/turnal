package replay

import (
	"fmt"
	"strings"

	"agent-vcs-again/internal/checkpoint"
	"agent-vcs-again/internal/primitives"
)

type selection struct {
	Entries      []Checkpoint
	CurrentIndex int
}

func (selection selection) Current() Checkpoint {
	return selection.Entries[selection.CurrentIndex]
}

func (manager Manager) resolveSelection(value string) (selection, error) {
	if err := manager.requireRepo(); err != nil {
		return selection{}, err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return selection{}, fmt.Errorf("replay target is required")
	}

	if sessionText, rest, ok := strings.Cut(value, ":turn:"); ok {
		sessionID, err := primitives.ParseSessionID(sessionText)
		if err != nil {
			return selection{}, err
		}
		if strings.Contains(rest, "..") {
			return manager.resolveRangeSelection(sessionID, rest)
		}
		target, err := primitives.ParseTargetRef(value)
		if err != nil {
			return selection{}, err
		}
		return manager.resolveTargetSelection(target)
	}

	parts := strings.Split(value, ":")
	switch len(parts) {
	case 1:
		sessionID, err := primitives.ParseSessionID(parts[0])
		if err != nil {
			return selection{}, err
		}
		return manager.resolveSessionSelection(sessionID)
	case 2, 3:
		sessionID, err := primitives.ParseSessionID(parts[0])
		if err != nil {
			return selection{}, err
		}
		if strings.Contains(parts[1], "..") {
			if len(parts) == 3 {
				return selection{}, fmt.Errorf("range replay target must not include a phase")
			}
			return manager.resolveRangeSelection(sessionID, parts[1])
		}
		turnID, err := primitives.ParseTurnID(parts[1])
		if err != nil {
			return selection{}, err
		}
		phase := primitives.CheckpointPhase("")
		if len(parts) == 3 {
			phase, err = primitives.ParseCheckpointPhase(parts[2])
			if err != nil {
				return selection{}, err
			}
		}
		target, err := primitives.NewTargetRef(sessionID, turnID, phase)
		if err != nil {
			return selection{}, err
		}
		return manager.resolveTargetSelection(target)
	default:
		return selection{}, fmt.Errorf("replay target must be <session>, <session>:turn:<turn>[:pre|post], or <session>:turn:<start>..<end>")
	}
}

func (manager Manager) resolveSessionSelection(sessionID primitives.SessionID) (selection, error) {
	entries, err := manager.sessionEntries(sessionID)
	if err != nil {
		return selection{}, err
	}
	if len(entries) == 0 {
		return selection{}, fmt.Errorf("no checkpoints found for session %s", sessionID)
	}
	return selection{Entries: entries, CurrentIndex: 0}, nil
}

func (manager Manager) resolveRangeSelection(sessionID primitives.SessionID, rangeText string) (selection, error) {
	startText, endText, ok := strings.Cut(rangeText, "..")
	if !ok || strings.TrimSpace(startText) == "" || strings.TrimSpace(endText) == "" {
		return selection{}, fmt.Errorf("range replay target must be <start>..<end>")
	}
	startTurn, err := primitives.ParseTurnID(startText)
	if err != nil {
		return selection{}, err
	}
	endTurn, err := primitives.ParseTurnID(endText)
	if err != nil {
		return selection{}, err
	}
	if startTurn.Uint64() > endTurn.Uint64() {
		return selection{}, fmt.Errorf("range start turn must be less than or equal to end turn")
	}

	entries, err := manager.sessionEntries(sessionID)
	if err != nil {
		return selection{}, err
	}
	filtered := make([]Checkpoint, 0, len(entries))
	for _, entry := range entries {
		if entry.Turn >= startTurn.Uint64() && entry.Turn <= endTurn.Uint64() {
			filtered = append(filtered, entry)
		}
	}
	if len(filtered) == 0 {
		return selection{}, fmt.Errorf("no checkpoints found for %s turn %s..%s", sessionID, startTurn, endTurn)
	}
	return selection{Entries: filtered, CurrentIndex: 0}, nil
}

func (manager Manager) resolveTargetSelection(target primitives.TargetRef) (selection, error) {
	entries, err := manager.sessionEntries(target.SessionID())
	if err != nil {
		return selection{}, err
	}
	if len(entries) == 0 {
		return selection{}, fmt.Errorf("no checkpoints found for session %s", target.SessionID())
	}

	if phase, ok := target.Phase(); ok {
		return selectionWithTarget(entries, target.SessionID(), target.TurnID(), phase)
	}
	if selected, err := selectionWithTarget(entries, target.SessionID(), target.TurnID(), primitives.CheckpointPhasePost); err == nil {
		return selected, nil
	}
	return selectionWithTarget(entries, target.SessionID(), target.TurnID(), primitives.CheckpointPhasePre)
}

func selectionWithTarget(entries []Checkpoint, sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase) (selection, error) {
	for index, entry := range entries {
		if entry.SessionID == sessionID.String() && entry.Turn == turnID.Uint64() && entry.Phase == phase.String() {
			return selection{Entries: entries, CurrentIndex: index}, nil
		}
	}
	return selection{}, fmt.Errorf("checkpoint %s:turn:%s:%s not found", sessionID, turnID, phase)
}

func (manager Manager) sessionEntries(sessionID primitives.SessionID) ([]Checkpoint, error) {
	infos, err := manager.Repo.ListCheckpointRefInfos(sessionID)
	if err != nil {
		return nil, err
	}
	entries := make([]Checkpoint, 0, len(infos))
	for _, info := range infos {
		if !info.HasPhase {
			continue
		}
		entries = append(entries, checkpointEntry(info))
	}
	return entries, nil
}

func checkpointEntry(info checkpoint.CheckpointRefInfo) Checkpoint {
	return Checkpoint{
		Target:    fmt.Sprintf("%s:turn:%s:%s", info.SessionID, info.TurnID, info.Phase),
		Ref:       info.Ref.String(),
		Commit:    info.Commit.String(),
		SessionID: info.SessionID.String(),
		Turn:      info.TurnID.Uint64(),
		Phase:     info.Phase.String(),
	}
}
