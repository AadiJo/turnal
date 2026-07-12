package recall

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/AadiJo/turnal/internal/adapters"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/workspacegit"
)

type Options struct {
	IncludeRaw        bool
	IncludeTranscript bool
	WorktreeID        primitives.WorktreeID
	StreamID          primitives.EventStreamID
}

type Reader struct {
	MetadataDir string
	WorktreeID  primitives.WorktreeID
}

type Turn struct {
	SessionID       primitives.SessionID     `json:"session_id"`
	WorktreeID      primitives.WorktreeID    `json:"worktree_id,omitempty"`
	StreamID        primitives.EventStreamID `json:"stream_id,omitempty"`
	TurnID          primitives.TurnID        `json:"turn_id"`
	Adapters        []primitives.AdapterName `json:"adapters,omitempty"`
	StartedAt       *primitives.Timestamp    `json:"started_at,omitempty"`
	FinishedAt      *primitives.Timestamp    `json:"finished_at,omitempty"`
	PreCheckpoint   *Checkpoint              `json:"pre_checkpoint,omitempty"`
	PostCheckpoint  *Checkpoint              `json:"post_checkpoint,omitempty"`
	Complete        bool                     `json:"complete"`
	SessionEvents   []eventlog.Event         `json:"session_events,omitempty"`
	Events          []eventlog.Event         `json:"events"`
	RawRecords      []RawRecord              `json:"raw_records,omitempty"`
	RawRecordErrors []RawRecordError         `json:"raw_record_errors,omitempty"`
	Transcript      *Transcript              `json:"transcript,omitempty"`
}

type Checkpoint struct {
	Phase         primitives.CheckpointPhase `json:"phase"`
	CommitSHA     primitives.CommitSHA       `json:"commit_sha"`
	Ref           primitives.CheckpointRef   `json:"ref"`
	EventSeqStart *primitives.EventSeq       `json:"event_seq_start,omitempty"`
	EventSeqEnd   *primitives.EventSeq       `json:"event_seq_end,omitempty"`
	UserGit       *workspacegit.Context      `json:"user_git,omitempty"`
}

type RawRecord struct {
	RawRef string                 `json:"raw_ref"`
	Record adapters.RawHookRecord `json:"record"`
}

type RawRecordError struct {
	RawRef string `json:"raw_ref"`
	Error  string `json:"error"`
}

type checkpointPayload struct {
	Turn          uint64                `json:"turn"`
	Phase         string                `json:"phase"`
	WorktreeID    string                `json:"worktree_id,omitempty"`
	StreamID      string                `json:"stream_id,omitempty"`
	CommitSHA     string                `json:"commit_sha"`
	Ref           string                `json:"ref"`
	EventSeqStart uint64                `json:"event_seq_start,omitempty"`
	EventSeqEnd   uint64                `json:"event_seq_end,omitempty"`
	UserGit       *workspacegit.Context `json:"user_git,omitempty"`
}

type sessionPayload struct {
	ProviderSessionID string `json:"provider_session_id"`
	Model             string `json:"model,omitempty"`
	PermissionMode    string `json:"permission_mode,omitempty"`
	TranscriptPath    string `json:"transcript_path,omitempty"`
}

type promptPayload struct {
	Text           string `json:"text"`
	ProviderTurnID string `json:"provider_turn_id,omitempty"`
}

func NewReader(metadataDir string) Reader {
	return Reader{MetadataDir: metadataDir}
}

func NewScopedReader(metadataDir string, worktreeID primitives.WorktreeID) Reader {
	return Reader{MetadataDir: metadataDir, WorktreeID: worktreeID}
}

