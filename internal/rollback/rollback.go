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
	"agent-vcs-again/internal/gitsync"
	"agent-vcs-again/internal/primitives"
	"agent-vcs-again/internal/workspacegit"
)

const journalFileName = "rollback-journal.json"

type Engine struct {
	Repo *checkpoint.Repo
}

type Request struct {
	Target       primitives.TargetRef
	DryRun       bool
	WorkspaceGit bool
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
	Target     ResolvedTarget
	Mode       primitives.RollbackMode
	Plan       checkpoint.RestorePlan
	GitPlan    *workspacegit.RestorePlan
	GitSyncRef string
	Safety     *checkpoint.Snapshot
	GitSafety  *checkpoint.Snapshot
	Event      *eventlog.Event
	DryRun     bool
}

type ChangeSummary struct {
	Total       int `json:"total"`
	Added       int `json:"added"`
	Modified    int `json:"modified"`
	Deleted     int `json:"deleted"`
	ModeChanged int `json:"mode_changed"`
}

type EventPayload struct {
	Turn               uint64        `json:"turn"`
	Phase              string        `json:"phase"`
	Mode               string        `json:"mode,omitempty"`
	Target             string        `json:"target"`
	Ref                string        `json:"ref"`
	CommitSHA          string        `json:"commit_sha"`
	SafetyRef          string        `json:"safety_ref"`
	SafetyCommitSHA    string        `json:"safety_commit_sha"`
	GitSyncRef         string        `json:"git_sync_ref,omitempty"`
	GitSafetyRef       string        `json:"git_safety_ref,omitempty"`
	GitSafetyCommitSHA string        `json:"git_safety_commit_sha,omitempty"`
	ChangeSummary      ChangeSummary `json:"change_summary"`
}

type Journal struct {
	Version            int                        `json:"version"`
	State              string                     `json:"state"`
	RestorePhase       string                     `json:"restore_phase"`
	StartedAt          string                     `json:"started_at"`
	UpdatedAt          string                     `json:"updated_at"`
	Target             string                     `json:"target"`
	CheckpointRef      string                     `json:"checkpoint_ref"`
	TargetCommitSHA    string                     `json:"target_commit_sha"`
	Mode               string                     `json:"mode,omitempty"`
	GitSyncRef         string                     `json:"git_sync_ref,omitempty"`
	SafetyRef          string                     `json:"safety_ref"`
	SafetyCommitSHA    string                     `json:"safety_commit_sha"`
	GitSafetyRef       string                     `json:"git_safety_ref,omitempty"`
	GitSafetyCommitSHA string                     `json:"git_safety_commit_sha,omitempty"`
	EventSourceID      string                     `json:"event_source_id"`
	Changes            []checkpoint.RestoreChange `json:"changes"`
}

type ActiveJournalError struct {
	Path    string
	Journal Journal
}

