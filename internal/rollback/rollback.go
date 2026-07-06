package rollback

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"agent-vcs-again/internal/checkpoint"
	eventlog "agent-vcs-again/internal/events"
	"agent-vcs-again/internal/primitives"
)

const journalFileName = "rollback-journal.json"

type Engine struct {
	Repo *checkpoint.Repo
}

type Request struct {
	Target primitives.TargetRef
	DryRun bool
}

type ResolvedTarget struct {
	Target        primitives.TargetRef
	CheckpointRef primitives.CheckpointRef
	Commit        primitives.CommitSHA
	SessionID     primitives.SessionID
	TurnID        primitives.TurnID
	Phase         primitives.CheckpointPhase
}

type Result struct {
	Target ResolvedTarget
	Plan   checkpoint.RestorePlan
	Safety *checkpoint.Snapshot
	Event  *eventlog.Event
	DryRun bool
}

type ChangeSummary struct {
	Total       int `json:"total"`
	Added       int `json:"added"`
	Modified    int `json:"modified"`
	Deleted     int `json:"deleted"`
	ModeChanged int `json:"mode_changed"`
}

type EventPayload struct {
	Turn            uint64        `json:"turn"`
	Phase           string        `json:"phase"`
	Target          string        `json:"target"`
	Ref             string        `json:"ref"`
	CommitSHA       string        `json:"commit_sha"`
	SafetyRef       string        `json:"safety_ref"`
	SafetyCommitSHA string        `json:"safety_commit_sha"`
	ChangeSummary   ChangeSummary `json:"change_summary"`
}

type Journal struct {
	Version         int                        `json:"version"`
	State           string                     `json:"state"`
	StartedAt       string                     `json:"started_at"`
	UpdatedAt       string                     `json:"updated_at"`
	Target          string                     `json:"target"`
	CheckpointRef   string                     `json:"checkpoint_ref"`
	TargetCommitSHA string                     `json:"target_commit_sha"`
	SafetyRef       string                     `json:"safety_ref"`
	SafetyCommitSHA string                     `json:"safety_commit_sha"`
	EventSourceID   string                     `json:"event_source_id"`
	Changes         []checkpoint.RestoreChange `json:"changes"`
}

type ActiveJournalError struct {
	Path    string
	Journal Journal
}

func (err ActiveJournalError) Error() string {
	return fmt.Sprintf(
		"rollback invariant failed: active rollback journal exists at %s; previous rollback may be incomplete (state=%s target=%s safety_ref=%s safety_commit=%s)",
		err.Path,
		err.Journal.State,
		err.Journal.Target,
		err.Journal.SafetyRef,
		err.Journal.SafetyCommitSHA,
	)
}

type SafetyError struct {
	Op          string
	Err         error
	Safety      checkpoint.Snapshot
	JournalPath string
}

func (err SafetyError) Error() string {
	return fmt.Sprintf(
		"%s: %v (safety_ref=%s safety_commit=%s journal=%s)",
		err.Op,
		err.Err,
		err.Safety.Ref,
		err.Safety.Commit,
		err.JournalPath,
	)
}

func (err SafetyError) Unwrap() error {
	return err.Err
}

func New(repo *checkpoint.Repo) Engine {
	return Engine{Repo: repo}
}

func JournalPath(repo *checkpoint.Repo) string {
	return filepath.Join(repo.TmpDir, journalFileName)
}

func ResolveTarget(repo *checkpoint.Repo, target primitives.TargetRef) (ResolvedTarget, error) {
	phase, ok := target.Phase()
	if !ok {
		phase = primitives.CheckpointPhasePre
		var err error
		target, err = primitives.NewTargetRef(target.SessionID(), target.TurnID(), phase)
		if err != nil {
			return ResolvedTarget{}, err
		}
	}

	ref, err := target.CheckpointRef()
	if err != nil {
		return ResolvedTarget{}, err
	}
	commit, err := repo.CheckpointCommit(ref)
	if err != nil {
		return ResolvedTarget{}, err
	}
	return ResolvedTarget{
		Target:        target,
		CheckpointRef: ref,
		Commit:        commit,
		SessionID:     target.SessionID(),
		TurnID:        target.TurnID(),
		Phase:         phase,
	}, nil
}

