package turnevents

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/turns"
	"github.com/AadiJo/turnal/internal/workspacegit"
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
	Turn             uint64               `json:"turn"`
	Phase            string               `json:"phase"`
	CheckpointID     string               `json:"checkpoint_id,omitempty"`
	WorktreeID       string               `json:"worktree_id,omitempty"`
	StreamID         string               `json:"stream_id,omitempty"`
	CommitSHA        string               `json:"commit_sha"`
	CapturedAt       string               `json:"captured_at,omitempty"`
	Ref              string               `json:"ref"`
	CanonicalRef     string               `json:"canonical_ref,omitempty"`
	GitSyncRef       string               `json:"git_sync_ref,omitempty"`
	GitSyncCommitSHA string               `json:"git_sync_commit_sha,omitempty"`
	EventSeqStart    uint64               `json:"event_seq_start"`
	EventSeqEnd      uint64               `json:"event_seq_end"`
	UserGit          workspacegit.Context `json:"user_git"`
}

func (recorder Recorder) Start(sessionID primitives.SessionID, requestedTurnID primitives.TurnID) (turns.StartResult, error) {
	manager := recorder.Manager.WithCheckpointEvents(recorder.Adapter, recorder.RawRef)
	if err := RecoverCheckpointJournals(recorder.Log, manager.Repo); err != nil {
		return turns.StartResult{}, err
	}
	started, err := manager.Start(sessionID, requestedTurnID)
	if err != nil {
		return turns.StartResult{}, err
	}
	if err := AppendTurnStart(recorder.Log, recorder.Adapter, sessionID, started.TurnID, recorder.RawRef); err != nil {
		return turns.StartResult{}, fmt.Errorf("append turn.start event for session %s turn %s: %w", sessionID, started.TurnID, err)
	}
	if err := AppendCheckpointWithGitSync(recorder.Log, recorder.Adapter, sessionID, started.TurnID, primitives.CheckpointPhasePre, started.Pre, started.GitSync, recorder.RawRef); err != nil {
		return turns.StartResult{}, fmt.Errorf("append pre checkpoint event for session %s turn %s: %w", sessionID, started.TurnID, err)
	}
	return started, nil
}

func (recorder Recorder) Finish(sessionID primitives.SessionID, requestedTurnID primitives.TurnID) (turns.FinishResult, error) {
	manager := recorder.Manager.WithCheckpointEvents(recorder.Adapter, recorder.RawRef)
	if err := RecoverCheckpointJournals(recorder.Log, manager.Repo); err != nil {
		return turns.FinishResult{}, err
	}
	finished, err := manager.Finish(sessionID, requestedTurnID)
	if err != nil {
		return turns.FinishResult{}, err
	}
	if err := AppendTurnFinish(recorder.Log, recorder.Adapter, sessionID, finished.TurnID, recorder.RawRef); err != nil {
		return turns.FinishResult{}, fmt.Errorf("append turn.finish event for session %s turn %s: %w", sessionID, finished.TurnID, err)
	}
	if err := AppendCheckpointWithGitSync(recorder.Log, recorder.Adapter, sessionID, finished.TurnID, primitives.CheckpointPhasePost, finished.Post, finished.GitSync, recorder.RawRef); err != nil {
		return turns.FinishResult{}, fmt.Errorf("append post checkpoint event for session %s turn %s: %w", sessionID, finished.TurnID, err)
	}
	return finished, nil
}

func AppendTurnStart(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID, turnID primitives.TurnID, rawRef string) error {
	_, err := appendPayloadEvent(log, eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypeTurnStart,
		Adapter:   adapter,
		SourceID:  scopedSourceID(log, sessionID, fmt.Sprintf("%s:turn:%s:start", adapter, turnID)),
		RawRef:    rawRef,
		Payload:   mustJSON(turnPayload{Turn: turnID.Uint64()}),
	})
	return err
}

