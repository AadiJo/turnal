package turnevents

import (
	"encoding/json"
	"fmt"

	"agent-vcs-again/internal/checkpoint"
	eventlog "agent-vcs-again/internal/events"
	"agent-vcs-again/internal/primitives"
	"agent-vcs-again/internal/turns"
)

type Recorder struct {
	Log     eventlog.Log
	Manager turns.Manager
	Adapter primitives.AdapterName
	RawRef  string
}

type turnPayload struct {
	Turn uint64 `json:"turn"`
}

type checkpointPayload struct {
	Turn      uint64 `json:"turn"`
	Phase     string `json:"phase"`
	CommitSHA string `json:"commit_sha"`
	Ref       string `json:"ref"`
}

func (recorder Recorder) Start(sessionID primitives.SessionID, requestedTurnID primitives.TurnID) (turns.StartResult, error) {
	started, err := recorder.Manager.Start(sessionID, requestedTurnID)
	if err != nil {
		return turns.StartResult{}, err
	}
	if err := AppendTurnStart(recorder.Log, recorder.Adapter, sessionID, started.TurnID, recorder.RawRef); err != nil {
		return turns.StartResult{}, fmt.Errorf("append turn.start event for session %s turn %s: %w", sessionID, started.TurnID, err)
	}
	if err := AppendCheckpoint(recorder.Log, recorder.Adapter, sessionID, started.TurnID, primitives.CheckpointPhasePre, started.Pre, recorder.RawRef); err != nil {
		return turns.StartResult{}, fmt.Errorf("append pre checkpoint event for session %s turn %s: %w", sessionID, started.TurnID, err)
	}
	return started, nil
}

func (recorder Recorder) Finish(sessionID primitives.SessionID, requestedTurnID primitives.TurnID) (turns.FinishResult, error) {
	finished, err := recorder.Manager.Finish(sessionID, requestedTurnID)
	if err != nil {
		return turns.FinishResult{}, err
	}
	if err := AppendTurnFinish(recorder.Log, recorder.Adapter, sessionID, finished.TurnID, recorder.RawRef); err != nil {
		return turns.FinishResult{}, fmt.Errorf("append turn.finish event for session %s turn %s: %w", sessionID, finished.TurnID, err)
	}
	if err := AppendCheckpoint(recorder.Log, recorder.Adapter, sessionID, finished.TurnID, primitives.CheckpointPhasePost, finished.Post, recorder.RawRef); err != nil {
		return turns.FinishResult{}, fmt.Errorf("append post checkpoint event for session %s turn %s: %w", sessionID, finished.TurnID, err)
	}
	return finished, nil
}

func AppendTurnStart(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID, turnID primitives.TurnID, rawRef string) error {
	return appendPayloadEvent(log, eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypeTurnStart,
		Adapter:   adapter,
		SourceID:  fmt.Sprintf("%s:turn:%s:start", adapter, turnID),
		RawRef:    rawRef,
		Payload:   mustJSON(turnPayload{Turn: turnID.Uint64()}),
	})
}

func AppendTurnFinish(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID, turnID primitives.TurnID, rawRef string) error {
	return appendPayloadEvent(log, eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypeTurnFinish,
		Adapter:   adapter,
		SourceID:  fmt.Sprintf("%s:turn:%s:finish", adapter, turnID),
		RawRef:    rawRef,
		Payload:   mustJSON(turnPayload{Turn: turnID.Uint64()}),
	})
}

func AppendCheckpoint(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase, created checkpoint.Checkpoint, rawRef string) error {
	return appendPayloadEvent(log, eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypeCheckpoint,
		Adapter:   adapter,
		SourceID:  fmt.Sprintf("%s:turn:%s:checkpoint:%s", adapter, turnID, phase),
		RawRef:    rawRef,
		Payload: mustJSON(checkpointPayload{
			Turn:      turnID.Uint64(),
			Phase:     phase.String(),
			CommitSHA: created.Commit.String(),
			Ref:       created.Ref.String(),
		}),
	})
}

func appendPayloadEvent(log eventlog.Log, input eventlog.AppendInput) error {
	if input.SourceID != "" {
		seen, err := log.ContainsSourceID(input.SessionID, input.SourceID)
		if err != nil {
			return err
		}
		if seen {
			return nil
		}
	}
	_, err := log.Append(input)
	return err
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
