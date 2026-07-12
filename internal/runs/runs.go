package runs

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
)

const (
	EnvRunID = "TURNAL_RUN_ID"

	CaptureWrapper  = "wrapper"
	CaptureProvider = "provider"

	StatusRunning    = "running"
	StatusSucceeded  = "succeeded"
	StatusFailed     = "failed"
	StatusIncomplete = "incomplete"
)

type startPayload struct {
	RunID      primitives.RunID      `json:"run_id"`
	RepoID     primitives.RepoID     `json:"repo_id"`
	StoreID    primitives.StoreID    `json:"store_id"`
	WorktreeID primitives.WorktreeID `json:"worktree_id"`
	Command    []string              `json:"command,omitempty"`
}

type capturePayload struct {
	RunID     primitives.RunID       `json:"run_id"`
	Kind      string                 `json:"kind"`
	SessionID primitives.SessionID   `json:"session_id"`
	Adapter   primitives.AdapterName `json:"adapter"`
}

type attemptPayload struct {
	RunID     primitives.RunID     `json:"run_id"`
	AttemptID primitives.AttemptID `json:"attempt_id"`
	SessionID primitives.SessionID `json:"session_id"`
	TurnID    primitives.TurnID    `json:"turn_id"`
}

type finishPayload struct {
	RunID  primitives.RunID `json:"run_id"`
	Status string           `json:"status"`
	Error  string           `json:"error,omitempty"`
}

type Provenance struct {
	SessionID primitives.SessionID     `json:"session_id"`
	TurnID    *primitives.TurnID       `json:"turn_id,omitempty"`
	StreamID  primitives.EventStreamID `json:"stream_id,omitempty"`
	EventSeq  primitives.EventSeq      `json:"event_seq"`
	EventType primitives.EventType     `json:"event_type"`
	Adapter   primitives.AdapterName   `json:"adapter,omitempty"`
}

type Capture struct {
	Kind       string                 `json:"kind"`
	SessionID  primitives.SessionID   `json:"session_id"`
	Adapter    primitives.AdapterName `json:"adapter"`
	Provenance Provenance             `json:"provenance"`
}

type Attempt struct {
	ID         primitives.AttemptID `json:"attempt_id"`
	SessionID  primitives.SessionID `json:"session_id"`
	TurnID     primitives.TurnID    `json:"turn_id"`
	Provenance Provenance           `json:"provenance"`
	Fields     []Provenance         `json:"field_provenance,omitempty"`
}

type Projection struct {
	ID         primitives.RunID      `json:"run_id"`
	RepoID     primitives.RepoID     `json:"repo_id"`
	StoreID    primitives.StoreID    `json:"store_id"`
	WorktreeID primitives.WorktreeID `json:"worktree_id"`
	Command    []string              `json:"command,omitempty"`
	Status     string                `json:"status"`
	Error      string                `json:"error,omitempty"`
	Shape      string                `json:"shape"`
	Captures   []Capture             `json:"captures"`
	Attempts   []Attempt             `json:"attempts"`
	Start      Provenance            `json:"start"`
	Finish     *Provenance           `json:"finish,omitempty"`
}

func Start(repo *checkpoint.Repo, runID primitives.RunID, wrapperSession primitives.SessionID, command []string) error {
	if err := validateRepoAndRun(repo, runID); err != nil {
		return err
	}
	err := appendOnce(repo.EventLog(), eventlog.AppendInput{
		SessionID: wrapperSession, Type: primitives.EventTypeRunStart,
		Adapter: primitives.AdapterCodex, SourceID: "run:" + runID.String() + ":start",
		Payload: mustJSON(startPayload{RunID: runID, RepoID: repo.RepoID, StoreID: repo.StoreID, WorktreeID: repo.WorktreeID, Command: command}),
	})
	return err
}

func LinkCapture(repo *checkpoint.Repo, runID primitives.RunID, kind string, sessionID primitives.SessionID, adapter primitives.AdapterName) error {
	projection, err := AcceptsCapture(repo, runID)
	if err != nil {
		return err
	}
	if kind != CaptureWrapper && kind != CaptureProvider {
		return fmt.Errorf("invalid run capture kind %q", kind)
	}
	err = appendOnce(repo.EventLog(), eventlog.AppendInput{
		SessionID: sessionID, Type: primitives.EventTypeRunCaptureLink, Adapter: adapter,
		SourceID: fmt.Sprintf("run:%s:capture:%s:%s", runID, kind, sessionID),
		Payload:  mustJSON(capturePayload{RunID: projection.ID, Kind: kind, SessionID: sessionID, Adapter: adapter}),
	})
	return err
}