func AppendTurnFinish(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID, turnID primitives.TurnID, rawRef string) error {
	_, err := appendPayloadEvent(log, eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypeTurnFinish,
		Adapter:   adapter,
		SourceID:  scopedSourceID(log, sessionID, fmt.Sprintf("%s:turn:%s:finish", adapter, turnID)),
		RawRef:    rawRef,
		Payload:   mustJSON(turnPayload{Turn: turnID.Uint64()}),
	})
	return err
}

func AppendCheckpoint(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase, created checkpoint.Checkpoint, rawRef string) error {
	return AppendCheckpointWithGitSync(log, adapter, sessionID, turnID, phase, created, nil, rawRef)
}

func AppendCheckpointWithGitSync(log eventlog.Log, adapter primitives.AdapterName, sessionID primitives.SessionID, turnID primitives.TurnID, phase primitives.CheckpointPhase, created checkpoint.Checkpoint, gitSync *checkpoint.Snapshot, rawRef string) error {
	gitSyncRef := ""
	gitSyncCommitSHA := ""
	if gitSync != nil {
		gitSyncRef = gitSync.Ref
		gitSyncCommitSHA = gitSync.Commit.String()
	}
	repo := checkpointRepoFromLog(log)
	capturedAt := ""
	if !created.CapturedAt.IsZero() {
		capturedAt = created.CapturedAt.UTC().Format(time.RFC3339Nano)
	}
	userGit, err := workspacegit.Open(repo.WorkspaceRoot).Context()
	if err != nil {
		return err
	}
	event, err := appendPayloadEvent(log, eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypeCheckpoint,
		Adapter:   adapter,
		SourceID:  scopedSourceID(log, sessionID, fmt.Sprintf("%s:turn:%s:checkpoint:%s", adapter, turnID, phase)),
		RawRef:    rawRef,
		BuildPayload: func(context eventlog.AppendContext) (json.RawMessage, error) {
			eventSeqStart, err := checkpointEventSeqStart(context.PreviousEvents)
			if err != nil {
				return nil, err
			}
			return mustJSON(checkpointPayload{
				Turn:             turnID.Uint64(),
				Phase:            phase.String(),
				CheckpointID:     created.ID.String(),
				WorktreeID:       created.WorktreeID.String(),
				StreamID:         created.StreamID.String(),
				CommitSHA:        created.Commit.String(),
				CapturedAt:       capturedAt,
				Ref:              created.Ref.String(),
				CanonicalRef:     created.CanonicalRef.String(),
				GitSyncRef:       gitSyncRef,
				GitSyncCommitSHA: gitSyncCommitSHA,
				EventSeqStart:    eventSeqStart.Uint64(),
				EventSeqEnd:      context.Seq.Uint64(),
				UserGit:          userGit,
			}), nil
		},
	})
	if err != nil {
		return err
	}
	return repo.FinalizeCheckpointJournal(sessionID, turnID, phase, event.Seq, event.Hash)
}

func RecoverCheckpointJournals(log eventlog.Log, repo *checkpoint.Repo) error {
	if repo == nil {
		return nil
	}
	return repo.WithWorkspaceLock("recover checkpoint journals", func() error {
		return recoverCheckpointJournalsLocked(log, repo)
	})
}

func recoverCheckpointJournalsLocked(log eventlog.Log, repo *checkpoint.Repo) error {
	journals, err := repo.ListCheckpointJournals()
	if err != nil {
		return err
	}
	for _, journal := range journals {
		switch journal.State {
		case "intent":
			recovered, err := recoverIntentCheckpointJournal(log, repo, journal)
			if err != nil {
				return err
			}
			if recovered {
				continue
			}
			if err := repo.ClearCheckpointJournal(journal.SessionID, journal.TurnID, journal.Phase); err != nil {
				return err
			}
		case "committed":
			if journal.Adapter == "" {
				event, found, err := findRecordedCheckpointEvent(log, journal)
				if err != nil {
					return err
				}
				if !found {
					return fmt.Errorf("checkpoint invariant failed: committed checkpoint journal for session %s turn %s %s has no adapter and no matching recorded event", journal.SessionID, journal.TurnID, journal.Phase)
				}
				if err := repo.FinalizeCheckpointJournal(journal.SessionID, journal.TurnID, journal.Phase, event.Seq, event.Hash); err != nil {
					return err
				}
				continue
			}
			if err := recoverCheckpointJournal(log, repo, journal); err != nil {
				return err
			}
		case "finalized":
			if err := repo.ClearCheckpointJournal(journal.SessionID, journal.TurnID, journal.Phase); err != nil {
				return err
			}
		default:
			return fmt.Errorf("checkpoint invariant failed: unknown checkpoint journal state %q", journal.State)
		}
	}
	return nil
}