func (engine Engine) Run(request Request) (Result, error) {
	if engine.Repo == nil {
		return Result{}, fmt.Errorf("rollback repo is required")
	}
	if err := engine.ensureNoActiveJournal(); err != nil {
		return Result{}, err
	}

	target, err := ResolveTarget(engine.Repo, request.Target)
	if err != nil {
		return Result{}, err
	}
	plan, err := engine.Repo.PlanRestoreCommit(target.Commit)
	if err != nil {
		return Result{}, err
	}

	result := Result{Target: target, Plan: plan, DryRun: request.DryRun}
	if request.DryRun {
		return result, nil
	}

	safetyRef := safetyRef(target, time.Now().UTC())
	safety, err := engine.Repo.CreateSnapshotRef(safetyRef, fmt.Sprintf("agent-vcs rollback safety %s", target.Target))
	if err != nil {
		return result, err
	}
	result.Safety = &safety
	eventSourceID := rollbackEventSourceID(target, safety)

	journal := Journal{
		Version:         1,
		State:           "planned",
		StartedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		Target:          target.Target.String(),
		CheckpointRef:   target.CheckpointRef.String(),
		TargetCommitSHA: target.Commit.String(),
		SafetyRef:       safety.Ref,
		SafetyCommitSHA: safety.Commit.String(),
		EventSourceID:   eventSourceID,
		Changes:         plan.Changes,
	}
	if err := writeJournal(JournalPath(engine.Repo), journal); err != nil {
		return result, engine.safetyError("write rollback journal", safety, err)
	}

	journal.State = "restoring"
	if err := writeJournal(JournalPath(engine.Repo), journal); err != nil {
		return result, engine.safetyError("write rollback journal", safety, err)
	}
	if err := engine.Repo.RestoreCommit(target.Commit); err != nil {
		return result, engine.safetyError("restore checkpoint", safety, err)
	}

	journal.State = "restored"
	if err := writeJournal(JournalPath(engine.Repo), journal); err != nil {
		return result, engine.safetyError("write rollback journal", safety, err)
	}

	event, err := appendRollbackEvent(engine.Repo, target, safety, plan, eventSourceID)
	if err != nil {
		return result, engine.safetyError("append rollback event", safety, err)
	}
	result.Event = &event

	if err := os.Remove(JournalPath(engine.Repo)); err != nil && !os.IsNotExist(err) {
		return result, engine.safetyError("clear rollback journal", safety, err)
	}
	return result, nil
}

func (engine Engine) ensureNoActiveJournal() error {
	path := JournalPath(engine.Repo)
	journal, ok, err := readJournal(path)
	if err != nil {
		return err
	}
	if ok {
		if journal.State == "restored" {
			return engine.finalizeRestoredJournal(path, journal)
		}
		return ActiveJournalError{Path: path, Journal: journal}
	}
	return nil
}

func (engine Engine) finalizeRestoredJournal(path string, journal Journal) error {
	target, safety, plan, eventSourceID, err := journalRollback(journal)
	if err != nil {
		return err
	}

	log := eventlog.Open(engine.Repo.MetadataDir)
	exists, err := rollbackEventExists(log, target, safety, eventSourceID)
	if err != nil {
		return fmt.Errorf("finalize restored rollback journal: %w", err)
	}
	if !exists {
		if _, err := appendRollbackEvent(engine.Repo, target, safety, plan, eventSourceID); err != nil {
			return SafetyError{
				Op:          "finalize restored rollback journal",
				Err:         err,
				Safety:      safety,
				JournalPath: path,
			}
		}
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return SafetyError{
			Op:          "clear restored rollback journal",
			Err:         err,
			Safety:      safety,
			JournalPath: path,
		}
	}
	return nil
}

func appendRollbackEvent(repo *checkpoint.Repo, target ResolvedTarget, safety checkpoint.Snapshot, plan checkpoint.RestorePlan, sourceID string) (eventlog.Event, error) {
	payload, err := json.Marshal(EventPayload{
		Turn:            target.TurnID.Uint64(),
		Phase:           target.Phase.String(),
		Target:          target.Target.String(),
		Ref:             target.CheckpointRef.String(),
		CommitSHA:       target.Commit.String(),
		SafetyRef:       safety.Ref,
		SafetyCommitSHA: safety.Commit.String(),
		ChangeSummary:   summarizeChanges(plan.Changes),
	})
	if err != nil {
		return eventlog.Event{}, fmt.Errorf("marshal rollback event: %w", err)
	}
	return eventlog.Open(repo.MetadataDir).Append(eventlog.AppendInput{
		SessionID: target.SessionID,
		TurnID:    &target.TurnID,
		Type:      primitives.EventTypeRollback,
		SourceID:  sourceID,
		RawRef:    target.Target.String(),
		Payload:   payload,
	})
}