func (err ActiveJournalError) Error() string {
	return fmt.Sprintf(
		"rollback invariant failed: active rollback journal exists at %s; previous rollback may be incomplete (state=%s target=%s safety_ref=%s safety_commit=%s)",
		err.Path,
		err.Journal.phase(),
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
	if request.WorkspaceGit {
		return engine.runWorkspaceGit(request)
	}
	return engine.runCheckpoint(request)
}

func (engine Engine) runCheckpoint(request Request) (Result, error) {
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

	result := Result{Target: target, Mode: primitives.RollbackModeCheckpoint, Plan: plan, DryRun: request.DryRun}
	if request.DryRun {
		return result, nil
	}

	now := time.Now().UTC()
	journal := Journal{
		Version:         1,
		State:           "intent",
		RestorePhase:    "intent",
		StartedAt:       now.Format(time.RFC3339Nano),
		Mode:            primitives.RollbackModeCheckpoint.String(),
		Target:          target.Target.String(),
		CheckpointRef:   target.CheckpointRef.String(),
		TargetCommitSHA: target.Commit.String(),
		Changes:         plan.Changes,
	}
	if err := writeJournal(JournalPath(engine.Repo), journal); err != nil {
		return result, fmt.Errorf("write rollback journal: %w", err)
	}

	safetyRef := safetyRef(target, time.Now().UTC())
	safety, err := engine.Repo.CreateSnapshotRef(safetyRef, fmt.Sprintf("agent-vcs rollback safety %s", target.Target))
	if err != nil {
		return result, err
	}
	result.Safety = &safety
	eventSourceID := rollbackEventSourceID(target, safety)

	journal = journal.withPhase("planned")
	journal.SafetyRef = safety.Ref
	journal.SafetyCommitSHA = safety.Commit.String()
	journal.EventSourceID = eventSourceID
	if err := writeJournal(JournalPath(engine.Repo), journal); err != nil {
		return result, engine.safetyError("write rollback journal", safety, err)
	}

	journal = journal.withPhase("restoring")
	if err := writeJournal(JournalPath(engine.Repo), journal); err != nil {
		return result, engine.safetyError("write rollback journal", safety, err)
	}
	if err := engine.Repo.RestoreCommit(target.Commit); err != nil {
		return result, engine.safetyError("restore checkpoint", safety, err)
	}

	journal = journal.withPhase("restored")
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

func (engine Engine) runWorkspaceGit(request Request) (Result, error) {
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
	gitSyncRef, err := gitsync.Ref(target.SessionID, target.TurnID, target.Phase)
	if err != nil {
		return Result{}, err
	}
	targetCapture, err := gitsync.Load(engine.Repo, gitSyncRef)
	if err != nil {
		return Result{}, fmt.Errorf("workspace-git rollback requires captured git-sync state for %s: %w", gitSyncRef, err)
	}
	workspace := workspacegit.Open(engine.Repo.WorkspaceRoot)
	gitPlan, err := workspace.PlanRestore(targetCapture)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		Target:     target,
		Mode:       primitives.RollbackModeWorkspaceGit,
		GitPlan:    &gitPlan,
		GitSyncRef: gitSyncRef.String(),
		DryRun:     request.DryRun,
	}
	if request.DryRun {
		return result, nil
	}

	now := time.Now().UTC()
	journal := Journal{
		Version:         1,
		State:           "intent",
		RestorePhase:    "intent",
		StartedAt:       now.Format(time.RFC3339Nano),
		Mode:            primitives.RollbackModeWorkspaceGit.String(),
		Target:          target.Target.String(),
		CheckpointRef:   target.CheckpointRef.String(),
		TargetCommitSHA: target.Commit.String(),
		GitSyncRef:      gitSyncRef.String(),
	}
	if err := writeJournal(JournalPath(engine.Repo), journal); err != nil {
		return result, fmt.Errorf("write rollback journal: %w", err)
	}

	safety, err := engine.Repo.CreateSnapshotRef(safetyRef(target, now), fmt.Sprintf("agent-vcs rollback safety %s", target.Target))
	if err != nil {
		return result, err
	}
	result.Safety = &safety

	currentCapture, err := workspace.Capture()
	if err != nil {
		return result, engine.safetyError("capture workspace git safety state", safety, err)
	}
	gitSafetyRef := gitSafetyRef(target, now)
	gitSafety, err := gitsync.SavePrivate(engine.Repo, gitSafetyRef, currentCapture, fmt.Sprintf("agent-vcs workspace git rollback safety %s", target.Target))
	if err != nil {
		return result, engine.safetyError("save workspace git safety state", safety, err)
	}
	result.GitSafety = &gitSafety

	eventSourceID := rollbackEventSourceIDForMode(target, safety, primitives.RollbackModeWorkspaceGit)
	journal = journal.withPhase("planned")
	journal.SafetyRef = safety.Ref
	journal.SafetyCommitSHA = safety.Commit.String()
	journal.GitSafetyRef = gitSafety.Ref
	journal.GitSafetyCommitSHA = gitSafety.Commit.String()
	journal.EventSourceID = eventSourceID
	if err := writeJournal(JournalPath(engine.Repo), journal); err != nil {
		return result, engine.safetyError("write rollback journal", safety, err)
	}

	journal = journal.withPhase("restoring")
	if err := writeJournal(JournalPath(engine.Repo), journal); err != nil {
		return result, engine.safetyError("write rollback journal", safety, err)
	}
	if err := workspace.Restore(targetCapture); err != nil {
		return result, engine.safetyError("restore workspace git state", safety, err)
	}

	journal = journal.withPhase("restored")
	if err := writeJournal(JournalPath(engine.Repo), journal); err != nil {
		return result, engine.safetyError("write rollback journal", safety, err)
	}

	event, err := appendWorkspaceGitRollbackEvent(engine.Repo, target, safety, gitSafety, gitSyncRef, gitPlan, eventSourceID)
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
		switch journal.phase() {
		case "intent", "planned":
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("clear pre-restore rollback journal: %w", err)
			}
			return nil
		case "restored":
			if journal.Mode == primitives.RollbackModeWorkspaceGit.String() {
				return engine.finalizeRestoredWorkspaceGitJournal(path, journal)
			}
			return engine.finalizeRestoredJournal(path, journal)
		case "restoring":
			return ActiveJournalError{Path: path, Journal: journal}
		default:
			return fmt.Errorf("rollback invariant failed: unknown rollback journal phase %q at %s", journal.phase(), path)
		}
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

func (engine Engine) finalizeRestoredWorkspaceGitJournal(path string, journal Journal) error {
	target, safety, gitSafety, gitSyncRef, eventSourceID, err := journalWorkspaceGitRollback(journal)
	if err != nil {
		return err
	}

	log := eventlog.Open(engine.Repo.MetadataDir)
	exists, err := rollbackEventExists(log, target, safety, eventSourceID)
	if err != nil {
		return fmt.Errorf("finalize restored workspace-git rollback journal: %w", err)
	}
	if !exists {
		if _, err := appendWorkspaceGitRollbackEvent(engine.Repo, target, safety, gitSafety, gitSyncRef, workspacegit.RestorePlan{}, eventSourceID); err != nil {
			return SafetyError{
				Op:          "finalize restored workspace-git rollback journal",
				Err:         err,
				Safety:      safety,
				JournalPath: path,
			}
		}
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return SafetyError{
			Op:          "clear restored workspace-git rollback journal",
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
		Mode:            primitives.RollbackModeCheckpoint.String(),
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

func appendWorkspaceGitRollbackEvent(repo *checkpoint.Repo, target ResolvedTarget, safety checkpoint.Snapshot, gitSafety checkpoint.Snapshot, gitSyncRef primitives.GitSyncRef, plan workspacegit.RestorePlan, sourceID string) (eventlog.Event, error) {
	payload, err := json.Marshal(EventPayload{
		Turn:               target.TurnID.Uint64(),
		Phase:              target.Phase.String(),
		Mode:               primitives.RollbackModeWorkspaceGit.String(),
		Target:             target.Target.String(),
		Ref:                target.CheckpointRef.String(),
		CommitSHA:          target.Commit.String(),
		SafetyRef:          safety.Ref,
		SafetyCommitSHA:    safety.Commit.String(),
		GitSyncRef:         gitSyncRef.String(),
		GitSafetyRef:       gitSafety.Ref,
		GitSafetyCommitSHA: gitSafety.Commit.String(),
		ChangeSummary: ChangeSummary{
			Total:    len(plan.StagedPaths) + len(plan.UnstagedPaths) + len(plan.Untracked),
			Modified: len(plan.StagedPaths) + len(plan.UnstagedPaths),
			Added:    len(plan.Untracked),
		},
	})
	if err != nil {
		return eventlog.Event{}, fmt.Errorf("marshal workspace-git rollback event: %w", err)
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

func journalWorkspaceGitRollback(journal Journal) (ResolvedTarget, checkpoint.Snapshot, checkpoint.Snapshot, primitives.GitSyncRef, string, error) {
	target, safety, _, eventSourceID, err := journalRollback(journal)
	if err != nil {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.Snapshot{}, "", "", err
	}
	gitSafetyCommit, err := primitives.ParseCommitSHA(journal.GitSafetyCommitSHA)
	if err != nil {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.Snapshot{}, "", "", fmt.Errorf("rollback journal git safety commit invariant failed: %w", err)
	}
	if journal.GitSafetyRef == "" {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.Snapshot{}, "", "", fmt.Errorf("rollback journal git safety ref invariant failed: must not be empty")
	}
	gitSyncRef, err := primitives.ParseGitSyncRef(journal.GitSyncRef)
	if err != nil {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.Snapshot{}, "", "", fmt.Errorf("rollback journal git-sync ref invariant failed: %w", err)
	}
	if eventSourceID == "" {
		eventSourceID = rollbackEventSourceIDForMode(target, safety, primitives.RollbackModeWorkspaceGit)
	}
	return target, safety, checkpoint.Snapshot{Ref: journal.GitSafetyRef, Commit: gitSafetyCommit}, gitSyncRef, eventSourceID, nil
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
	return rollbackEventSourceIDForMode(target, safety, primitives.RollbackModeCheckpoint)
}

func rollbackEventSourceIDForMode(target ResolvedTarget, safety checkpoint.Snapshot, mode primitives.RollbackMode) string {
	return fmt.Sprintf("agent-vcs:rollback:%s:%s:%s", mode, target.Target, safety.Commit)
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

func (journal Journal) phase() string {
	if journal.RestorePhase != "" {
		return journal.RestorePhase
	}
	return journal.State
}

func (journal Journal) withPhase(phase string) Journal {
	journal.State = phase
	journal.RestorePhase = phase
	return journal
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

func gitSafetyRef(target ResolvedTarget, now time.Time) string {
	return fmt.Sprintf(
		"refs/agent-vcs/git-sync-safety/rollback/%s/turn/%s/%s/%d-%s",
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
