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
	Field     string                   `json:"field"`
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
	Fields     []Provenance           `json:"field_provenance,omitempty"`
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

type Inventory struct {
	Runs             []Projection `json:"runs"`
	UnlinkedCaptures []Capture    `json:"unlinked_captures"`
}

// Inspect derives all run relationships from durable events and reports every
// remaining session stream explicitly as an unlinked capture. It never guesses
// relationships for legacy history.
func Inspect(repo *checkpoint.Repo) (Inventory, error) {
	if repo == nil {
		return Inventory{}, fmt.Errorf("run inspection requires checkpoint repo")
	}
	streams, err := eventlog.ListDurableStreams(repo.MetadataDir)
	if err != nil {
		return Inventory{}, err
	}
	runIDs := map[primitives.RunID]bool{}
	for _, stream := range streams {
		for _, event := range stream.Events {
			if event.Type != primitives.EventTypeRunStart {
				continue
			}
			var payload startPayload
			if json.Unmarshal(event.Payload, &payload) == nil && payload.RunID != "" {
				runIDs[payload.RunID] = true
			}
		}
	}
	var inventory Inventory
	linked := map[string]bool{}
	for runID := range runIDs {
		projection, err := Read(repo, runID)
		if err != nil {
			return Inventory{}, err
		}
		inventory.Runs = append(inventory.Runs, projection)
		for _, capture := range projection.Captures {
			linked[capture.Provenance.StreamID.String()] = true
		}
	}
	for _, stream := range streams {
		if linked[stream.StreamID.String()] || len(stream.Events) == 0 {
			continue
		}
		first := stream.Events[0]
		adapter := first.Adapter
		for _, event := range stream.Events {
			if adapter == "" && event.Adapter != "" {
				adapter = event.Adapter
			}
		}
		capture := Capture{Kind: "unlinked", SessionID: stream.SessionID, Adapter: adapter, Provenance: provenance(first)}
		for _, event := range stream.Events {
			if field := fieldForEvent(event); field != "" {
				source := provenance(event)
				source.Field = field
				capture.Fields = append(capture.Fields, source)
			}
		}
		inventory.UnlinkedCaptures = append(inventory.UnlinkedCaptures, capture)
	}
	sort.Slice(inventory.Runs, func(i, j int) bool { return inventory.Runs[i].ID < inventory.Runs[j].ID })
	sort.Slice(inventory.UnlinkedCaptures, func(i, j int) bool {
		if inventory.UnlinkedCaptures[i].SessionID != inventory.UnlinkedCaptures[j].SessionID {
			return inventory.UnlinkedCaptures[i].SessionID < inventory.UnlinkedCaptures[j].SessionID
		}
		return inventory.UnlinkedCaptures[i].Provenance.StreamID < inventory.UnlinkedCaptures[j].Provenance.StreamID
	})
	return inventory, nil
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
	projection, err := AcceptsCapture(repo, runID)
	if err != nil {
		return err
	}
	if err := finish(repo, projection, wrapperSession, status, message); err != nil {
		return err
	}
	return clearLifecycleJournal(repo, runID)
}

func finish(repo *checkpoint.Repo, projection Projection, wrapperSession primitives.SessionID, status, message string) error {
	return appendOnce(repo.EventLog(), eventlog.AppendInput{
		SessionID: wrapperSession, Type: primitives.EventTypeRunFinish, Adapter: primitives.AdapterCodex,
		SourceID: "run:" + projection.ID.String() + ":finish",
		Payload:  mustJSON(finishPayload{RunID: projection.ID, Status: status, Error: message}),
	})
}

