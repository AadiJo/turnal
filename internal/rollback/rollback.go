package rollback

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/gitsync"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/workspacegit"
)

const (
	journalFileName = "rollback-journal.json"

	// DefaultTargetPhase is used when rollback callers identify a turn without
	// saying whether they want the pre or post checkpoint.
	DefaultTargetPhase primitives.CheckpointPhase = primitives.CheckpointPhasePost
)

type Engine struct {
	Repo *checkpoint.Repo
}

type Request struct {
	Target       primitives.TargetRef
	Resolved     *ResolvedTarget
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
	GitChanges         *WorkspaceGitChanges       `json:"git_changes,omitempty"`
}

type WorkspaceGitChanges struct {
	StagedPaths   []primitives.RepoPath `json:"staged_paths,omitempty"`
	UnstagedPaths []primitives.RepoPath `json:"unstaged_paths,omitempty"`
	Untracked     []primitives.RepoPath `json:"untracked,omitempty"`
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

func InspectJournal(repo *checkpoint.Repo) []string {
	if repo == nil {
		return nil
	}
	path := JournalPath(repo)
	journal, ok, err := readJournal(path)
	if err != nil {
		return []string{err.Error()}
	}
	if !ok {
		return nil
	}
	switch journal.phase() {
	case "intent", "planned":
		return []string{fmt.Sprintf("rollback journal pending before restore at %s: target=%s phase=%s", path, journal.Target, journal.phase())}
	case "restoring":
		return []string{fmt.Sprintf("rollback journal indicates restore may be incomplete at %s: target=%s safety_ref=%s safety_commit=%s", path, journal.Target, journal.SafetyRef, journal.SafetyCommitSHA)}
	case "restored":
		return []string{fmt.Sprintf("rollback journal restored but not finalized at %s: target=%s safety_ref=%s safety_commit=%s", path, journal.Target, journal.SafetyRef, journal.SafetyCommitSHA)}
	default:
		return []string{fmt.Sprintf("rollback invariant failed: unknown rollback journal phase %q at %s", journal.phase(), path)}
	}
}

func RecoveryStatus(repo *checkpoint.Repo) (Journal, bool, error) {
	if repo == nil {
		return Journal{}, false, fmt.Errorf("recovery repo is required")
	}
	return readJournal(JournalPath(repo))
}

// ResumeRecovery explicitly reapplies the recorded target and finalizes the
// rollback. Reapplying is intentional because a crash in the restoring phase
// leaves the exact progress ambiguous.
func (engine Engine) ResumeRecovery() error {
	if engine.Repo == nil {
		return fmt.Errorf("recovery repo is required")
	}
	return engine.Repo.WithWorkspaceLock("resume rollback recovery", func() error {
		path := JournalPath(engine.Repo)
		journal, ok, err := readJournal(path)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no rollback recovery journal exists")
		}
		switch journal.phase() {
		case "intent":
			return clearJournal(path)
		case "planned", "restoring":
			if journal.Mode != primitives.RollbackModeWorkspaceGit.String() {
				commit, err := primitives.ParseCommitSHA(journal.TargetCommitSHA)
				if err != nil {
					return fmt.Errorf("rollback recovery target commit: %w", err)
				}
				if err := engine.Repo.PreflightRestoreCommit(commit); err != nil {
					return fmt.Errorf("preflight resumed restore: %w", err)
				}
			} else {
				gitSyncRef, err := primitives.ParseGitSyncRef(journal.GitSyncRef)
				if err != nil {
					return fmt.Errorf("rollback recovery git-sync ref: %w", err)
				}
				targetCapture, err := gitsync.Load(engine.Repo, gitSyncRef)
				if err != nil {
					return fmt.Errorf("load rollback recovery workspace state: %w", err)
				}
				if err := workspacegit.Open(engine.Repo.WorkspaceRoot).PreflightRestore(targetCapture); err != nil {
					return fmt.Errorf("preflight resumed workspace-Git restore: %w", err)
				}
			}
			journal = journal.withPhase("restoring")
			if err := writeJournal(path, journal); err != nil {
				return err
			}
			if journal.Mode == primitives.RollbackModeWorkspaceGit.String() {
				gitSyncRef, err := primitives.ParseGitSyncRef(journal.GitSyncRef)
				if err != nil {
					return fmt.Errorf("rollback recovery git-sync ref: %w", err)
				}
				targetCapture, err := gitsync.Load(engine.Repo, gitSyncRef)
				if err != nil {
					return fmt.Errorf("load rollback recovery workspace state: %w", err)
				}
				if err := workspacegit.Open(engine.Repo.WorkspaceRoot).Restore(targetCapture); err != nil {
					return fmt.Errorf("resume workspace-git restore: %w", err)
				}
			} else {
				commit, err := primitives.ParseCommitSHA(journal.TargetCommitSHA)
				if err != nil {
					return fmt.Errorf("rollback recovery target commit: %w", err)
				}
				if err := engine.Repo.RestoreCommit(commit); err != nil {
					return fmt.Errorf("resume checkpoint restore: %w", err)
				}
			}
			journal = journal.withPhase("restored")
			if err := writeJournal(path, journal); err != nil {
				return err
			}
			fallthrough
		case "restored":
			if journal.Mode == primitives.RollbackModeWorkspaceGit.String() {
				return engine.finalizeRestoredWorkspaceGitJournal(path, journal)
			}
			return engine.finalizeRestoredJournal(path, journal)
		default:
			return fmt.Errorf("rollback invariant failed: unknown rollback journal phase %q at %s", journal.phase(), path)
		}
	})
}