func (reader Reader) RecallTurn(sessionID primitives.SessionID, turnID primitives.TurnID, options Options) (Turn, error) {
	if strings.TrimSpace(reader.MetadataDir) == "" {
		return Turn{}, fmt.Errorf("recall requires metadata dir")
	}
	parsedSessionID, err := primitives.ParseSessionID(sessionID.String())
	if err != nil {
		return Turn{}, err
	}
	parsedTurnID, err := primitives.NewTurnID(turnID.Uint64())
	if err != nil {
		return Turn{}, err
	}

	events, err := eventlog.Open(reader.MetadataDir).Read(parsedSessionID)
	if err != nil {
		return Turn{}, err
	}

	var candidateStreams = map[primitives.EventStreamID]primitives.WorktreeID{}
	for _, event := range events {
		if event.TurnID == nil || *event.TurnID != parsedTurnID {
			continue
		}
		if options.StreamID != "" && event.StreamID != options.StreamID {
			continue
		}
		if options.WorktreeID != "" && event.WorktreeID != "" && event.WorktreeID != options.WorktreeID {
			continue
		}
		candidateStreams[event.StreamID] = event.WorktreeID
	}
	if len(candidateStreams) > 1 && options.StreamID == "" {
		var choices []string
		for streamID, worktreeID := range candidateStreams {
			choices = append(choices, fmt.Sprintf("stream=%s worktree=%s", streamID, worktreeID))
		}
		sort.Strings(choices)
		return Turn{}, fmt.Errorf("turn %s:%s is ambiguous across event streams: %s", parsedSessionID, parsedTurnID, strings.Join(choices, ", "))
	}
	var selectedStream primitives.EventStreamID
	var selectedWorktree primitives.WorktreeID
	for streamID, worktreeID := range candidateStreams {
		selectedStream = streamID
		selectedWorktree = worktreeID
	}

	turn := Turn{
		SessionID:  parsedSessionID,
		WorktreeID: selectedWorktree,
		StreamID:   selectedStream,
		TurnID:     parsedTurnID,
	}
	adapterSet := map[primitives.AdapterName]struct{}{}
	for _, event := range events {
		if selectedStream != "" && event.StreamID != selectedStream {
			continue
		}
		if event.Type == primitives.EventTypeSessionStart && event.TurnID == nil {
			turn.SessionEvents = append(turn.SessionEvents, event)
		}
		if event.TurnID == nil || *event.TurnID != parsedTurnID {
			continue
		}
		turn.Events = append(turn.Events, event)
		if event.Adapter != "" {
			adapterSet[event.Adapter] = struct{}{}
		}
		if err := applyEventMetadata(&turn, event); err != nil {
			return Turn{}, err
		}
	}
	if len(turn.Events) == 0 {
		return Turn{}, fmt.Errorf("recall invariant failed for session %s turn %s: no events found", parsedSessionID, parsedTurnID)
	}

	for adapter := range adapterSet {
		turn.Adapters = append(turn.Adapters, adapter)
	}
	sort.Slice(turn.Adapters, func(i, j int) bool {
		return turn.Adapters[i].String() < turn.Adapters[j].String()
	})

	turn.Complete = turn.StartedAt != nil && turn.FinishedAt != nil && turn.PreCheckpoint != nil && turn.PostCheckpoint != nil

	if options.IncludeRaw {
		turn.RawRecords, turn.RawRecordErrors = reader.rawRecords(turn)
	}
	if options.IncludeTranscript {
		turn.Transcript = reader.transcript(turn, events)
	}
	return turn, nil
}

func applyEventMetadata(turn *Turn, event eventlog.Event) error {
	switch event.Type {
	case primitives.EventTypeTurnStart:
		if turn.StartedAt == nil {
			startedAt := event.Time
			turn.StartedAt = &startedAt
		}
	case primitives.EventTypeTurnFinish:
		finishedAt := event.Time
		turn.FinishedAt = &finishedAt
	case primitives.EventTypeCheckpoint:
		checkpoint, err := parseCheckpointPayload(turn.SessionID, turn.TurnID, event)
		if err != nil {
			return err
		}
		switch checkpoint.Phase {
		case primitives.CheckpointPhasePre:
			turn.PreCheckpoint = &checkpoint
		case primitives.CheckpointPhasePost:
			turn.PostCheckpoint = &checkpoint
		}
	}
	return nil
}