func AcceptsCapture(repo *checkpoint.Repo, runID primitives.RunID) (Projection, error) {
	// A hook may outlive a crashed wrapper process. Recover unlocked lifecycle
	// journals before treating a durable running status as authorization.
	if err := RecoverAbandoned(repo); err != nil {
		return Projection{}, err
	}
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
	var claimed []eventlog.Event
	for _, stream := range streams {
		for _, event := range stream.Events {
			if claimsRun(event, runID) {
				claimed = append(claimed, event)
			}
		}
	}
	var result Projection
	for _, event := range claimed {
		if event.Type != primitives.EventTypeRunStart {
			continue
		}
		if result.ID != "" {
			return Projection{}, fmt.Errorf("run %s has duplicate start records", runID)
		}
		var payload startPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return Projection{}, relationshipError(event, err)
		}
		if err := validateStartEvent(event, payload); err != nil {
			return Projection{}, err
		}
		result.ID, result.RepoID, result.StoreID, result.WorktreeID = payload.RunID, payload.RepoID, payload.StoreID, payload.WorktreeID
		result.Command, result.Status, result.Start = payload.Command, StatusRunning, provenance(event)
	}
	if result.ID == "" {
		return Projection{}, fmt.Errorf("run %s does not exist in this Turnal store", runID)
	}
	seenCaptures := map[string]bool{}
	seenAttempts := map[string]bool{}
	providerCaptures := map[string]Capture{}
	for _, event := range claimed {
		if event.Type == primitives.EventTypeRunStart {
			continue
		}
		if err := validateRelationshipScope(event, result); err != nil {
			return Projection{}, err
		}
		switch event.Type {
		case primitives.EventTypeRunCaptureLink:
			var payload capturePayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return Projection{}, relationshipError(event, err)
			}
			if err := validateCaptureEvent(event, result, payload); err != nil {
				return Projection{}, err
			}
			key := payload.Kind + "\x00" + payload.SessionID.String() + "\x00" + event.StreamID.String()
			if seenCaptures[key] {
				return Projection{}, relationshipError(event, fmt.Errorf("duplicate capture relationship"))
			}
			capture := Capture{Kind: payload.Kind, SessionID: payload.SessionID, Adapter: payload.Adapter, Provenance: provenance(event)}
			result.Captures = append(result.Captures, capture)
			seenCaptures[key] = true
			if payload.Kind == CaptureProvider {
				providerCaptures[payload.SessionID.String()+"\x00"+event.StreamID.String()] = capture
			}
		case primitives.EventTypeRunFinish:
			var payload finishPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return Projection{}, relationshipError(event, err)
			}
			if err := validateFinishEvent(event, result, payload); err != nil {
				return Projection{}, err
			}
			if result.Finish != nil {
				return Projection{}, relationshipError(event, fmt.Errorf("duplicate run finish"))
			}
			p := provenance(event)
			result.Status, result.Error, result.Finish = payload.Status, payload.Error, &p
		}
	}
	for _, event := range claimed {
		if event.Type != primitives.EventTypeRunAttemptLink {
			continue
		}
		if err := validateRelationshipScope(event, result); err != nil {
			return Projection{}, err
		}
		var payload attemptPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return Projection{}, relationshipError(event, err)
		}
		capture, ok := providerCaptures[payload.SessionID.String()+"\x00"+event.StreamID.String()]
		if err := validateAttemptEvent(event, result, payload, capture, ok); err != nil {
			return Projection{}, err
		}
		key := payload.SessionID.String() + "\x00" + payload.TurnID.String()
		if seenAttempts[key] {
			return Projection{}, relationshipError(event, fmt.Errorf("duplicate attempt relationship"))
		}
		result.Attempts = append(result.Attempts, Attempt{ID: payload.AttemptID, SessionID: payload.SessionID, TurnID: payload.TurnID, Provenance: provenance(event)})
		seenAttempts[key] = true
	}
	for index := range result.Captures {
		capture := &result.Captures[index]
		for _, stream := range streams {
			if stream.SessionID != capture.SessionID || stream.StreamID != capture.Provenance.StreamID {
				continue
			}
			for _, event := range stream.Events {
				if field := fieldForEvent(event); field != "" {
					source := provenance(event)
					source.Field = field
					capture.Fields = append(capture.Fields, source)
				}
			}
		}
	}
	// Attach observable provider fields to their attempt while retaining their exact source.
	for index := range result.Attempts {
		attempt := &result.Attempts[index]
		for _, stream := range streams {
			if stream.SessionID != attempt.SessionID || stream.StreamID != attempt.Provenance.StreamID {
				continue
			}
			for _, event := range stream.Events {
				if event.TurnID == nil || *event.TurnID != attempt.TurnID {
					continue
				}
				if field := fieldForEvent(event); field != "" && field != "transcript" {
					source := provenance(event)
					source.Field = field
					attempt.Fields = append(attempt.Fields, source)
				}
			}
		}
		for _, capture := range result.Captures {
			if capture.Kind != CaptureProvider || capture.SessionID != attempt.SessionID {
				continue
			}
			for _, source := range capture.Fields {
				if source.Field == "transcript" {
					attempt.Fields = append(attempt.Fields, source)
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

func claimsRun(event eventlog.Event, runID primitives.RunID) bool {
	switch event.Type {
	case primitives.EventTypeRunStart, primitives.EventTypeRunCaptureLink, primitives.EventTypeRunAttemptLink, primitives.EventTypeRunFinish:
	default:
		return false
	}
	var identity struct {
		RunID string `json:"run_id"`
	}
	return json.Unmarshal(event.Payload, &identity) == nil && identity.RunID == runID.String()
}

func validateStartEvent(event eventlog.Event, payload startPayload) error {
	if _, err := primitives.ParseRunID(payload.RunID.String()); err != nil {
		return relationshipError(event, err)
	}
	if _, err := primitives.ParseRepoID(payload.RepoID.String()); err != nil {
		return relationshipError(event, err)
	}
	if _, err := primitives.ParseStoreID(payload.StoreID.String()); err != nil {
		return relationshipError(event, err)
	}
	if _, err := primitives.ParseWorktreeID(payload.WorktreeID.String()); err != nil {
		return relationshipError(event, err)
	}
	if event.TurnID != nil {
		return relationshipError(event, fmt.Errorf("run start must not have a turn id"))
	}
	if event.RepoID != payload.RepoID || event.WorktreeID != payload.WorktreeID {
		return relationshipError(event, fmt.Errorf("run start identity does not match its event envelope"))
	}
	if event.Adapter != primitives.AdapterCodex {
		return relationshipError(event, fmt.Errorf("run start adapter must be %s", primitives.AdapterCodex))
	}
	return nil
}

func validateRelationshipScope(event eventlog.Event, run Projection) error {
	if event.RepoID != run.RepoID || event.WorktreeID != run.WorktreeID {
		return relationshipError(event, fmt.Errorf("repository or worktree identity does not match run start"))
	}
	return nil
}

func validateCaptureEvent(event eventlog.Event, run Projection, payload capturePayload) error {
	if payload.RunID != run.ID {
		return relationshipError(event, fmt.Errorf("run id does not match run start"))
	}
	if payload.Kind != CaptureWrapper && payload.Kind != CaptureProvider {
		return relationshipError(event, fmt.Errorf("invalid capture kind %q", payload.Kind))
	}
	if _, err := primitives.ParseSessionID(payload.SessionID.String()); err != nil {
		return relationshipError(event, err)
	}
	if _, err := primitives.ParseAdapterName(payload.Adapter.String()); err != nil {
		return relationshipError(event, err)
	}
	if payload.SessionID != event.SessionID || payload.Adapter != event.Adapter || event.TurnID != nil {
		return relationshipError(event, fmt.Errorf("capture payload does not match its event envelope"))
	}
	if payload.Kind == CaptureWrapper && event.SessionID != run.Start.SessionID {
		return relationshipError(event, fmt.Errorf("wrapper capture is not in the run-start session"))
	}
	if payload.Kind == CaptureProvider && event.SessionID == run.Start.SessionID {
		return relationshipError(event, fmt.Errorf("provider capture must remain distinct from wrapper capture"))
	}
	return nil
}

func validateAttemptEvent(event eventlog.Event, run Projection, payload attemptPayload, capture Capture, captureFound bool) error {
	if payload.RunID != run.ID {
		return relationshipError(event, fmt.Errorf("run id does not match run start"))
	}
	if _, err := primitives.ParseAttemptID(payload.AttemptID.String()); err != nil {
		return relationshipError(event, err)
	}
	if _, err := primitives.ParseSessionID(payload.SessionID.String()); err != nil {
		return relationshipError(event, err)
	}
	if _, err := primitives.NewTurnID(payload.TurnID.Uint64()); err != nil {
		return relationshipError(event, err)
	}
	if event.TurnID == nil || payload.SessionID != event.SessionID || payload.TurnID != *event.TurnID {
		return relationshipError(event, fmt.Errorf("attempt payload does not match its event envelope"))
	}
	if !captureFound || capture.Adapter != event.Adapter {
		return relationshipError(event, fmt.Errorf("attempt has no matching provider capture"))
	}
	return nil
}

func validateFinishEvent(event eventlog.Event, run Projection, payload finishPayload) error {
	if payload.RunID != run.ID {
		return relationshipError(event, fmt.Errorf("run id does not match run start"))
	}
	if payload.Status != StatusSucceeded && payload.Status != StatusFailed && payload.Status != StatusIncomplete {
		return relationshipError(event, fmt.Errorf("invalid terminal status %q", payload.Status))
	}
	if event.SessionID != run.Start.SessionID || event.TurnID != nil || event.Adapter != primitives.AdapterCodex {
		return relationshipError(event, fmt.Errorf("run finish does not match the run-start envelope"))
	}
	return nil
}

func relationshipError(event eventlog.Event, cause error) error {
	return fmt.Errorf("run relationship invariant failed for session %s event %s (%s): %w", event.SessionID, event.Seq, event.Type, cause)
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
func fieldForEvent(event eventlog.Event) string {
	switch event.Type {
	case primitives.EventTypeSessionStart:
		return "transcript"
	case primitives.EventTypeCheckpoint:
		return "checkpoint"
	case primitives.EventTypePromptUser:
		return "prompt"
	case primitives.EventTypeAssistantMessage:
		return "assistant"
	case primitives.EventTypeToolCall:
		return "tool_call"
	case primitives.EventTypeToolResult:
		return "tool_result"
	default:
		return ""
	}
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