// RestoreSafety abandons the target restore and restores the safety snapshot
// captured immediately before rollback began.
func (engine Engine) RestoreSafety() error {
	if engine.Repo == nil {
		return fmt.Errorf("recovery repo is required")
	}
	return engine.Repo.WithWorkspaceLock("restore rollback safety", func() error {
		path := JournalPath(engine.Repo)
		journal, ok, err := readJournal(path)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no rollback recovery journal exists")
		}
		if journal.SafetyRef == "" || journal.SafetyCommitSHA == "" {
			return fmt.Errorf("rollback journal has no safety snapshot; target restore had not started")
		}
		if journal.Mode == primitives.RollbackModeWorkspaceGit.String() {
			if journal.GitSafetyRef == "" {
				return fmt.Errorf("rollback journal has no workspace-Git safety snapshot")
			}
			capture, err := gitsync.LoadPrivate(engine.Repo, journal.GitSafetyRef)
			if err != nil {
				return fmt.Errorf("load workspace-Git safety snapshot: %w", err)
			}
			if err := workspacegit.Open(engine.Repo.WorkspaceRoot).Restore(capture); err != nil {
				return fmt.Errorf("restore workspace-Git safety snapshot: %w", err)
			}
		} else {
			commit, err := primitives.ParseCommitSHA(journal.SafetyCommitSHA)
			if err != nil {
				return fmt.Errorf("rollback safety commit: %w", err)
			}
			refCommit, err := engine.Repo.RefCommit(journal.SafetyRef)
			if err != nil {
				return fmt.Errorf("resolve rollback safety ref: %w", err)
			}
			if refCommit != commit {
				return fmt.Errorf("rollback safety ref points to %s, journal records %s", refCommit, commit)
			}
			if err := engine.Repo.RestoreCommit(commit); err != nil {
				return fmt.Errorf("restore checkpoint safety snapshot: %w", err)
			}
		}
		if err := clearJournal(path); err != nil {
			return fmt.Errorf("clear rollback recovery journal: %w", err)
		}
		return nil
	})
}

