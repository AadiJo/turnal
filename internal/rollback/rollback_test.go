package rollback

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/filelock"
	"github.com/AadiJo/turnal/internal/gitsync"
	"github.com/AadiJo/turnal/internal/manualcheckpoints"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/turns"
	"github.com/AadiJo/turnal/internal/workspacegit"
)

func writeOwnedJournal(repo *checkpoint.Repo, journal Journal) error {
	journal.RepoID = repo.RepoID
	journal.StoreID = repo.StoreID
	journal.WorktreeID = repo.WorktreeID
	journal.WorkspaceRoot = repo.WorkspaceRoot.String()
	return writeJournal(JournalPath(repo), journal)
}

func TestValidateJournalOwnerAcceptsWorkspaceFilesystemAlias(t *testing.T) {
	requireGit(t)
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	alias := filepath.Join(parent, "workspace-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	workspace, err := primitives.ParseWorkspaceRoot(root)
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot: %v", err)
	}
	repo, err := checkpoint.Init(workspace)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	journal := Journal{
		Version: 1, RepoID: repo.RepoID, StoreID: repo.StoreID,
		WorktreeID: repo.WorktreeID, WorkspaceRoot: alias,
	}
	if err := validateJournalOwner(repo, journal); err != nil {
		t.Fatalf("validateJournalOwner: %v", err)
	}
}

func TestResolveTargetDefaultsToPost(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	writeFile(t, root, "app.txt", "before\n")
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("pre checkpoint: %v", err)
	}
	writeFile(t, root, "app.txt", "after\n")
	post, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost)
	if err != nil {
		t.Fatalf("post checkpoint: %v", err)
	}

	targetRef, err := primitives.NewTargetRef(sessionID, turnID, "")
	if err != nil {
		t.Fatalf("NewTargetRef: %v", err)
	}
	resolved, err := ResolveTarget(repo, targetRef)
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	if resolved.Phase != primitives.CheckpointPhasePost {
		t.Fatalf("resolved phase = %q, want post", resolved.Phase)
	}
	if resolved.Commit != post.Commit {
		t.Fatalf("resolved commit = %s, want post %s", resolved.Commit, post.Commit)
	}
	if resolved.Target.String() != "demo:turn:1:post" {
		t.Fatalf("resolved target = %q, want demo:turn:1:post", resolved.Target)
	}
}