func parseCheckpointPayload(sessionID primitives.SessionID, turnID primitives.TurnID, event eventlog.Event) (Checkpoint, error) {
	var parsed checkpointPayload
	if err := json.Unmarshal(event.Payload, &parsed); err != nil {
		return Checkpoint{}, fmt.Errorf("recall invariant failed for session %s turn %s: malformed checkpoint payload: %w", sessionID, turnID, err)
	}
	if parsed.Turn != turnID.Uint64() {
		return Checkpoint{}, fmt.Errorf("recall invariant failed for session %s turn %s: checkpoint payload turn %d does not match", sessionID, turnID, parsed.Turn)
	}
	phase, err := primitives.ParseCheckpointPhase(parsed.Phase)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("recall invariant failed for session %s turn %s: %w", sessionID, turnID, err)
	}
	ref, err := primitives.ParseCheckpointRef(parsed.Ref)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("recall invariant failed for session %s turn %s: %w", sessionID, turnID, err)
	}
	refParts, err := ref.Parts()
	if err != nil {
		return Checkpoint{}, fmt.Errorf("recall invariant failed for session %s turn %s: %w", sessionID, turnID, err)
	}
	if refParts.SessionID != sessionID || refParts.TurnID != turnID || refParts.Phase != phase || !refParts.HasPhase {
		return Checkpoint{}, fmt.Errorf("recall invariant failed for session %s turn %s: checkpoint ref %s does not match payload phase %s", sessionID, turnID, ref, phase)
	}
	if parsed.WorktreeID != "" {
		worktreeID, err := primitives.ParseWorktreeID(parsed.WorktreeID)
		if err != nil {
			return Checkpoint{}, fmt.Errorf("recall invariant failed for session %s turn %s: checkpoint payload worktree: %w", sessionID, turnID, err)
		}
		if event.WorktreeID != "" && worktreeID != event.WorktreeID {
			return Checkpoint{}, fmt.Errorf("recall invariant failed for session %s turn %s: checkpoint payload worktree %s does not match event worktree %s", sessionID, turnID, worktreeID, event.WorktreeID)
		}
		if refParts.Scoped && worktreeID != refParts.WorktreeID {
			return Checkpoint{}, fmt.Errorf("recall invariant failed for session %s turn %s: checkpoint ref worktree %s does not match payload worktree %s", sessionID, turnID, refParts.WorktreeID, worktreeID)
		}
	}
	if parsed.StreamID != "" {
		streamID, err := primitives.ParseEventStreamID(parsed.StreamID)
		if err != nil {
			return Checkpoint{}, fmt.Errorf("recall invariant failed for session %s turn %s: checkpoint payload stream: %w", sessionID, turnID, err)
		}
		if event.StreamID != "" && streamID != event.StreamID {
			return Checkpoint{}, fmt.Errorf("recall invariant failed for session %s turn %s: checkpoint payload stream %s does not match event stream %s", sessionID, turnID, streamID, event.StreamID)
		}
		if refParts.Scoped && streamID != refParts.StreamID {
			return Checkpoint{}, fmt.Errorf("recall invariant failed for session %s turn %s: checkpoint ref stream %s does not match payload stream %s", sessionID, turnID, refParts.StreamID, streamID)
		}
	}
	if refParts.Scoped {
		if parsed.WorktreeID == "" || parsed.StreamID == "" {
			return Checkpoint{}, fmt.Errorf("recall invariant failed for session %s turn %s: scoped checkpoint payload requires worktree_id and stream_id", sessionID, turnID)
		}
		if event.WorktreeID == "" || event.StreamID == "" {
			return Checkpoint{}, fmt.Errorf("recall invariant failed for session %s turn %s: scoped checkpoint ref requires event worktree and stream identity", sessionID, turnID)
		}
		if refParts.WorktreeID != event.WorktreeID || refParts.StreamID != event.StreamID {
			return Checkpoint{}, fmt.Errorf("recall invariant failed for session %s turn %s: scoped checkpoint ref identity does not match event worktree and stream", sessionID, turnID)
		}
	}
	commit, err := primitives.ParseCommitSHA(parsed.CommitSHA)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("recall invariant failed for session %s turn %s: %w", sessionID, turnID, err)
	}
	checkpoint := Checkpoint{Phase: phase, CommitSHA: commit, Ref: ref, UserGit: parsed.UserGit}
	if parsed.EventSeqStart != 0 || parsed.EventSeqEnd != 0 {
		eventSeqStart, err := primitives.NewEventSeq(parsed.EventSeqStart)
		if err != nil {
			return Checkpoint{}, fmt.Errorf("recall invariant failed for session %s turn %s: %w", sessionID, turnID, err)
		}
		eventSeqEnd, err := primitives.NewEventSeq(parsed.EventSeqEnd)
		if err != nil {
			return Checkpoint{}, fmt.Errorf("recall invariant failed for session %s turn %s: %w", sessionID, turnID, err)
		}
		if eventSeqStart.Uint64() > eventSeqEnd.Uint64() {
			return Checkpoint{}, fmt.Errorf("recall invariant failed for session %s turn %s: checkpoint event sequence range %s-%s is invalid", sessionID, turnID, eventSeqStart, eventSeqEnd)
		}
		checkpoint.EventSeqStart = &eventSeqStart
		checkpoint.EventSeqEnd = &eventSeqEnd
	}
	return checkpoint, nil
}

