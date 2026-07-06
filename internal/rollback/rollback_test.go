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

	"agent-vcs-again/internal/checkpoint"
	eventlog "agent-vcs-again/internal/events"
	"agent-vcs-again/internal/gitsync"
	"agent-vcs-again/internal/primitives"
	"agent-vcs-again/internal/turns"
	"agent-vcs-again/internal/workspacegit"
)

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
	if err := writeJournal(JournalPath(repo), journal); err != nil {
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

	if err := writeJournal(JournalPath(repo), journal); err != nil {
		t.Fatalf("writeJournal second time: %v", err)
	}
	if _, err := New(repo).Run(Request{Target: targetRef, DryRun: true}); err != nil {
		t.Fatalf("Run with already-logged restored journal: %v", err)
	}
	if got := countEventsWithSourceID(t, repo, sessionID, sourceID); got != 1 {
		t.Fatalf("rollback events with source id after retry = %d, want 1", got)
	}

	journal.EventSourceID = "missing-source-id"
	if err := writeJournal(JournalPath(repo), journal); err != nil {
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
	bootstrapped, err := checkpoint.Bootstrap(root)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	repo := bootstrapped.Repo
	runWorkspaceGit(t, root, "config", "user.email", "agent-vcs@example.test")
	runWorkspaceGit(t, root, "config", "user.name", "agent-vcs")

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
	if err := writeJournal(JournalPath(repo), journal); err != nil {
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
	if err := writeJournal(JournalPath(repo), journal); err != nil {
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
	if err := writeJournal(JournalPath(repo), journal); err != nil {
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
	if err := os.Mkdir(repo.WorkspaceLockPath(), 0o700); err != nil {
		t.Fatalf("create workspace lock: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(repo.WorkspaceLockPath()) })

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
	writeFile(t, root, "conflict/.agent-vcs/keep", "metadata\n")
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
	writeFile(t, root, ".agent-vcs/config.toml", "version = 1\n[secrets]\nsnapshot_deny_globs = [\"never-match-secret\"]\n")

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

	writeFile(t, root, ".agent-vcs/config.toml", "version = 1\n[secrets]\nsnapshot_deny_globs = [\".env\"]\n")
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
	bootstrapped, err := checkpoint.Bootstrap(root)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	repo := bootstrapped.Repo
	runWorkspaceGit(t, root, "config", "user.email", "agent-vcs@example.test")
	runWorkspaceGit(t, root, "config", "user.name", "agent-vcs")

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