func TestRestoreSafetyRecoversPreRollbackWorkspace(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	writeFile(t, root, ".gitignore", "")
	writeFile(t, root, "app.txt", "target\n")
	target, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("target checkpoint: %v", err)
	}
	writeFile(t, root, ".gitignore", "scratch.tmp\n")
	writeFile(t, root, "app.txt", "before rollback\n")
	writeFile(t, root, "scratch.tmp", "protected current data\n")
	safety, err := repo.CreateSnapshotRef("refs/agent-vcs/rollback-safety/demo/turn/000001/pre/recovery", "recovery safety")
	if err != nil {
		t.Fatalf("safety snapshot: %v", err)
	}
	plan, err := repo.PlanRestoreCommit(target.Commit)
	if err != nil {
		t.Fatalf("PlanRestoreCommit: %v", err)
	}
	targetRef, _ := primitives.NewTargetRef(sessionID, turnID, primitives.CheckpointPhasePre)
	journal := Journal{
		Version: 1, State: "restoring", RestorePhase: "restoring", Target: targetRef.String(),
		CheckpointRef: target.Ref.String(), TargetCommitSHA: target.Commit.String(),
		Mode: primitives.RollbackModeCheckpoint.String(), SafetyRef: safety.Ref, SafetyCommitSHA: safety.Commit.String(),
		Changes: plan.Changes, RestoreProtection: &plan.Protection,
	}
	if err := writeOwnedJournal(repo, journal); err != nil {
		t.Fatalf("write journal: %v", err)
	}
	writeFile(t, root, "app.txt", "partially restored\n")

	if err := New(repo).RestoreSafety(); err != nil {
		t.Fatalf("RestoreSafety: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root.String(), "app.txt"))
	if err != nil {
		t.Fatalf("read restored app: %v", err)
	}
	if string(data) != "before rollback\n" {
		t.Fatalf("restored app = %q, want pre-rollback content", data)
	}
	if content := readFile(t, root, "scratch.tmp"); content != "protected current data\n" {
		t.Fatalf("restored scratch.tmp = %q, want protected current data", content)
	}
	if _, err := os.Stat(JournalPath(repo)); !os.IsNotExist(err) {
		t.Fatalf("journal remains after safety restore: %v", err)
	}
}

func TestResumeRecoveryReappliesTargetAndFinalizes(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	writeFile(t, root, ".gitignore", "")
	writeFile(t, root, "app.txt", "target\n")
	target, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("target checkpoint: %v", err)
	}
	writeFile(t, root, ".gitignore", "scratch.tmp\n")
	writeFile(t, root, "app.txt", "before rollback\n")
	writeFile(t, root, "scratch.tmp", "protected current data\n")
	safety, err := repo.CreateSnapshotRef("refs/agent-vcs/rollback-safety/demo/turn/000001/pre/resume", "resume safety")
	if err != nil {
		t.Fatalf("safety snapshot: %v", err)
	}
	plan, err := repo.PlanRestoreCommit(target.Commit)
	if err != nil {
		t.Fatalf("PlanRestoreCommit: %v", err)
	}
	targetRef, _ := primitives.NewTargetRef(sessionID, turnID, primitives.CheckpointPhasePre)
	resolved, err := ResolveTarget(repo, targetRef)
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	journal := Journal{
		Version: 1, State: "restoring", RestorePhase: "restoring", Target: targetRef.String(),
		CheckpointRef: target.Ref.String(), TargetCommitSHA: target.Commit.String(),
		Mode: primitives.RollbackModeCheckpoint.String(), SafetyRef: safety.Ref, SafetyCommitSHA: safety.Commit.String(),
		EventSourceID: rollbackEventSourceID(resolved, safety),
		Changes:       plan.Changes, RestoreProtection: &plan.Protection,
	}
	if err := writeOwnedJournal(repo, journal); err != nil {
		t.Fatalf("write journal: %v", err)
	}
	writeFile(t, root, ".gitignore", "")
	writeFile(t, root, "app.txt", "ambiguous partial state\n")

	if err := New(repo).ResumeRecovery(); err != nil {
		t.Fatalf("ResumeRecovery: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root.String(), "app.txt"))
	if err != nil {
		t.Fatalf("read resumed app: %v", err)
	}
	if string(data) != "target\n" {
		t.Fatalf("resumed app = %q, want target content", data)
	}
	if content := readFile(t, root, "scratch.tmp"); content != "protected current data\n" {
		t.Fatalf("resumed scratch.tmp = %q, want protected current data", content)
	}
	if _, err := os.Stat(JournalPath(repo)); !os.IsNotExist(err) {
		t.Fatalf("journal remains after resume: %v", err)
	}
}

func TestManualRollbackRecoveryPhasesFinalizeIdempotently(t *testing.T) {
	for _, phase := range []string{"planned", "restoring", "restored"} {
		t.Run(phase, func(t *testing.T) {
			requireGit(t)
			root := workspaceRoot(t)
			repo, err := checkpoint.Init(root)
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			writeFile(t, root, "app.txt", "target\n")
			created, err := repo.CreateManualCheckpoint()
			if err != nil {
				t.Fatalf("CreateManualCheckpoint: %v", err)
			}
			if _, err := manualcheckpoints.Append(repo, created, "known good"); err != nil {
				t.Fatalf("append manual checkpoint: %v", err)
			}
			infos, err := repo.FindCheckpointIDPrefix(created.ID.String())
			if err != nil || len(infos) != 1 {
				t.Fatalf("manual checkpoint infos = %#v, err=%v", infos, err)
			}
			resolved, err := ResolveManualCheckpointInfo(infos[0])
			if err != nil {
				t.Fatalf("ResolveManualCheckpointInfo: %v", err)
			}
			writeFile(t, root, "app.txt", "before rollback\n")
			safety, err := repo.CreateSnapshotRef("refs/agent-vcs/rollback-safety/manual/recovery-"+phase, "manual recovery safety")
			if err != nil {
				t.Fatalf("CreateSnapshotRef: %v", err)
			}
			plan, err := repo.PlanRestoreCommit(created.Commit)
			if err != nil {
				t.Fatalf("PlanRestoreCommit: %v", err)
			}
			if phase == "restored" {
				if err := repo.RestoreCommit(created.Commit); err != nil {
					t.Fatalf("RestoreCommit: %v", err)
				}
			} else {
				writeFile(t, root, "app.txt", "ambiguous partial state\n")
			}
			sourceID := rollbackEventSourceID(resolved, safety)
			journal := Journal{
				Version: 1, State: phase, RestorePhase: phase, Manual: true,
				Target: created.Commit.String(), CheckpointRef: created.Ref.String(), TargetCommitSHA: created.Commit.String(),
				Mode: primitives.RollbackModeCheckpoint.String(), SafetyRef: safety.Ref, SafetyCommitSHA: safety.Commit.String(),
				EventSourceID: sourceID,
				Changes:       plan.Changes, RestoreProtection: &plan.Protection,
			}
			if err := writeOwnedJournal(repo, journal); err != nil {
				t.Fatalf("writeJournal: %v", err)
			}

			if err := New(repo).ResumeRecovery(); err != nil {
				t.Fatalf("ResumeRecovery: %v", err)
			}
			content, err := os.ReadFile(filepath.Join(root.String(), "app.txt"))
			if err != nil || string(content) != "target\n" {
				t.Fatalf("recovered content = %q, err=%v", content, err)
			}
			if _, err := os.Stat(JournalPath(repo)); !os.IsNotExist(err) {
				t.Fatalf("journal remains after recovery: %v", err)
			}
			if got := countWorkspaceRollbackEvents(t, repo, sourceID); got != 1 {
				t.Fatalf("workspace rollback events = %d, want 1", got)
			}

			journal.State, journal.RestorePhase = "restored", "restored"
			if err := writeOwnedJournal(repo, journal); err != nil {
				t.Fatalf("write restored journal again: %v", err)
			}
			if err := New(repo).ResumeRecovery(); err != nil {
				t.Fatalf("idempotent ResumeRecovery: %v", err)
			}
			if got := countWorkspaceRollbackEvents(t, repo, sourceID); got != 1 {
				t.Fatalf("workspace rollback events after retry = %d, want 1", got)
			}
		})
	}
}

func TestManualRollbackRestoreSafety(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	writeFile(t, root, "app.txt", "target\n")
	created, err := repo.CreateManualCheckpoint()
	if err != nil {
		t.Fatalf("CreateManualCheckpoint: %v", err)
	}
	writeFile(t, root, "app.txt", "before rollback\n")
	safety, err := repo.CreateSnapshotRef("refs/agent-vcs/rollback-safety/manual/restore-safety", "manual safety")
	if err != nil {
		t.Fatalf("CreateSnapshotRef: %v", err)
	}
	plan, err := repo.PlanRestoreCommit(created.Commit)
	if err != nil {
		t.Fatalf("PlanRestoreCommit: %v", err)
	}
	journal := Journal{
		Version: 1, State: "restoring", RestorePhase: "restoring", Manual: true,
		Target: created.Commit.String(), CheckpointRef: created.Ref.String(), TargetCommitSHA: created.Commit.String(),
		Mode: primitives.RollbackModeCheckpoint.String(), SafetyRef: safety.Ref, SafetyCommitSHA: safety.Commit.String(),
		Changes: plan.Changes, RestoreProtection: &plan.Protection,
	}
	if err := writeOwnedJournal(repo, journal); err != nil {
		t.Fatalf("writeJournal: %v", err)
	}
	writeFile(t, root, "app.txt", "partial\n")

	if err := New(repo).RestoreSafety(); err != nil {
		t.Fatalf("RestoreSafety: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root.String(), "app.txt"))
	if err != nil || string(content) != "before rollback\n" {
		t.Fatalf("safety content = %q, err=%v", content, err)
	}
	if _, err := os.Stat(JournalPath(repo)); !os.IsNotExist(err) {
		t.Fatalf("journal remains after safety restore: %v", err)
	}
}

func TestManualRollbackRecoveryRejectsMalformedJournal(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	created, err := repo.CreateManualCheckpoint()
	if err != nil {
		t.Fatalf("CreateManualCheckpoint: %v", err)
	}
	safety, err := repo.CreateSnapshotRef("refs/agent-vcs/rollback-safety/manual/malformed", "manual safety")
	if err != nil {
		t.Fatalf("CreateSnapshotRef: %v", err)
	}
	journal := Journal{
		Version: 1, State: "restored", RestorePhase: "restored", Manual: true,
		Target: strings.Repeat("0", len(created.Commit.String())), CheckpointRef: created.Ref.String(), TargetCommitSHA: created.Commit.String(),
		Mode: primitives.RollbackModeCheckpoint.String(), SafetyRef: safety.Ref, SafetyCommitSHA: safety.Commit.String(),
	}
	if err := writeOwnedJournal(repo, journal); err != nil {
		t.Fatalf("writeJournal: %v", err)
	}
	if err := New(repo).ResumeRecovery(); err == nil || !strings.Contains(err.Error(), "manual target invariant failed") {
		t.Fatalf("ResumeRecovery error = %v", err)
	}
	if _, err := os.Stat(JournalPath(repo)); err != nil {
		t.Fatalf("malformed journal was removed: %v", err)
	}
}

func TestManualRollbackRecoveryRejectsRefCommitMismatchBeforeMutation(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	writeFile(t, root, "app.txt", "checkpoint a\n")
	checkpointA, err := repo.CreateManualCheckpoint()
	if err != nil {
		t.Fatalf("CreateManualCheckpoint A: %v", err)
	}
	writeFile(t, root, "app.txt", "checkpoint b\n")
	checkpointB, err := repo.CreateManualCheckpoint()
	if err != nil {
		t.Fatalf("CreateManualCheckpoint B: %v", err)
	}
	writeFile(t, root, "app.txt", "current workspace\n")
	safety, err := repo.CreateSnapshotRef("refs/agent-vcs/rollback-safety/manual/ref-mismatch", "manual safety")
	if err != nil {
		t.Fatalf("CreateSnapshotRef: %v", err)
	}
	journal := Journal{
		Version: 1, State: "restoring", RestorePhase: "restoring", Manual: true,
		Target: checkpointB.Commit.String(), CheckpointRef: checkpointA.Ref.String(), TargetCommitSHA: checkpointB.Commit.String(),
		Mode: primitives.RollbackModeCheckpoint.String(), SafetyRef: safety.Ref, SafetyCommitSHA: safety.Commit.String(),
	}
	if err := writeOwnedJournal(repo, journal); err != nil {
		t.Fatalf("writeJournal: %v", err)
	}

	if err := New(repo).ResumeRecovery(); err == nil || !strings.Contains(err.Error(), "checkpoint ref invariant failed") {
		t.Fatalf("ResumeRecovery error = %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root.String(), "app.txt"))
	if err != nil || string(content) != "current workspace\n" {
		t.Fatalf("workspace mutated before journal validation: content=%q err=%v", content, err)
	}
	remaining, ok, err := readJournal(JournalPath(repo))
	if err != nil || !ok || remaining.phase() != "restoring" {
		t.Fatalf("journal after rejection = %#v, ok=%v err=%v", remaining, ok, err)
	}
	if events, err := manualcheckpoints.ReadEvents(repo, false); err != nil || len(events) != 0 {
		t.Fatalf("rejected recovery wrote events: %#v err=%v", events, err)
	}
}

func TestRunFinalizesRestoredJournal(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	writeFile(t, root, "app.txt", "target\n")
	targetCheckpoint, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("target checkpoint: %v", err)
	}

	writeFile(t, root, "app.txt", "before rollback\n")
	targetRef, err := primitives.NewTargetRef(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("NewTargetRef: %v", err)
	}
	resolved, err := ResolveTarget(repo, targetRef)
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	safety, err := repo.CreateSnapshotRef("refs/agent-vcs/rollback-safety/demo/turn/000001/pre/test", "test safety")
	if err != nil {
		t.Fatalf("CreateSnapshotRef: %v", err)
	}
	if err := repo.RestoreCommit(targetCheckpoint.Commit); err != nil {
		t.Fatalf("RestoreCommit: %v", err)
	}

	sourceID := rollbackEventSourceID(resolved, safety)
	journal := Journal{
		Version:         1,
		State:           "restored",
		StartedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		Target:          targetRef.String(),
		CheckpointRef:   resolved.CheckpointRef.String(),
		TargetCommitSHA: resolved.Commit.String(),
		SafetyRef:       safety.Ref,
		SafetyCommitSHA: safety.Commit.String(),
		EventSourceID:   sourceID,
		Changes: []checkpoint.RestoreChange{{
			Path:   "app.txt",
			Action: checkpoint.RestoreActionModified,
		}},
	}
	if err := writeOwnedJournal(repo, journal); err != nil {
		t.Fatalf("writeJournal: %v", err)
	}

	result, err := New(repo).Run(Request{Target: targetRef, DryRun: true})
	if err != nil {
		t.Fatalf("Run with restored journal: %v", err)
	}
	if !result.DryRun {
		t.Fatal("Run result DryRun=false")
	}
	if _, err := os.Stat(JournalPath(repo)); !os.IsNotExist(err) {
		t.Fatalf("journal still exists or stat failed: %v", err)
	}
	if got := countEventsWithSourceID(t, repo, sessionID, sourceID); got != 1 {
		t.Fatalf("rollback events with source id = %d, want 1", got)
	}

	if err := writeOwnedJournal(repo, journal); err != nil {
		t.Fatalf("writeJournal second time: %v", err)
	}
	if _, err := New(repo).Run(Request{Target: targetRef, DryRun: true}); err != nil {
		t.Fatalf("Run with already-logged restored journal: %v", err)
	}
	if got := countEventsWithSourceID(t, repo, sessionID, sourceID); got != 1 {
		t.Fatalf("rollback events with source id after retry = %d, want 1", got)
	}

	journal.EventSourceID = "missing-source-id"
	if err := writeOwnedJournal(repo, journal); err != nil {
		t.Fatalf("writeJournal with missing source id: %v", err)
	}
	if _, err := New(repo).Run(Request{Target: targetRef, DryRun: true}); err != nil {
		t.Fatalf("Run with payload-matched restored journal: %v", err)
	}
	if got := countRollbackEvents(t, repo, sessionID); got != 1 {
		t.Fatalf("rollback events after payload-matched retry = %d, want 1", got)
	}
}

func TestRunFinalizesRestoredWorkspaceGitJournalWithChangeSummary(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	runWorkspaceGit(t, root, "init")
	bootstrapped, err := checkpoint.Bootstrap(root)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	repo := bootstrapped.Repo
	runWorkspaceGit(t, root, "config", "user.email", "turnal@example.test")
	runWorkspaceGit(t, root, "config", "user.name", "turnal")

	writeFile(t, root, "tracked.txt", "base\n")
	runWorkspaceGit(t, root, "add", ".gitignore", "tracked.txt")
	runWorkspaceGit(t, root, "commit", "-m", "base")

	writeFile(t, root, "tracked.txt", "staged\n")
	runWorkspaceGit(t, root, "add", "tracked.txt")
	writeFile(t, root, "tracked.txt", "unstaged\n")
	writeFile(t, root, "scratch.txt", "untracked\n")

	sessionID := sessionID(t, "demo")
	enabled := true
	manager := turns.Manager{Repo: repo, GitSyncEnabled: &enabled}
	started, err := manager.Start(sessionID, 0)
	if err != nil {
		t.Fatalf("Start with git-sync: %v", err)
	}
	if started.GitSync == nil {
		t.Fatal("Start did not create git-sync state")
	}

	targetRef, err := primitives.NewTargetRef(sessionID, started.TurnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("NewTargetRef: %v", err)
	}
	resolved, err := ResolveTarget(repo, targetRef)
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	gitSyncRef, err := gitsync.Ref(sessionID, started.TurnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("git-sync ref: %v", err)
	}
	targetCapture, err := gitsync.Load(repo, gitSyncRef)
	if err != nil {
		t.Fatalf("load git-sync: %v", err)
	}
	workspace := workspacegit.Open(repo.WorkspaceRoot)
	gitPlan, err := workspace.PlanRestore(targetCapture)
	if err != nil {
		t.Fatalf("PlanRestore: %v", err)
	}
	currentCapture, err := workspace.Capture()
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	safety, err := repo.CreateSnapshotRef("refs/agent-vcs/rollback-safety/demo/turn/000001/pre/workspace-test", "test safety")
	if err != nil {
		t.Fatalf("CreateSnapshotRef: %v", err)
	}
	gitSafety, err := gitsync.SavePrivate(repo, "refs/agent-vcs/git-safety/demo/turn/000001/pre/workspace-test", currentCapture, "test git safety")
	if err != nil {
		t.Fatalf("SavePrivate git safety: %v", err)
	}
	sourceID := rollbackEventSourceIDForMode(resolved, safety, primitives.RollbackModeWorkspaceGit)
	journal := Journal{
		Version:            1,
		State:              "restored",
		RestorePhase:       "restored",
		StartedAt:          time.Now().UTC().Format(time.RFC3339Nano),
		Mode:               primitives.RollbackModeWorkspaceGit.String(),
		Target:             targetRef.String(),
		CheckpointRef:      resolved.CheckpointRef.String(),
		TargetCommitSHA:    resolved.Commit.String(),
		GitSyncRef:         gitSyncRef.String(),
		SafetyRef:          safety.Ref,
		SafetyCommitSHA:    safety.Commit.String(),
		GitSafetyRef:       gitSafety.Ref,
		GitSafetyCommitSHA: gitSafety.Commit.String(),
		EventSourceID:      sourceID,
		GitChanges:         workspaceGitChangesFromPlan(gitPlan),
	}
	if err := writeOwnedJournal(repo, journal); err != nil {
		t.Fatalf("writeJournal: %v", err)
	}

	if _, err := New(repo).Run(Request{Target: targetRef, DryRun: true, WorkspaceGit: true}); err != nil {
		t.Fatalf("Run with restored workspace-git journal: %v", err)
	}
	event := rollbackEventWithSourceID(t, repo, sessionID, sourceID)
	var payload EventPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("unmarshal rollback payload: %v", err)
	}
	if payload.ChangeSummary.Total != 3 || payload.ChangeSummary.Modified != 2 || payload.ChangeSummary.Added != 1 {
		t.Fatalf("change summary = %#v, want total=3 modified=2 added=1", payload.ChangeSummary)
	}
}

func TestRunClearsPreRestoreJournal(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	writeFile(t, root, "app.txt", "target\n")
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("target checkpoint: %v", err)
	}
	targetRef, err := primitives.NewTargetRef(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("NewTargetRef: %v", err)
	}
	resolved, err := ResolveTarget(repo, targetRef)
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}

	journal := Journal{
		Version:         1,
		State:           "intent",
		RestorePhase:    "intent",
		StartedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		Target:          targetRef.String(),
		CheckpointRef:   resolved.CheckpointRef.String(),
		TargetCommitSHA: resolved.Commit.String(),
		Changes: []checkpoint.RestoreChange{{
			Path:   "app.txt",
			Action: checkpoint.RestoreActionModified,
		}},
	}
	if err := writeOwnedJournal(repo, journal); err != nil {
		t.Fatalf("writeJournal intent: %v", err)
	}
	result, err := New(repo).Run(Request{Target: targetRef, DryRun: true})
	if err != nil {
		t.Fatalf("Run with intent journal: %v", err)
	}
	if !result.DryRun {
		t.Fatal("Run result DryRun=false")
	}
	if _, err := os.Stat(JournalPath(repo)); !os.IsNotExist(err) {
		t.Fatalf("intent journal still exists or stat failed: %v", err)
	}

	journal.State = "planned"
	journal.RestorePhase = "planned"
	journal.SafetyRef = "refs/agent-vcs/rollback-safety/demo/turn/000001/pre/example"
	journal.SafetyCommitSHA = resolved.Commit.String()
	if err := writeOwnedJournal(repo, journal); err != nil {
		t.Fatalf("writeJournal planned: %v", err)
	}
	_, err = New(repo).Run(Request{Target: targetRef, DryRun: true})
	if err == nil {
		t.Fatal("Run with planned journal succeeded, want active journal error")
	}
	var activeErr ActiveJournalError
	if !errors.As(err, &activeErr) {
		t.Fatalf("Run error = %T %v, want ActiveJournalError", err, err)
	}
	if _, err := os.Stat(JournalPath(repo)); err != nil {
		t.Fatalf("planned journal missing after blocked run: %v", err)
	}
}

func TestRunFailsWhenWorkspaceLockHeld(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	writeFile(t, root, "app.txt", "target\n")
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("target checkpoint: %v", err)
	}
	writeFile(t, root, "app.txt", "current\n")
	lock, err := filelock.Acquire(repo.WorkspaceLockPath(), time.Second)
	if err != nil {
		t.Fatalf("create workspace lock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	repo.LockTimeout = 20 * time.Millisecond

	targetRef, err := primitives.NewTargetRef(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("NewTargetRef: %v", err)
	}
	for _, request := range []Request{
		{Target: targetRef, DryRun: true},
		{Target: targetRef},
	} {
		_, err = New(repo).Run(request)
		if err == nil {
			t.Fatalf("Run(%#v) succeeded while workspace lock was held", request)
		}
		if !strings.Contains(err.Error(), "workspace lock busy") {
			t.Fatalf("Run(%#v) error = %v, want workspace lock busy", request, err)
		}
	}
}

func TestRunRejectsWorkspaceChangedAfterPreview(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	writeFile(t, root, "app.txt", "target\n")
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("target checkpoint: %v", err)
	}
	writeFile(t, root, "app.txt", "previewed\n")
	targetRef, err := primitives.NewTargetRef(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("NewTargetRef: %v", err)
	}
	preview, err := New(repo).Run(Request{Target: targetRef, DryRun: true})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	writeFile(t, root, "app.txt", "changed after preview\n")

	_, err = New(repo).Run(Request{Target: targetRef, ExpectedWorkspaceTree: preview.Plan.WorkspaceTree})
	if err == nil || !strings.Contains(err.Error(), "workspace changed since rollback preview") {
		t.Fatalf("Run error = %v, want stale preview rejection", err)
	}
	if got := readFile(t, root, "app.txt"); got != "changed after preview\n" {
		t.Fatalf("app.txt = %q, want workspace left untouched", got)
	}
	if _, err := os.Stat(JournalPath(repo)); !os.IsNotExist(err) {
		t.Fatalf("rollback journal created for rejected preview: %v", err)
	}
}

func TestRunRestoreFailureReturnsSafetyAndKeepsExtraFiles(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	writeFile(t, root, "conflict", "target\n")
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("target checkpoint: %v", err)
	}

	if err := os.Remove(filepath.Join(root.String(), "conflict")); err != nil {
		t.Fatalf("remove conflict file: %v", err)
	}
	writeFile(t, root, "conflict/.turnal/keep", "metadata\n")
	writeFile(t, root, "extra.txt", "must remain after failed restore\n")

	targetRef, err := primitives.NewTargetRef(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("NewTargetRef: %v", err)
	}
	_, err = New(repo).Run(Request{Target: targetRef})
	if err == nil {
		t.Fatal("Run succeeded, want restore failure")
	}
	var safetyErr SafetyError
	if !errors.As(err, &safetyErr) {
		t.Fatalf("error = %T %v, want SafetyError", err, err)
	}
	if safetyErr.Safety.Ref == "" || safetyErr.Safety.Commit == "" || safetyErr.JournalPath != JournalPath(repo) {
		t.Fatalf("safety error missing recovery metadata: %#v", safetyErr)
	}
	if _, err := os.Stat(filepath.Join(root.String(), "extra.txt")); err != nil {
		t.Fatalf("extra.txt was deleted before restore failure: %v", err)
	}
	journal, ok, err := readJournal(JournalPath(repo))
	if err != nil {
		t.Fatalf("readJournal: %v", err)
	}
	if !ok {
		t.Fatal("journal missing after restore failure")
	}
	if journal.State != "restoring" {
		t.Fatalf("journal state = %q, want restoring", journal.State)
	}
	if journal.SafetyRef != safetyErr.Safety.Ref || journal.SafetyCommitSHA != safetyErr.Safety.Commit.String() {
		t.Fatalf("journal safety = %s %s, want %s %s", journal.SafetyRef, journal.SafetyCommitSHA, safetyErr.Safety.Ref, safetyErr.Safety.Commit)
	}
}

func TestRunPreservesSecretsDeniedFiles(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	writeFile(t, root, "app.txt", "target\n")
	writeFile(t, root, ".env", "SECRET=target\n")
	writeFile(t, root, "nested/.env", "SECRET=nested-target\n")
	writeFile(t, root, "config/credentials.json", `{"secret":"target"}`)
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("target checkpoint: %v", err)
	}

	writeFile(t, root, "app.txt", "current\n")
	writeFile(t, root, ".env", "SECRET=current\n")
	writeFile(t, root, "nested/.env", "SECRET=nested-current\n")
	writeFile(t, root, "config/credentials.json", `{"secret":"current"}`)

	targetRef, err := primitives.NewTargetRef(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("NewTargetRef: %v", err)
	}
	if _, err := New(repo).Run(Request{Target: targetRef}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := readFile(t, root, "app.txt"); got != "target\n" {
		t.Fatalf("app.txt = %q, want target", got)
	}
	for path, want := range map[string]string{
		".env":                    "SECRET=current\n",
		"nested/.env":             "SECRET=nested-current\n",
		"config/credentials.json": `{"secret":"current"}`,
	} {
		if got := readFile(t, root, path); got != want {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
	}
}

func TestRunDryRunAndRestoreIgnoreDeniedSecretsFromOldCheckpoints(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	writeFile(t, root, ".turnal/config.toml", "version = 1\n[secrets]\nsnapshot_deny_globs = [\"never-match-secret\"]\n")

	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	writeFile(t, root, "app.txt", "target\n")
	writeFile(t, root, ".env", "SECRET=target\n")
	target, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("target checkpoint: %v", err)
	}
	if _, err := repo.CommitFileBytes(target.Commit, ".env"); err != nil {
		t.Fatalf("target checkpoint did not capture .env: %v", err)
	}

	writeFile(t, root, ".turnal/config.toml", "version = 1\n[secrets]\nsnapshot_deny_globs = [\".env\"]\n")
	writeFile(t, root, "app.txt", "current\n")
	writeFile(t, root, ".env", "SECRET=current\n")

	targetRef, err := primitives.NewTargetRef(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("NewTargetRef: %v", err)
	}
	dryRun, err := New(repo).Run(Request{Target: targetRef, DryRun: true})
	if err != nil {
		t.Fatalf("dry-run Run: %v", err)
	}
	if !hasRestoreChange(dryRun.Plan.Changes, "app.txt") {
		t.Fatalf("dry-run changes = %#v, want app.txt", dryRun.Plan.Changes)
	}
	if hasRestoreChange(dryRun.Plan.Changes, ".env") {
		t.Fatalf("dry-run changes = %#v, want .env filtered by secrets policy", dryRun.Plan.Changes)
	}

	if _, err := New(repo).Run(Request{Target: targetRef}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := readFile(t, root, "app.txt"); got != "target\n" {
		t.Fatalf("app.txt = %q, want target", got)
	}
	if got := readFile(t, root, ".env"); got != "SECRET=current\n" {
		t.Fatalf(".env = %q, want current secret preserved", got)
	}
}

func TestRunWorkspaceGitRestoresCapturedDirtyState(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	runWorkspaceGit(t, root, "init")
	bootstrapped, err := checkpoint.Bootstrap(root)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	repo := bootstrapped.Repo
	runWorkspaceGit(t, root, "config", "user.email", "turnal@example.test")
	runWorkspaceGit(t, root, "config", "user.name", "turnal")
	runWorkspaceGit(t, root, "config", "core.autocrlf", "false")

	writeFile(t, root, "tracked.txt", "base\n")
	runWorkspaceGit(t, root, "add", ".gitignore", "tracked.txt")
	runWorkspaceGit(t, root, "commit", "-m", "base")
	baseCommit := strings.TrimSpace(runWorkspaceGit(t, root, "rev-parse", "HEAD"))

	writeFile(t, root, "tracked.txt", "staged\n")
	runWorkspaceGit(t, root, "add", "tracked.txt")
	writeFile(t, root, "tracked.txt", "unstaged\n")
	writeFile(t, root, "scratch.txt", "untracked\n")

	sessionID := sessionID(t, "demo")
	enabled := true
	manager := turns.Manager{Repo: repo, GitSyncEnabled: &enabled}
	started, err := manager.Start(sessionID, 0)
	if err != nil {
		t.Fatalf("Start with git-sync: %v", err)
	}
	if started.GitSync == nil {
		t.Fatal("Start did not create git-sync state")
	}

	runWorkspaceGit(t, root, "reset", "--hard", "HEAD")
	runWorkspaceGit(t, root, "clean", "-fd", "--", ".")
	writeFile(t, root, "tracked.txt", "future\n")
	runWorkspaceGit(t, root, "add", "tracked.txt")
	runWorkspaceGit(t, root, "commit", "-m", "future")
	writeFile(t, root, "other.txt", "remove me\n")

	targetRef, err := primitives.NewTargetRef(sessionID, started.TurnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("NewTargetRef: %v", err)
	}
	result, err := New(repo).Run(Request{Target: targetRef, WorkspaceGit: true})
	if err != nil {
		t.Fatalf("workspace-git rollback: %v", err)
	}
	if result.Mode != primitives.RollbackModeWorkspaceGit || result.GitSafety == nil || result.Safety == nil {
		t.Fatalf("workspace-git rollback result missing mode/safety: %#v", result)
	}

	if head := strings.TrimSpace(runWorkspaceGit(t, root, "rev-parse", "HEAD")); head != baseCommit {
		t.Fatalf("HEAD = %s, want base %s", head, baseCommit)
	}
	indexContent := runWorkspaceGit(t, root, "show", ":tracked.txt")
	if indexContent != "staged\n" {
		t.Fatalf("index tracked.txt = %q, want staged", indexContent)
	}
	worktreeContent, err := os.ReadFile(filepath.Join(root.String(), "tracked.txt"))
	if err != nil {
		t.Fatalf("read tracked.txt: %v", err)
	}
	if string(worktreeContent) != "unstaged\n" {
		t.Fatalf("worktree tracked.txt = %q, want unstaged", worktreeContent)
	}
	scratch, err := os.ReadFile(filepath.Join(root.String(), "scratch.txt"))
	if err != nil {
		t.Fatalf("read scratch.txt: %v", err)
	}
	if string(scratch) != "untracked\n" {
		t.Fatalf("scratch.txt = %q, want untracked", scratch)
	}
	if _, err := os.Stat(filepath.Join(root.String(), "other.txt")); !os.IsNotExist(err) {
		t.Fatalf("other.txt still exists or stat failed: %v", err)
	}
	status := runWorkspaceGit(t, root, "status", "--porcelain")
	if !strings.Contains(status, "MM tracked.txt") || !strings.Contains(status, "?? scratch.txt") {
		t.Fatalf("status after workspace-git rollback = %q, want staged+unstaged tracked and untracked scratch", status)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable not found")
	}
}

func workspaceRoot(t *testing.T) primitives.WorkspaceRoot {
	t.Helper()
	// Redirect machine-wide state: initializing a store registers it, and a
	// throwaway temp workspace must not land in the developer's real registry.
	t.Setenv("TURNAL_STATE_DIR", t.TempDir())
	root, err := primitives.ParseWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot: %v", err)
	}
	return root
}

func sessionID(t *testing.T, value string) primitives.SessionID {
	t.Helper()
	sessionID, err := primitives.ParseSessionID(value)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	return sessionID
}

func writeFile(t *testing.T, root primitives.WorkspaceRoot, relPath, content string) {
	t.Helper()
	path := filepath.Join(root.String(), filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
}

func countWorkspaceRollbackEvents(t *testing.T, repo *checkpoint.Repo, sourceID string) int {
	t.Helper()
	events, err := manualcheckpoints.ReadEvents(repo, false)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	count := 0
	for _, event := range events {
		if event.Type == primitives.EventTypeRollback && event.SourceID == sourceID {
			count++
		}
	}
	return count
}

func readFile(t *testing.T, root primitives.WorkspaceRoot, relPath string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root.String(), filepath.FromSlash(relPath)))
	if err != nil {
		t.Fatalf("read %s: %v", relPath, err)
	}
	return string(content)
}

