package rollback

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/gitsync"
	"github.com/AadiJo/turnal/internal/manualcheckpoints"
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
	Target                primitives.TargetRef
	Resolved              *ResolvedTarget
	DryRun                bool
	WorkspaceGit          bool
	ExpectedWorkspaceTree string
	Application           *ApplicationMetadata
}

type ApplicationMetadata struct {
	CaseID     primitives.CaseID    `json:"case_id"`
	AttemptID  primitives.AttemptID `json:"attempt_id"`
	PostCommit primitives.CommitSHA `json:"post_commit"`
}

type ResolvedTarget struct {
	Target        primitives.TargetRef
	CheckpointRef primitives.CheckpointRef
	Commit        primitives.CommitSHA
	SessionID     primitives.SessionID
	TurnID        primitives.TurnID
	Phase         primitives.CheckpointPhase
	Manual        bool
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
	Turn               uint64               `json:"turn,omitempty"`
	Phase              string               `json:"phase,omitempty"`
	Mode               string               `json:"mode,omitempty"`
	Target             string               `json:"target"`
	Ref                string               `json:"ref"`
	CommitSHA          string               `json:"commit_sha"`
	SafetyRef          string               `json:"safety_ref"`
	SafetyCommitSHA    string               `json:"safety_commit_sha"`
	GitSyncRef         string               `json:"git_sync_ref,omitempty"`
	GitSafetyRef       string               `json:"git_safety_ref,omitempty"`
	GitSafetyCommitSHA string               `json:"git_safety_commit_sha,omitempty"`
	ChangeSummary      ChangeSummary        `json:"change_summary"`
	CaseApplication    *ApplicationMetadata `json:"case_application,omitempty"`
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
	Manual             bool                       `json:"manual,omitempty"`
	Mode               string                     `json:"mode,omitempty"`
	GitSyncRef         string                     `json:"git_sync_ref,omitempty"`
	SafetyRef          string                     `json:"safety_ref"`
	SafetyCommitSHA    string                     `json:"safety_commit_sha"`
	GitSafetyRef       string                     `json:"git_safety_ref,omitempty"`
	GitSafetyCommitSHA string                     `json:"git_safety_commit_sha,omitempty"`
	EventSourceID      string                     `json:"event_source_id"`
	Changes            []checkpoint.RestoreChange `json:"changes"`
	GitChanges         *WorkspaceGitChanges       `json:"git_changes,omitempty"`
	CaseApplication    *ApplicationMetadata       `json:"case_application,omitempty"`
	RepoID             primitives.RepoID          `json:"repo_id,omitempty"`
	StoreID            primitives.StoreID         `json:"store_id,omitempty"`
	WorktreeID         primitives.WorktreeID      `json:"worktree_id,omitempty"`
	WorkspaceRoot      string                     `json:"workspace_root,omitempty"`
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
	return filepath.Join(repo.TmpDir, "rollback-journal-"+repo.WorktreeID.String()+".json")
}

func legacyJournalPath(repo *checkpoint.Repo) string {
	return filepath.Join(repo.TmpDir, journalFileName)
}