func rollbackEventExists(log eventlog.Log, target ResolvedTarget, safety checkpoint.Snapshot, sourceID string) (bool, error) {
	if sourceID != "" {
		exists, err := log.ContainsSourceID(target.SessionID, sourceID)
		if err != nil || exists {
			return exists, err
		}
	}

	events, err := log.Read(target.SessionID)
	if err != nil {
		return false, err
	}
	for _, event := range events {
		if event.Type != primitives.EventTypeRollback || event.RawRef != target.Target.String() {
			continue
		}
		var payload EventPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			continue
		}
		if payload.SafetyRef == safety.Ref && payload.SafetyCommitSHA == safety.Commit.String() {
			return true, nil
		}
	}
	return false, nil
}

func journalRollback(journal Journal) (ResolvedTarget, checkpoint.Snapshot, checkpoint.RestorePlan, string, error) {
	targetRef, err := primitives.ParseTargetRef(journal.Target)
	if err != nil {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.RestorePlan{}, "", fmt.Errorf("rollback journal target invariant failed: %w", err)
	}
	phase, ok := targetRef.Phase()
	if !ok {
		phase = primitives.CheckpointPhasePre
		targetRef, err = primitives.NewTargetRef(targetRef.SessionID(), targetRef.TurnID(), phase)
		if err != nil {
			return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.RestorePlan{}, "", err
		}
	}
	ref, err := primitives.ParseCheckpointRef(journal.CheckpointRef)
	if err != nil {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.RestorePlan{}, "", fmt.Errorf("rollback journal checkpoint ref invariant failed: %w", err)
	}
	commit, err := primitives.ParseCommitSHA(journal.TargetCommitSHA)
	if err != nil {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.RestorePlan{}, "", fmt.Errorf("rollback journal target commit invariant failed: %w", err)
	}
	safetyCommit, err := primitives.ParseCommitSHA(journal.SafetyCommitSHA)
	if err != nil {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.RestorePlan{}, "", fmt.Errorf("rollback journal safety commit invariant failed: %w", err)
	}
	if journal.SafetyRef == "" {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.RestorePlan{}, "", fmt.Errorf("rollback journal safety ref invariant failed: must not be empty")
	}

	target := ResolvedTarget{
		Target:        targetRef,
		CheckpointRef: ref,
		Commit:        commit,
		SessionID:     targetRef.SessionID(),
		TurnID:        targetRef.TurnID(),
		Phase:         phase,
	}
	safety := checkpoint.Snapshot{Ref: journal.SafetyRef, Commit: safetyCommit}
	plan := checkpoint.RestorePlan{TargetCommit: commit, Changes: journal.Changes}
	eventSourceID := journal.EventSourceID
	if eventSourceID == "" {
		eventSourceID = rollbackEventSourceID(target, safety)
	}
	return target, safety, plan, eventSourceID, nil
}

func summarizeChanges(changes []checkpoint.RestoreChange) ChangeSummary {
	summary := ChangeSummary{Total: len(changes)}
	for _, change := range changes {
		switch change.Action {
		case checkpoint.RestoreActionAdded:
			summary.Added++
		case checkpoint.RestoreActionModified:
			summary.Modified++
		case checkpoint.RestoreActionDeleted:
			summary.Deleted++
		case checkpoint.RestoreActionModeChanged:
			summary.ModeChanged++
		}
	}
	return summary
}

func (engine Engine) safetyError(op string, safety checkpoint.Snapshot, err error) error {
	return SafetyError{
		Op:          op,
		Err:         err,
		Safety:      safety,
		JournalPath: JournalPath(engine.Repo),
	}
}

func rollbackEventSourceID(target ResolvedTarget, safety checkpoint.Snapshot) string {
	return fmt.Sprintf("agent-vcs:rollback:%s:%s", target.Target, safety.Commit)
}

func readJournal(path string) (Journal, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Journal{}, false, nil
	}
	if err != nil {
		return Journal{}, false, fmt.Errorf("read rollback journal: %w", err)
	}
	var journal Journal
	if err := json.Unmarshal(data, &journal); err != nil {
		return Journal{}, false, fmt.Errorf("rollback invariant failed: unreadable rollback journal at %s: %w", path, err)
	}
	return journal, true, nil
}

func writeJournal(path string, journal Journal) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create rollback journal dir: %w", err)
	}
	journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal rollback journal: %w", err)
	}
	data = append(data, '\n')

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("write rollback journal: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("commit rollback journal: %w", err)
	}
	return nil
}

func safetyRef(target ResolvedTarget, now time.Time) string {
	return fmt.Sprintf(
		"refs/agent-vcs/rollback-safety/%s/turn/%s/%s/%d-%s",
		target.SessionID,
		target.TurnID.RefSegment(),
		target.Phase,
		now.UnixNano(),
		randomSuffix(),
	)
}

func randomSuffix() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%d", os.Getpid())
}