func EnsureAttempt(repo *checkpoint.Repo, runID primitives.RunID, sessionID primitives.SessionID, turnID primitives.TurnID, adapter primitives.AdapterName) (primitives.AttemptID, error) {
	projection, err := AcceptsCapture(repo, runID)
	if err != nil {
		return "", err
	}
	for _, attempt := range projection.Attempts {
		if attempt.SessionID == sessionID && attempt.TurnID == turnID {
			return attempt.ID, nil
		}
	}
	attemptID, err := primitives.NewAttemptID()
	if err != nil {
		return "", err
	}
	err = appendOnce(repo.EventLog(), eventlog.AppendInput{
		SessionID: sessionID, TurnID: &turnID, Type: primitives.EventTypeRunAttemptLink, Adapter: adapter,
		SourceID: fmt.Sprintf("run:%s:attempt:%s:%s", runID, sessionID, turnID),
		Payload:  mustJSON(attemptPayload{RunID: runID, AttemptID: attemptID, SessionID: sessionID, TurnID: turnID}),
	})
	return attemptID, err
}

func Finish(repo *checkpoint.Repo, runID primitives.RunID, wrapperSession primitives.SessionID, status, message string) error {
	if status != StatusSucceeded && status != StatusFailed && status != StatusIncomplete {
		return fmt.Errorf("invalid run status %q", status)
	}
	if _, err := AcceptsCapture(repo, runID); err != nil {
		return err
	}
	err := appendOnce(repo.EventLog(), eventlog.AppendInput{
		SessionID: wrapperSession, Type: primitives.EventTypeRunFinish, Adapter: primitives.AdapterCodex,
		SourceID: "run:" + runID.String() + ":finish",
		Payload:  mustJSON(finishPayload{RunID: runID, Status: status, Error: message}),
	})
	return err
}

func AcceptsCapture(repo *checkpoint.Repo, runID primitives.RunID) (Projection, error) {
	projection, err := Read(repo, runID)
	if err != nil {
		return Projection{}, err
	}
	if projection.RepoID != repo.RepoID || projection.StoreID != repo.StoreID || projection.WorktreeID != repo.WorktreeID {
		return Projection{}, fmt.Errorf("run %s belongs to a different repository store or worktree", runID)
	}
	if projection.Status != StatusRunning {
		return Projection{}, fmt.Errorf("run %s cannot accept capture in status %s", runID, projection.Status)
	}
	return projection, nil
}