func hasRestoreChange(changes []checkpoint.RestoreChange, path string) bool {
	for _, change := range changes {
		if change.Path == path {
			return true
		}
	}
	return false
}

func runWorkspaceGit(t *testing.T, root primitives.WorkspaceRoot, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root.String()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func countEventsWithSourceID(t *testing.T, repo *checkpoint.Repo, sessionID primitives.SessionID, sourceID string) int {
	t.Helper()
	events, err := eventlog.Open(repo.MetadataDir).Read(sessionID)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	count := 0
	for _, event := range events {
		if event.SourceID == sourceID {
			count++
		}
	}
	return count
}

func rollbackEventWithSourceID(t *testing.T, repo *checkpoint.Repo, sessionID primitives.SessionID, sourceID string) eventlog.Event {
	t.Helper()
	events, err := eventlog.Open(repo.MetadataDir).Read(sessionID)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	for _, event := range events {
		if event.Type == primitives.EventTypeRollback && event.SourceID == sourceID {
			return event
		}
	}
	t.Fatalf("rollback event with source id %s not found", sourceID)
	return eventlog.Event{}
}

func countRollbackEvents(t *testing.T, repo *checkpoint.Repo, sessionID primitives.SessionID) int {
	t.Helper()
	events, err := eventlog.Open(repo.MetadataDir).Read(sessionID)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	count := 0
	for _, event := range events {
		if event.Type == primitives.EventTypeRollback {
			count++
		}
	}
	return count
}