func (reader Reader) rawRecords(turn Turn) ([]RawRecord, []RawRecordError) {
	rawRefs := orderedRawRefs(turn)
	records := make([]RawRecord, 0, len(rawRefs))
	var rawErrors []RawRecordError
	for _, rawRef := range rawRefs {
		record, err := adapters.ReadRawHookRecord(reader.MetadataDir, rawRef)
		if err != nil {
			rawErrors = append(rawErrors, RawRecordError{RawRef: rawRef, Error: err.Error()})
			continue
		}
		records = append(records, RawRecord{RawRef: rawRef, Record: record})
	}
	return records, rawErrors
}

func orderedRawRefs(turn Turn) []string {
	seen := map[string]struct{}{}
	var refs []string
	add := func(events []eventlog.Event) {
		for _, event := range events {
			if event.RawRef == "" {
				continue
			}
			if _, ok := seen[event.RawRef]; ok {
				continue
			}
			seen[event.RawRef] = struct{}{}
			refs = append(refs, event.RawRef)
		}
	}
	add(turn.SessionEvents)
	add(turn.Events)
	return refs
}

func WriteText(w io.Writer, turn Turn) error {
	if _, err := fmt.Fprintf(w, "session %s turn %s\n", turn.SessionID, turn.TurnID); err != nil {
		return err
	}
	if len(turn.Adapters) > 0 {
		if _, err := fmt.Fprintf(w, "adapters: %s\n", joinAdapters(turn.Adapters)); err != nil {
			return err
		}
	}
	if turn.StartedAt != nil {
		if _, err := fmt.Fprintf(w, "started: %s\n", turn.StartedAt); err != nil {
			return err
		}
	}
	if turn.FinishedAt != nil {
		if _, err := fmt.Fprintf(w, "finished: %s\n", turn.FinishedAt); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "complete: %t\n", turn.Complete); err != nil {
		return err
	}
	if turn.PreCheckpoint != nil {
		if _, err := fmt.Fprintf(w, "pre: %s %s\n", turn.PreCheckpoint.CommitSHA, turn.PreCheckpoint.Ref); err != nil {
			return err
		}
	}
	if turn.PostCheckpoint != nil {
		if _, err := fmt.Fprintf(w, "post: %s %s\n", turn.PostCheckpoint.CommitSHA, turn.PostCheckpoint.Ref); err != nil {
			return err
		}
	}

	if len(turn.SessionEvents) > 0 {
		if _, err := fmt.Fprintln(w, "\nsession events:"); err != nil {
			return err
		}
		if err := writeEvents(w, turn.SessionEvents); err != nil {
			return err
		}
	}

	if _, err := fmt.Fprintln(w, "\nturn events:"); err != nil {
		return err
	}
	if err := writeEvents(w, turn.Events); err != nil {
		return err
	}

	if len(turn.RawRecords) > 0 {
		if _, err := fmt.Fprintln(w, "\nraw adapter records:"); err != nil {
			return err
		}
		for _, rawRecord := range turn.RawRecords {
			if _, err := fmt.Fprintf(w, "[%s] adapter=%s hook=%s received_at=%s cwd=%s\n", rawRecord.RawRef, rawRecord.Record.Adapter, rawRecord.Record.Hook, rawRecord.Record.ReceivedAt, rawRecord.Record.CWD); err != nil {
				return err
			}
			switch {
			case len(rawRecord.Record.Payload) > 0:
				if _, err := fmt.Fprintf(w, "payload: %s\n", prettyJSON(rawRecord.Record.Payload)); err != nil {
					return err
				}
			case rawRecord.Record.Raw != "":
				if _, err := fmt.Fprintf(w, "raw: %s\n", rawRecord.Record.Raw); err != nil {
					return err
				}
			}
			if rawRecord.Record.Error != "" {
				if _, err := fmt.Fprintf(w, "error: %s\n", rawRecord.Record.Error); err != nil {
					return err
				}
			}
		}
	}

	if len(turn.RawRecordErrors) > 0 {
		if _, err := fmt.Fprintln(w, "\nraw adapter record errors:"); err != nil {
			return err
		}
		for _, rawError := range turn.RawRecordErrors {
			if _, err := fmt.Fprintf(w, "[%s] %s\n", rawError.RawRef, rawError.Error); err != nil {
				return err
			}
		}
	}

	if turn.Transcript != nil {
		if _, err := fmt.Fprintln(w, "\ntranscript:"); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "path: %s\n", turn.Transcript.Path); err != nil {
			return err
		}
		if turn.Transcript.Adapter != "" {
			if _, err := fmt.Fprintf(w, "adapter: %s\n", turn.Transcript.Adapter); err != nil {
				return err
			}
		}
		for _, message := range turn.Transcript.Messages {
			if _, err := fmt.Fprintf(w, "[%d] %s", message.Index, message.Role); err != nil {
				return err
			}
			if message.ID != "" {
				if _, err := fmt.Fprintf(w, " id=%s", message.ID); err != nil {
					return err
				}
			}
			if message.Timestamp != "" {
				if _, err := fmt.Fprintf(w, " time=%s", message.Timestamp); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(w, "\n%s\n", message.Text); err != nil {
				return err
			}
		}
		for _, transcriptError := range turn.Transcript.Errors {
			if _, err := fmt.Fprintf(w, "error: %s\n", transcriptError); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeEvents(w io.Writer, events []eventlog.Event) error {
	for _, event := range events {
		if _, err := fmt.Fprintf(w, "[%s] %s adapter=%s time=%s", event.Seq, event.Type, event.Adapter, event.Time); err != nil {
			return err
		}
		if event.SourceID != "" {
			if _, err := fmt.Fprintf(w, " source=%s", event.SourceID); err != nil {
				return err
			}
		}
		if event.RawRef != "" {
			if _, err := fmt.Fprintf(w, " raw=%s", event.RawRef); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "\npayload: %s\n", prettyJSON(event.Payload)); err != nil {
			return err
		}
	}
	return nil
}

func prettyJSON(data json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, data, "", "  "); err != nil {
		return string(data)
	}
	return buf.String()
}

func joinAdapters(adapters []primitives.AdapterName) string {
	values := make([]string, 0, len(adapters))
	for _, adapter := range adapters {
		values = append(values, adapter.String())
	}
	return strings.Join(values, ",")
}