func findRecordedCheckpointEvent(log eventlog.Log, journal checkpoint.CheckpointJournal) (eventlog.Event, bool, error) {
	events, err := log.Read(journal.SessionID)
	if err != nil {
		return eventlog.Event{}, false, err
	}
	for _, event := range events {
		if event.Type != primitives.EventTypeCheckpoint || event.TurnID == nil || *event.TurnID != journal.TurnID {
			continue
		}
		var payload struct {
			Phase     string `json:"phase"`
			CommitSHA string `json:"commit_sha"`
			Ref       string `json:"ref"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return eventlog.Event{}, false, fmt.Errorf("checkpoint invariant failed: parse recorded checkpoint event %s: %w", event.Seq, err)
		}
		if payload.Phase == journal.Phase.String() && payload.CommitSHA == journal.CommitSHA.String() && payload.Ref == journal.Ref.String() {
			return event, true, nil
		}
	}
	return eventlog.Event{}, false, nil
}

func recoverIntentCheckpointJournal(log eventlog.Log, repo *checkpoint.Repo, journal checkpoint.CheckpointJournal) (bool, error) {
	ref, err := repo.CheckpointRefFor(journal.SessionID, journal.TurnID, journal.Phase)
	if err != nil {
		return false, err
	}
	commit, err := repo.CheckpointCommit(ref)
	if err != nil {
		return false, nil
	}
	checkpointID := journal.CheckpointID
	if checkpointID == "" {
		checkpointID, err = primitives.NewCheckpointID()
		if err != nil {
			return false, err
		}
	}
	canonicalRef, err := repo.EnsureCanonicalCheckpointRef(checkpointID, commit)
	if err != nil {
		return false, err
	}
	created := checkpoint.Checkpoint{ID: checkpointID, Ref: ref, CanonicalRef: canonicalRef, Commit: commit, WorktreeID: journal.WorktreeID, StreamID: journal.StreamID}
	if err := repo.MarkCheckpointJournalCommitted(journal.SessionID, journal.TurnID, journal.Phase, created); err != nil {
		return false, err
	}
	journal.Ref = ref
	journal.CanonicalRef = canonicalRef
	journal.CommitSHA = commit
	return true, recoverCheckpointJournal(log, repo, journal)
}

func recoverCheckpointJournal(log eventlog.Log, repo *checkpoint.Repo, journal checkpoint.CheckpointJournal) error {
	if journal.Adapter == "" {
		return fmt.Errorf("checkpoint invariant failed: committed checkpoint journal for session %s turn %s %s has no adapter", journal.SessionID, journal.TurnID, journal.Phase)
	}
	if journal.Ref == "" || journal.CommitSHA == "" {
		return fmt.Errorf("checkpoint invariant failed: committed checkpoint journal for session %s turn %s %s has no ref/commit", journal.SessionID, journal.TurnID, journal.Phase)
	}
	if _, err := repo.CheckpointCommit(journal.Ref); err != nil {
		return fmt.Errorf("checkpoint invariant failed: checkpoint journal ref %s is not readable: %w", journal.Ref, err)
	}
	if journal.CheckpointID == "" {
		checkpointID, err := primitives.NewCheckpointID()
		if err != nil {
			return err
		}
		journal.CheckpointID = checkpointID
	}
	if journal.CanonicalRef == "" {
		canonicalRef, err := repo.EnsureCanonicalCheckpointRef(journal.CheckpointID, journal.CommitSHA)
		if err != nil {
			return err
		}
		journal.CanonicalRef = canonicalRef
	}
	if journal.WorktreeID == "" {
		journal.WorktreeID = repo.WorktreeID
	}
	if journal.StreamID == "" {
		streamID, err := repo.StreamID(journal.SessionID)
		if err != nil {
			return err
		}
		journal.StreamID = streamID
	}

	if journal.Phase == primitives.CheckpointPhasePre {
		if err := AppendTurnStart(log, journal.Adapter, journal.SessionID, journal.TurnID, journal.RawRef); err != nil {
			return err
		}
	} else if journal.Phase == primitives.CheckpointPhasePost {
		if err := AppendTurnFinish(log, journal.Adapter, journal.SessionID, journal.TurnID, journal.RawRef); err != nil {
			return err
		}
	}

	var gitSync *checkpoint.Snapshot
	if journal.GitSyncRef != "" && journal.GitSyncCommitSHA != "" {
		gitSyncCommit, err := primitives.ParseCommitSHA(journal.GitSyncCommitSHA)
		if err != nil {
			return err
		}
		gitSync = &checkpoint.Snapshot{Ref: journal.GitSyncRef, Commit: gitSyncCommit}
	}
	if err := AppendCheckpointWithGitSync(log, journal.Adapter, journal.SessionID, journal.TurnID, journal.Phase, checkpoint.Checkpoint{
		ID:           journal.CheckpointID,
		Ref:          journal.Ref,
		CanonicalRef: journal.CanonicalRef,
		Commit:       journal.CommitSHA,
		CapturedAt:   checkpointJournalCapturedAt(journal),
		WorktreeID:   journal.WorktreeID,
		StreamID:     journal.StreamID,
	}, gitSync, journal.RawRef); err != nil {
		return err
	}
	if journal.Phase == primitives.CheckpointPhasePost {
		if err := turns.NewManager(repo).ClearActiveForRecoveryLocked(journal.SessionID, journal.TurnID); err != nil {
			return fmt.Errorf("clear recovered active turn state: %w", err)
		}
	}
	return nil
}

func checkpointJournalCapturedAt(journal checkpoint.CheckpointJournal) time.Time {
	if parsed, err := time.Parse(time.RFC3339Nano, journal.CapturedAt); err == nil {
		return parsed
	}
	return time.Time{}
}

func checkpointEventSeqStart(events []eventlog.Event) (primitives.EventSeq, error) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != primitives.EventTypeCheckpoint {
			continue
		}
		return primitives.NewEventSeq(events[i].Seq.Uint64() + 1)
	}
	return primitives.NewEventSeq(1)
}

func appendPayloadEvent(log eventlog.Log, input eventlog.AppendInput) (eventlog.Event, error) {
	if input.SourceID != "" {
		event, seen, err := log.FindSourceID(input.SessionID, input.SourceID)
		if err != nil {
			return eventlog.Event{}, err
		}
		if seen {
			return event, nil
		}
	}
	return log.Append(input)
}

func scopedSourceID(log eventlog.Log, sessionID primitives.SessionID, sourceID string) string {
	if log.ProducerID == "" {
		return sourceID
	}
	streamID, err := primitives.DeriveEventStreamID(log.ProducerID, sessionID)
	if err != nil {
		return sourceID
	}
	return streamID.String() + ":" + sourceID
}

func checkpointRepoFromLog(log eventlog.Log) *checkpoint.Repo {
	metadataDir := filepath.Clean(filepath.Join(log.Dir, "..", ".."))
	root := log.WorkspaceRoot
	if root == "" {
		root = filepath.Dir(metadataDir)
	}
	return &checkpoint.Repo{
		WorkspaceRoot:   primitives.WorkspaceRoot(root),
		MetadataDir:     metadataDir,
		GitDir:          filepath.Join(metadataDir, "git"),
		TmpDir:          filepath.Join(metadataDir, "tmp"),
		RepoID:          log.RepoID,
		StoreID:         log.StoreID,
		WorktreeID:      log.WorktreeID,
		EventProducerID: log.ProducerID,
	}
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