func Read(repo *checkpoint.Repo, runID primitives.RunID) (Projection, error) {
	if err := validateRepoAndRun(repo, runID); err != nil {
		return Projection{}, err
	}
	streams, err := eventlog.ListDurableStreams(repo.MetadataDir)
	if err != nil {
		return Projection{}, err
	}
	var result Projection
	seenCaptures := map[string]bool{}
	seenAttempts := map[string]bool{}
	for _, stream := range streams {
		for _, event := range stream.Events {
			switch event.Type {
			case primitives.EventTypeRunStart:
				var payload startPayload
				if json.Unmarshal(event.Payload, &payload) != nil || payload.RunID != runID {
					continue
				}
				if result.ID != "" {
					return Projection{}, fmt.Errorf("run %s has duplicate start records", runID)
				}
				if _, err := primitives.ParseRunID(payload.RunID.String()); err != nil {
					return Projection{}, err
				}
				if _, err := primitives.ParseRepoID(payload.RepoID.String()); err != nil {
					return Projection{}, err
				}
				if _, err := primitives.ParseStoreID(payload.StoreID.String()); err != nil {
					return Projection{}, err
				}
				if _, err := primitives.ParseWorktreeID(payload.WorktreeID.String()); err != nil {
					return Projection{}, err
				}
				result.ID, result.RepoID, result.StoreID, result.WorktreeID = payload.RunID, payload.RepoID, payload.StoreID, payload.WorktreeID
				result.Command, result.Status, result.Start = payload.Command, StatusRunning, provenance(event)
			case primitives.EventTypeRunCaptureLink:
				var payload capturePayload
				if json.Unmarshal(event.Payload, &payload) != nil || payload.RunID != runID {
					continue
				}
				key := payload.Kind + "\x00" + payload.SessionID.String()
				if !seenCaptures[key] {
					result.Captures = append(result.Captures, Capture{Kind: payload.Kind, SessionID: payload.SessionID, Adapter: payload.Adapter, Provenance: provenance(event)})
					seenCaptures[key] = true
				}
			case primitives.EventTypeRunAttemptLink:
				var payload attemptPayload
				if json.Unmarshal(event.Payload, &payload) != nil || payload.RunID != runID {
					continue
				}
				key := payload.SessionID.String() + "\x00" + payload.TurnID.String()
				if !seenAttempts[key] {
					result.Attempts = append(result.Attempts, Attempt{ID: payload.AttemptID, SessionID: payload.SessionID, TurnID: payload.TurnID, Provenance: provenance(event)})
					seenAttempts[key] = true
				}
			case primitives.EventTypeRunFinish:
				var payload finishPayload
				if json.Unmarshal(event.Payload, &payload) != nil || payload.RunID != runID {
					continue
				}
				p := provenance(event)
				result.Status, result.Error, result.Finish = payload.Status, payload.Error, &p
			}
		}
	}
	if result.ID == "" {
		return Projection{}, fmt.Errorf("run %s does not exist in this Turnal store", runID)
	}
	// Attach observable provider fields to their attempt while retaining their exact source.
	for index := range result.Attempts {
		attempt := &result.Attempts[index]
		for _, stream := range streams {
			if stream.SessionID != attempt.SessionID {
				continue
			}
			for _, event := range stream.Events {
				if event.TurnID == nil || *event.TurnID != attempt.TurnID {
					continue
				}
				switch event.Type {
				case primitives.EventTypeCheckpoint, primitives.EventTypePromptUser, primitives.EventTypeAssistantMessage, primitives.EventTypeToolCall, primitives.EventTypeToolResult:
					attempt.Fields = append(attempt.Fields, provenance(event))
				}
			}
		}
	}
	sort.Slice(result.Captures, func(i, j int) bool {
		if result.Captures[i].Kind != result.Captures[j].Kind {
			return result.Captures[i].Kind < result.Captures[j].Kind
		}
		return result.Captures[i].SessionID < result.Captures[j].SessionID
	})
	sort.Slice(result.Attempts, func(i, j int) bool {
		if result.Attempts[i].SessionID != result.Attempts[j].SessionID {
			return result.Attempts[i].SessionID < result.Attempts[j].SessionID
		}
		return result.Attempts[i].TurnID < result.Attempts[j].TurnID
	})
	result.Shape = shape(result)
	return result, nil
}

func shape(run Projection) string {
	wrapper, provider := false, false
	for _, capture := range run.Captures {
		wrapper = wrapper || capture.Kind == CaptureWrapper
		provider = provider || capture.Kind == CaptureProvider
	}
	if wrapper && !provider {
		return "wrapper-only"
	}
	if provider && !wrapper {
		return "hook-only"
	}
	if !wrapper && !provider {
		return "incomplete"
	}
	if len(run.Attempts) == 1 {
		return "single-attempt"
	}
	if len(run.Attempts) > 1 {
		return "multi-attempt"
	}
	return "incomplete"
}

func provenance(event eventlog.Event) Provenance {
	return Provenance{SessionID: event.SessionID, TurnID: event.TurnID, StreamID: event.StreamID, EventSeq: event.Seq, EventType: event.Type, Adapter: event.Adapter}
}
func validateRepoAndRun(repo *checkpoint.Repo, runID primitives.RunID) error {
	if repo == nil {
		return fmt.Errorf("run operation requires checkpoint repo")
	}
	_, err := primitives.ParseRunID(runID.String())
	return err
}
func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func appendOnce(log eventlog.Log, input eventlog.AppendInput) error {
	if input.SourceID != "" {
		seen, err := log.ContainsSourceID(input.SessionID, input.SourceID)
		if err != nil || seen {
			return err
		}
	}
	_, err := log.Append(input)
	return err
}