func rejectLegacyJournal(repo *checkpoint.Repo) error {
	if _, err := os.Stat(legacyJournalPath(repo)); err == nil {
		return fmt.Errorf("legacy unscoped rollback journal requires manual inspection at %s; refusing to guess its worktree owner", legacyJournalPath(repo))
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func InspectJournal(repo *checkpoint.Repo) []string {
	if repo == nil {
		return nil
	}
	if err := rejectLegacyJournal(repo); err != nil {
		return []string{err.Error()}
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
	if err := rejectLegacyJournal(repo); err != nil {
		return Journal{}, false, err
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
	if err := rejectLegacyJournal(engine.Repo); err != nil {
		return err
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
		if err := validateJournalOwner(engine.Repo, journal); err != nil {
			return err
		}
		switch journal.phase() {
		case "intent":
			return clearJournal(path)
		case "planned", "restoring":
			target, _, _, _, err := journalRollback(engine.Repo, journal)
			if err != nil {
				return err
			}
			if journal.Mode != primitives.RollbackModeWorkspaceGit.String() {
				if err := engine.Repo.PreflightRestoreCommit(target.Commit); err != nil {
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
				if err := engine.Repo.RestoreCommit(target.Commit); err != nil {
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
	if err := rejectLegacyJournal(engine.Repo); err != nil {
		return err
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
		if err := validateJournalOwner(engine.Repo, journal); err != nil {
			return err
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

func ResolveManualCheckpointInfo(info checkpoint.CheckpointRefInfo) (ResolvedTarget, error) {
	parts, err := info.Ref.Parts()
	if err != nil {
		return ResolvedTarget{}, err
	}
	if !info.Manual || !parts.Manual || info.Commit == "" || info.WorktreeID == "" {
		return ResolvedTarget{}, fmt.Errorf("manual checkpoint selection invariant failed: ref=%s", info.Ref)
	}
	return ResolvedTarget{
		CheckpointRef: info.Ref,
		Commit:        info.Commit,
		Manual:        true,
	}, nil
}

func (target ResolvedTarget) selector() string {
	if target.Manual {
		return target.Commit.String()
	}
	return target.Target.String()
}

func (target ResolvedTarget) Selector() string { return target.selector() }

func (engine Engine) resolveRequestTarget(request Request) (ResolvedTarget, error) {
	if request.Resolved != nil {
		return *request.Resolved, nil
	}
	return ResolveTarget(engine.Repo, request.Target)
}

func (engine Engine) Run(request Request) (Result, error) {
	if request.WorkspaceGit {
		if request.Resolved != nil && request.Resolved.Manual {
			return Result{}, fmt.Errorf("workspace-git rollback is unavailable for manual checkpoints because turnal save does not capture the user's Git HEAD or index")
		}
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
	if err := validateApplicationMetadata(request.Application, target); err != nil {
		return Result{}, err
	}
	plan, err := engine.Repo.PlanRestoreCommit(target.Commit)
	if err != nil {
		return Result{}, err
	}
	if request.ExpectedWorkspaceTree != "" && plan.WorkspaceTree != request.ExpectedWorkspaceTree {
		return Result{}, fmt.Errorf("workspace changed since rollback preview; review the updated plan and try again")
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
		Target:          target.selector(),
		CheckpointRef:   target.CheckpointRef.String(),
		TargetCommitSHA: target.Commit.String(),
		Manual:          target.Manual,
		Changes:         plan.Changes,
		CaseApplication: cloneApplicationMetadata(request.Application),
		RepoID:          engine.Repo.RepoID, StoreID: engine.Repo.StoreID, WorktreeID: engine.Repo.WorktreeID, WorkspaceRoot: engine.Repo.WorkspaceRoot.String(),
	}
	if err := writeJournal(JournalPath(engine.Repo), journal); err != nil {
		return result, fmt.Errorf("write rollback journal: %w", err)
	}

	safetyRef := safetyRef(target, time.Now().UTC())
	safety, err := engine.Repo.CreateSnapshotRef(safetyRef, fmt.Sprintf("turnal rollback safety %s", target.selector()))
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

	event, err := appendRollbackEvent(engine.Repo, target, safety, plan, eventSourceID, request.Application)
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
		RepoID:          engine.Repo.RepoID, StoreID: engine.Repo.StoreID, WorktreeID: engine.Repo.WorktreeID, WorkspaceRoot: engine.Repo.WorkspaceRoot.String(),
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
	if err := rejectLegacyJournal(engine.Repo); err != nil {
		return err
	}
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
	target, safety, plan, eventSourceID, err := journalRollback(engine.Repo, journal)
	if err != nil {
		return err
	}

	log := engine.Repo.EventLog()
	exists, err := rollbackEventExists(log, target, safety, eventSourceID, journal.CaseApplication)
	if err != nil {
		return fmt.Errorf("finalize restored rollback journal: %w", err)
	}
	if !exists {
		if _, err := appendRollbackEvent(engine.Repo, target, safety, plan, eventSourceID, journal.CaseApplication); err != nil {
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
	target, safety, gitSafety, gitSyncRef, gitPlan, eventSourceID, err := journalWorkspaceGitRollback(engine.Repo, journal)
	if err != nil {
		return err
	}

	log := engine.Repo.EventLog()
	exists, err := rollbackEventExists(log, target, safety, eventSourceID, nil)
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

func appendRollbackEvent(repo *checkpoint.Repo, target ResolvedTarget, safety checkpoint.Snapshot, plan checkpoint.RestorePlan, sourceID string, application *ApplicationMetadata) (eventlog.Event, error) {
	payload, err := json.Marshal(EventPayload{
		Turn:            target.TurnID.Uint64(),
		Phase:           target.Phase.String(),
		Mode:            primitives.RollbackModeCheckpoint.String(),
		Target:          target.selector(),
		Ref:             target.CheckpointRef.String(),
		CommitSHA:       target.Commit.String(),
		SafetyRef:       safety.Ref,
		SafetyCommitSHA: safety.Commit.String(),
		ChangeSummary:   summarizeChanges(plan.Changes),
		CaseApplication: cloneApplicationMetadata(application),
	})
	if err != nil {
		return eventlog.Event{}, fmt.Errorf("marshal rollback event: %w", err)
	}
	if target.Manual {
		event, err := manualcheckpoints.AppendEvent(repo, primitives.EventTypeRollback, sourceID, target.selector(), payload)
		if err != nil {
			return eventlog.Event{}, err
		}
		var recorded EventPayload
		if event.Type != primitives.EventTypeRollback || event.RawRef != target.selector() || json.Unmarshal(event.Payload, &recorded) != nil ||
			recorded.Target != target.selector() || recorded.Ref != target.CheckpointRef.String() || recorded.CommitSHA != target.Commit.String() ||
			recorded.SafetyRef != safety.Ref || recorded.SafetyCommitSHA != safety.Commit.String() {
			return eventlog.Event{}, fmt.Errorf("manual rollback event source collision for %s", sourceID)
		}
		return event, nil
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

func rollbackEventExists(log eventlog.Log, target ResolvedTarget, safety checkpoint.Snapshot, sourceID string, application *ApplicationMetadata) (bool, error) {
	if target.Manual {
		// Workspace events are idempotent by source id in AppendEvent. Recovery
		// can safely append again and receive the existing durable event.
		return false, nil
	}
	sourceExists := false
	if sourceID != "" {
		exists, err := log.ContainsSourceID(target.SessionID, sourceID)
		if err != nil {
			return false, err
		}
		sourceExists = exists
	}

	events, err := log.Read(target.SessionID)
	if err != nil {
		return false, err
	}
	for _, event := range events {
		if event.Type != primitives.EventTypeRollback || event.RawRef != target.selector() {
			continue
		}
		var payload EventPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			continue
		}
		if payload.Target == target.selector() && payload.Ref == target.CheckpointRef.String() && payload.CommitSHA == target.Commit.String() && payload.SafetyRef == safety.Ref && payload.SafetyCommitSHA == safety.Commit.String() && reflect.DeepEqual(payload.CaseApplication, application) {
			return true, nil
		}
		if sourceID != "" && event.SourceID == sourceID {
			return false, fmt.Errorf("rollback event source collision for %s: durable payload does not match journal", sourceID)
		}
	}
	if sourceExists {
		return false, fmt.Errorf("rollback event source collision for %s: durable event was not found", sourceID)
	}
	return false, nil
}

func journalRollback(repo *checkpoint.Repo, journal Journal) (ResolvedTarget, checkpoint.Snapshot, checkpoint.RestorePlan, string, error) {
	if repo == nil {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.RestorePlan{}, "", fmt.Errorf("rollback journal repo invariant failed: repo is required")
	}
	if err := validateJournalOwner(repo, journal); err != nil {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.RestorePlan{}, "", err
	}
	ref, err := primitives.ParseCheckpointRef(journal.CheckpointRef)
	if err != nil {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.RestorePlan{}, "", fmt.Errorf("rollback journal checkpoint ref invariant failed: %w", err)
	}
	commit, err := primitives.ParseCommitSHA(journal.TargetCommitSHA)
	if err != nil {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.RestorePlan{}, "", fmt.Errorf("rollback journal target commit invariant failed: %w", err)
	}
	refCommit, err := repo.CheckpointCommit(ref)
	if err != nil {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.RestorePlan{}, "", fmt.Errorf("rollback journal checkpoint ref invariant failed: resolve %s: %w", ref, err)
	}
	if refCommit != commit {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.RestorePlan{}, "", fmt.Errorf("rollback journal checkpoint ref invariant failed: ref %s points to %s, target commit is %s", ref, refCommit, commit)
	}
	safetyCommit, err := primitives.ParseCommitSHA(journal.SafetyCommitSHA)
	if err != nil {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.RestorePlan{}, "", fmt.Errorf("rollback journal safety commit invariant failed: %w", err)
	}
	if journal.SafetyRef == "" {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.RestorePlan{}, "", fmt.Errorf("rollback journal safety ref invariant failed: must not be empty")
	}
	safetyRefCommit, err := repo.RefCommit(journal.SafetyRef)
	if err != nil {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.RestorePlan{}, "", fmt.Errorf("rollback journal safety ref invariant failed: %w", err)
	}
	if safetyRefCommit != safetyCommit {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.RestorePlan{}, "", fmt.Errorf("rollback journal safety ref invariant failed: %s points to %s, recorded %s", journal.SafetyRef, safetyRefCommit, safetyCommit)
	}

	var target ResolvedTarget
	if journal.Manual {
		parts, partsErr := ref.Parts()
		if partsErr != nil || !parts.Manual || parts.WorktreeID != journal.WorktreeID {
			return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.RestorePlan{}, "", fmt.Errorf("rollback journal manual checkpoint ref invariant failed: %s", ref)
		}
		selector, selectorErr := primitives.ParseCommitSHA(journal.Target)
		if selectorErr != nil || selector != commit {
			return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.RestorePlan{}, "", fmt.Errorf("rollback journal manual target invariant failed: target must equal target commit")
		}
		target = ResolvedTarget{CheckpointRef: ref, Commit: commit, Manual: true}
	} else {
		targetRef, targetErr := primitives.ParseTargetRef(journal.Target)
		if targetErr != nil {
			return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.RestorePlan{}, "", fmt.Errorf("rollback journal target invariant failed: %w", targetErr)
		}
		phase, ok := targetRef.Phase()
		if !ok {
			phase = DefaultTargetPhase
			targetRef, targetErr = primitives.NewTargetRef(targetRef.SessionID(), targetRef.TurnID(), phase)
			if targetErr != nil {
				return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.RestorePlan{}, "", targetErr
			}
		}
		target = ResolvedTarget{
			Target: targetRef, CheckpointRef: ref, Commit: commit,
			SessionID: targetRef.SessionID(), TurnID: targetRef.TurnID(), Phase: phase,
		}
		parts, partsErr := ref.Parts()
		if partsErr != nil || parts.Manual || parts.Canonical || parts.SessionID != target.SessionID || parts.TurnID != target.TurnID || parts.Phase != target.Phase || (parts.Scoped && parts.WorktreeID != journal.WorktreeID) {
			return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.RestorePlan{}, "", fmt.Errorf("rollback journal target/checkpoint ref invariant failed: target %s does not identify %s", targetRef, ref)
		}
	}
	if err := validateApplicationMetadata(journal.CaseApplication, target); err != nil {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.RestorePlan{}, "", fmt.Errorf("rollback journal Case application invariant failed: %w", err)
	}
	safety := checkpoint.Snapshot{Ref: journal.SafetyRef, Commit: safetyCommit}
	plan := checkpoint.RestorePlan{TargetCommit: commit, Changes: journal.Changes}
	eventSourceID := journal.EventSourceID
	if eventSourceID == "" {
		eventSourceID = rollbackEventSourceID(target, safety)
	}
	return target, safety, plan, eventSourceID, nil
}

// ValidateJournal checks that a pending journal is internally consistent and
// still points at every private ref recovery will need. Callers inspecting a
// journal owned by another linked worktree must provide a Repo carrying that
// journal's validated worktree and workspace identities.
func ValidateJournal(repo *checkpoint.Repo, journal Journal) (ResolvedTarget, error) {
	switch journal.Mode {
	case primitives.RollbackModeCheckpoint.String():
		target, _, _, _, err := journalRollback(repo, journal)
		return target, err
	case primitives.RollbackModeWorkspaceGit.String():
		if journal.Manual {
			return ResolvedTarget{}, fmt.Errorf("rollback journal mode invariant failed: manual checkpoints cannot use workspace-git mode")
		}
		target, _, _, _, _, _, err := journalWorkspaceGitRollback(repo, journal)
		return target, err
	default:
		return ResolvedTarget{}, fmt.Errorf("rollback journal mode invariant failed: unsupported mode %q", journal.Mode)
	}
}

func validateJournalOwner(repo *checkpoint.Repo, journal Journal) error {
	if journal.Version != 1 {
		return fmt.Errorf("rollback journal owner invariant failed: unsupported version %d", journal.Version)
	}
	if journal.RepoID == "" || journal.StoreID == "" || journal.WorktreeID == "" || journal.WorkspaceRoot == "" {
		return fmt.Errorf("rollback journal owner invariant failed: complete repository, store, worktree, and workspace identity is required")
	}
	if journal.RepoID != repo.RepoID || journal.StoreID != repo.StoreID || journal.WorktreeID != repo.WorktreeID || filepath.Clean(journal.WorkspaceRoot) != filepath.Clean(repo.WorkspaceRoot.String()) {
		return fmt.Errorf("rollback journal owner invariant failed: repository, store, worktree, or workspace mismatch")
	}
	return nil
}

func validateApplicationMetadata(application *ApplicationMetadata, target ResolvedTarget) error {
	if application == nil {
		return nil
	}
	if target.Manual {
		return fmt.Errorf("Case application metadata is not valid for manual checkpoints")
	}
	if _, err := primitives.ParseCaseID(application.CaseID.String()); err != nil {
		return fmt.Errorf("Case application id: %w", err)
	}
	if _, err := primitives.ParseAttemptID(application.AttemptID.String()); err != nil {
		return fmt.Errorf("Case application attempt id: %w", err)
	}
	if application.PostCommit != target.Commit {
		return fmt.Errorf("Case application post commit %s does not match rollback target %s", application.PostCommit, target.Commit)
	}
	return nil
}

func cloneApplicationMetadata(application *ApplicationMetadata) *ApplicationMetadata {
	if application == nil {
		return nil
	}
	copy := *application
	return &copy
}

func journalWorkspaceGitRollback(repo *checkpoint.Repo, journal Journal) (ResolvedTarget, checkpoint.Snapshot, checkpoint.Snapshot, string, workspacegit.RestorePlan, string, error) {
	target, safety, _, eventSourceID, err := journalRollback(repo, journal)
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
	gitSafetyRefCommit, err := repo.RefCommit(journal.GitSafetyRef)
	if err != nil {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.Snapshot{}, "", workspacegit.RestorePlan{}, "", fmt.Errorf("rollback journal workspace-Git safety ref invariant failed: %w", err)
	}
	if gitSafetyRefCommit != gitSafetyCommit {
		return ResolvedTarget{}, checkpoint.Snapshot{}, checkpoint.Snapshot{}, "", workspacegit.RestorePlan{}, "", fmt.Errorf("rollback journal workspace-Git safety ref invariant failed: %s points to %s, recorded %s", journal.GitSafetyRef, gitSafetyRefCommit, gitSafetyCommit)
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
	return fmt.Sprintf("turnal:rollback:%s:%s:%s:%s", mode, target.selector(), safety.Ref, safety.Commit)
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
	if target.Manual {
		return fmt.Sprintf(
			"refs/agent-vcs/rollback-safety/manual/%s/%d-%s",
			target.Commit, now.UnixNano(), randomSuffix(),
		)
	}
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
