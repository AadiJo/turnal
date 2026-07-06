package turnevents

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"agent-vcs-again/internal/checkpoint"
	eventlog "agent-vcs-again/internal/events"
	"agent-vcs-again/internal/primitives"
	"agent-vcs-again/internal/turns"
	"agent-vcs-again/internal/workspacegit"
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
	Turn          uint64               `json:"turn"`
	Phase         string               `json:"phase"`
	CommitSHA     string               `json:"commit_sha"`
	Ref           string               `json:"ref"`
	GitSyncRef    string               `json:"git_sync_ref,omitempty"`
	EventSeqStart uint64               `json:"event_seq_start"`
	EventSeqEnd   uint64               `json:"event_seq_end"`
	UserGit       workspacegit.Context `json:"user_git"`
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
		SourceID:  fmt.Sprintf("%s:turn:%s:start", adapter, turnID),
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
		SourceID:  fmt.Sprintf("%s:turn:%s:finish", adapter, turnID),
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
	if gitSync != nil {
		gitSyncRef = gitSync.Ref
	}
	repo := checkpointRepoFromLog(log)
	userGit, err := workspacegit.Open(repo.WorkspaceRoot).Context()
	if err != nil {
		return err
	}
	event, err := appendPayloadEvent(log, eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypeCheckpoint,
		Adapter:   adapter,
		SourceID:  fmt.Sprintf("%s:turn:%s:checkpoint:%s", adapter, turnID, phase),
		RawRef:    rawRef,
		BuildPayload: func(context eventlog.AppendContext) (json.RawMessage, error) {
			eventSeqStart, err := checkpointEventSeqStart(context.PreviousEvents)
			if err != nil {
				return nil, err
			}
			return mustJSON(checkpointPayload{
				Turn:          turnID.Uint64(),
				Phase:         phase.String(),
				CommitSHA:     created.Commit.String(),
				Ref:           created.Ref.String(),
				GitSyncRef:    gitSyncRef,
				EventSeqStart: eventSeqStart.Uint64(),
				EventSeqEnd:   context.Seq.Uint64(),
				UserGit:       userGit,
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
	journals, err := repo.ListCheckpointJournals()
	if err != nil {
		return err
	}
	for _, journal := range journals {
		switch journal.State {
		case "intent":
			if err := repo.ClearCheckpointJournal(journal.SessionID, journal.TurnID, journal.Phase); err != nil {
				return err
			}
		case "committed":
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
	return AppendCheckpointWithGitSync(log, journal.Adapter, journal.SessionID, journal.TurnID, journal.Phase, checkpoint.Checkpoint{
		Ref:    journal.Ref,
		Commit: journal.CommitSHA,
	}, gitSync, journal.RawRef)
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

func checkpointRepoFromLog(log eventlog.Log) *checkpoint.Repo {
	metadataDir := filepath.Clean(filepath.Join(log.Dir, "..", ".."))
	root := filepath.Dir(metadataDir)
	return &checkpoint.Repo{
		WorkspaceRoot: primitives.WorkspaceRoot(root),
		MetadataDir:   metadataDir,
		GitDir:        filepath.Join(metadataDir, "git"),
		TmpDir:        filepath.Join(metadataDir, "tmp"),
	}
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