func ResolveTarget(repo *checkpoint.Repo, target primitives.TargetRef) (ResolvedTarget, error) {
	phase, ok := target.Phase()
	if !ok {
		phase = DefaultTargetPhase
		var err error
		target, err = primitives.NewTargetRef(target.SessionID(), target.TurnID(), phase)
		if err != nil {
			return ResolvedTarget{}, err
		}
	}

	ref, err := repo.CheckpointRefFor(target.SessionID(), target.TurnID(), phase)
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

func ResolveCheckpointInfo(target primitives.TargetRef, info checkpoint.CheckpointRefInfo) (ResolvedTarget, error) {
	phase, ok := target.Phase()
	if !ok {
		phase = info.Phase
	}
	if info.SessionID != target.SessionID() || info.TurnID != target.TurnID() || info.Phase != phase {
		return ResolvedTarget{}, fmt.Errorf("checkpoint selection invariant failed: target=%s ref=%s", target, info.Ref)
	}
	return ResolvedTarget{
		Target:        target,
		CheckpointRef: info.Ref,
		Commit:        info.Commit,
		SessionID:     info.SessionID,
		TurnID:        info.TurnID,
		Phase:         info.Phase,
	}, nil
}

func (engine Engine) resolveRequestTarget(request Request) (ResolvedTarget, error) {
	if request.Resolved != nil {
		return *request.Resolved, nil
	}
	return ResolveTarget(engine.Repo, request.Target)
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
	var result Result
	err := engine.Repo.WithWorkspaceLock("rollback", func() error {
		var err error
		result, err = engine.runCheckpointUnlocked(request)
		return err
	})
	return result, err
}

func (engine Engine) runCheckpointUnlocked(request Request) (Result, error) {
	if err := engine.ensureNoActiveJournal(); err != nil {
		return Result{}, err
	}

	target, err := engine.resolveRequestTarget(request)
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
	safety, err := engine.Repo.CreateSnapshotRef(safetyRef, fmt.Sprintf("turnal rollback safety %s", target.Target))
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

	if err := engine.Repo.PreflightRestoreCommit(target.Commit); err != nil {
		return result, engine.safetyError("preflight checkpoint restore", safety, err)
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

	if err := clearJournal(JournalPath(engine.Repo)); err != nil {
		return result, engine.safetyError("clear rollback journal", safety, err)
	}
	return result, nil
}

func (engine Engine) runWorkspaceGit(request Request) (Result, error) {
	if engine.Repo == nil {
		return Result{}, fmt.Errorf("rollback repo is required")
	}
	var result Result
	err := engine.Repo.WithWorkspaceLock("workspace-git rollback", func() error {
		var err error
		result, err = engine.runWorkspaceGitUnlocked(request)
		return err
	})
	return result, err
}

func (engine Engine) runWorkspaceGitUnlocked(request Request) (Result, error) {
	if err := engine.ensureNoActiveJournal(); err != nil {
		return Result{}, err
	}

	target, err := engine.resolveRequestTarget(request)
	if err != nil {
		return Result{}, err
	}
	gitSyncRef, err := engine.Repo.GitSyncRefFor(target.SessionID, target.TurnID, target.Phase)
	if err != nil {
		return Result{}, err
	}
	targetCapture, err := gitsync.LoadPrivate(engine.Repo, gitSyncRef)
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
		GitSyncRef: gitSyncRef,
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
		GitSyncRef:      gitSyncRef,
		GitChanges:      workspaceGitChangesFromPlan(gitPlan),
	}
	if err := writeJournal(JournalPath(engine.Repo), journal); err != nil {
		return result, fmt.Errorf("write rollback journal: %w", err)
	}

	safety, err := engine.Repo.CreateSnapshotRef(safetyRef(target, now), fmt.Sprintf("turnal rollback safety %s", target.Target))
	if err != nil {
		return result, err
	}
	result.Safety = &safety

	currentCapture, err := workspace.Capture()
	if err != nil {
		return result, engine.safetyError("capture workspace git safety state", safety, err)
	}
	gitSafetyRef := gitSafetyRef(target, now)
	gitSafety, err := gitsync.SavePrivate(engine.Repo, gitSafetyRef, currentCapture, fmt.Sprintf("turnal workspace git rollback safety %s", target.Target))
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

	if err := workspace.PreflightRestore(targetCapture); err != nil {
		return result, engine.safetyError("preflight workspace-Git restore", safety, err)
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

	if err := clearJournal(JournalPath(engine.Repo)); err != nil {
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
		case "intent":
			if err := clearJournal(path); err != nil {
				return fmt.Errorf("clear pre-restore rollback journal: %w", err)
			}
			return nil
		case "planned":
			return ActiveJournalError{Path: path, Journal: journal}
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

	log := engine.Repo.EventLog()
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

	if err := clearJournal(path); err != nil {
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
	target, safety, gitSafety, gitSyncRef, gitPlan, eventSourceID, err := journalWorkspaceGitRollback(journal)
	if err != nil {
		return err
	}

	log := engine.Repo.EventLog()
	exists, err := rollbackEventExists(log, target, safety, eventSourceID)
	if err != nil {
		return fmt.Errorf("finalize restored workspace-git rollback journal: %w", err)
	}
	if !exists {
		if _, err := appendWorkspaceGitRollbackEvent(engine.Repo, target, safety, gitSafety, gitSyncRef, gitPlan, eventSourceID); err != nil {
			return SafetyError{
				Op:          "finalize restored workspace-git rollback journal",
				Err:         err,
				Safety:      safety,
				JournalPath: path,
			}
		}
	}

	if err := clearJournal(path); err != nil {
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
	return repo.EventLog().Append(eventlog.AppendInput{
		SessionID: target.SessionID,
		TurnID:    &target.TurnID,
		Type:      primitives.EventTypeRollback,
		SourceID:  sourceID,
		RawRef:    target.Target.String(),
		Payload:   payload,
	})
}

func appendWorkspaceGitRollbackEvent(repo *checkpoint.Repo, target ResolvedTarget, safety checkpoint.Snapshot, gitSafety checkpoint.Snapshot, gitSyncRef string, plan workspacegit.RestorePlan, sourceID string) (eventlog.Event, error) {
	payload, err := json.Marshal(EventPayload{
		Turn:               target.TurnID.Uint64(),
		Phase:              target.Phase.String(),
		Mode:               primitives.RollbackModeWorkspaceGit.String(),
		Target:             target.Target.String(),
		Ref:                target.CheckpointRef.String(),
		CommitSHA:          target.Commit.String(),
		SafetyRef:          safety.Ref,
		SafetyCommitSHA:    safety.Commit.String(),
		GitSyncRef:         gitSyncRef,
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
	return repo.EventLog().Append(eventlog.AppendInput{
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
		phase = DefaultTargetPhase
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

func journalWorkspaceGitRollback(journal Journal) (ResolvedTarget, checkpoint.Snapshot, checkpoint.Snapshot, string, workspacegit.RestorePlan, string, error) {
	target, safety, _, eventSourceID, err := journalRollback(journal)
	if err != nil {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.Snapshot{}, "", workspacegit.RestorePlan{}, "", err
	}
	gitSafetyCommit, err := primitives.ParseCommitSHA(journal.GitSafetyCommitSHA)
	if err != nil {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.Snapshot{}, "", workspacegit.RestorePlan{}, "", fmt.Errorf("rollback journal git safety commit invariant failed: %w", err)
	}
	if journal.GitSafetyRef == "" {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.Snapshot{}, "", workspacegit.RestorePlan{}, "", fmt.Errorf("rollback journal git safety ref invariant failed: must not be empty")
	}
	gitSyncRef := strings.TrimSpace(journal.GitSyncRef)
	if gitSyncRef == "" || (gitSyncRef != "refs/agent-vcs" && !strings.HasPrefix(gitSyncRef, "refs/agent-vcs/")) {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.Snapshot{}, "", workspacegit.RestorePlan{}, "", fmt.Errorf("rollback journal git-sync ref invariant failed: invalid private ref %q", journal.GitSyncRef)
	}
	if eventSourceID == "" {
		eventSourceID = rollbackEventSourceIDForMode(target, safety, primitives.RollbackModeWorkspaceGit)
	}
	gitPlan := workspacegit.RestorePlan{}
	if journal.GitChanges != nil {
		gitPlan = journal.GitChanges.restorePlan()
	}
	return target, safety, checkpoint.Snapshot{Ref: journal.GitSafetyRef, Commit: gitSafetyCommit}, gitSyncRef, gitPlan, eventSourceID, nil
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

func workspaceGitChangesFromPlan(plan workspacegit.RestorePlan) *WorkspaceGitChanges {
	return &WorkspaceGitChanges{
		StagedPaths:   append([]primitives.RepoPath(nil), plan.StagedPaths...),
		UnstagedPaths: append([]primitives.RepoPath(nil), plan.UnstagedPaths...),
		Untracked:     append([]primitives.RepoPath(nil), plan.Untracked...),
	}
}

func (changes WorkspaceGitChanges) restorePlan() workspacegit.RestorePlan {
	return workspacegit.RestorePlan{
		StagedPaths:   append([]primitives.RepoPath(nil), changes.StagedPaths...),
		UnstagedPaths: append([]primitives.RepoPath(nil), changes.UnstagedPaths...),
		Untracked:     append([]primitives.RepoPath(nil), changes.Untracked...),
	}
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
	return fmt.Sprintf("turnal:rollback:%s:%s:%s", mode, target.Target, safety.Commit)
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
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create rollback journal dir: %w", err)
	}
	journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal rollback journal: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".rollback-journal-*")
	if err != nil {
		return fmt.Errorf("create rollback journal: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure rollback journal: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write rollback journal: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync rollback journal: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close rollback journal: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("commit rollback journal: %w", err)
	}
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("sync rollback journal dir: %w", err)
	}
	return nil
}

func clearJournal(path string) error {
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return syncDirectory(filepath.Dir(path))
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
