package experiments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/cases"
	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	rollbackengine "github.com/AadiJo/turnal/internal/rollback"
	"github.com/AadiJo/turnal/internal/runs"
	"github.com/AadiJo/turnal/internal/turnevents"
	"github.com/AadiJo/turnal/internal/turns"
)

type runnerFunc func(context.Context, string, []string, []string) (int, error)

func (run runnerFunc) Run(ctx context.Context, root string, command, environment []string) (int, error) {
	return run(ctx, root, command, environment)
}

func TestExecuteRunsFromCaseBaseAndCapturesDurableResult(t *testing.T) {
	repo, definition := experimentCase(t)
	sourcePath := filepath.Join(repo.WorkspaceRoot.String(), "app.txt")
	if err := os.WriteFile(sourcePath, []byte("live workspace\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var isolatedRoot string
	runner := runnerFunc(func(_ context.Context, root string, command, environment []string) (int, error) {
		isolatedRoot = root
		data, err := os.ReadFile(filepath.Join(root, "app.txt"))
		if err != nil || string(data) != "before\n" {
			t.Fatalf("fork base = %q, %v", data, err)
		}
		if _, err := os.Stat(filepath.Join(root, ".git")); !os.IsNotExist(err) {
			t.Fatalf("isolated workspace contains user Git metadata: %v", err)
		}
		env := environmentMap(environment)
		if env[EnvCaseID] != definition.ID.String() || env[EnvInstruction] != "Fix the parser" || env[EnvBaseCommit] != definition.Readiness.Base.CommitSHA.String() {
			t.Fatalf("fork environment = %#v", env)
		}
		return 0, os.WriteFile(filepath.Join(root, "app.txt"), []byte("fork result\n"), 0o644)
	})

	result, err := Execute(context.Background(), repo, Request{Case: definition, Command: []string{"runner", "--flag"}, Runner: runner})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Status != cases.AttemptStatusSucceeded || result.AttemptID == "" || result.PostCommit == "" || result.Workspace != "" || result.WorkspaceKept {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Stat(isolatedRoot); !os.IsNotExist(err) {
		t.Fatalf("temporary fork workspace still exists: %v", err)
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil || string(data) != "live workspace\n" {
		t.Fatalf("source workspace changed: %q, %v", data, err)
	}
	postData, exists, err := repo.CommitFileBytesIfExists(result.PostCommit, "app.txt")
	if err != nil || !exists || string(postData) != "fork result\n" {
		t.Fatalf("durable post checkpoint = %q %t %v", postData, exists, err)
	}
	projection, err := cases.Rebuild(repo)
	if err != nil {
		t.Fatal(err)
	}
	updated, _ := projection.Case(definition.ID)
	if len(updated.AttemptLinks) != 1 || updated.AttemptLinks[0].Result == nil || updated.AttemptLinks[0].Result.PostCommit != result.PostCommit {
		t.Fatalf("case attempts = %#v", updated.AttemptLinks)
	}
}

func TestExecuteRecordsFailedAndInfrastructureAttempts(t *testing.T) {
	for name, runner := range map[string]Runner{
		"failed":     runnerFunc(func(context.Context, string, []string, []string) (int, error) { return 7, nil }),
		"incomplete": runnerFunc(func(context.Context, string, []string, []string) (int, error) { return -1, errors.New("cannot launch") }),
	} {
		t.Run(name, func(t *testing.T) {
			repo, definition := experimentCase(t)
			result, err := Execute(context.Background(), repo, Request{Case: definition, Command: []string{"runner"}, Runner: runner})
			if name == "incomplete" && (err == nil || !strings.Contains(err.Error(), "cannot launch")) {
				t.Fatalf("infrastructure error = %v", err)
			}
			if name == "failed" && (err != nil || result.Status != cases.AttemptStatusFailed || result.ExitCode == nil || *result.ExitCode != 7) {
				t.Fatalf("failed result = %#v, %v", result, err)
			}
			if name == "incomplete" && (result.Status != cases.AttemptStatusIncomplete || result.PostCommit == "") {
				t.Fatalf("incomplete result = %#v", result)
			}
			projection, rebuildErr := cases.Rebuild(repo)
			if rebuildErr != nil {
				t.Fatal(rebuildErr)
			}
			updated, _ := projection.Case(definition.ID)
			if len(updated.AttemptLinks) != 1 || updated.AttemptLinks[0].Result == nil || updated.AttemptLinks[0].Result.Status != result.Status {
				t.Fatalf("durable result = %#v", updated.AttemptLinks)
			}
		})
	}
}

func TestCompareUsesImmutableCaseBaseAndIncludesRequestedPatch(t *testing.T) {
	repo, definition := experimentCase(t)
	writeResult := func(content string) Result {
		result, err := Execute(context.Background(), repo, Request{Case: definition, Command: []string{"runner"}, Runner: runnerFunc(func(_ context.Context, root string, _ []string, _ []string) (int, error) {
			return 0, os.WriteFile(filepath.Join(root, "app.txt"), []byte(content), 0o644)
		})})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := writeResult("first\n")
	second := writeResult("second\nwith another line\n")
	if _, err := cases.SelectAttempt(repo, definition.ID, second.AttemptID); err != nil {
		t.Fatal(err)
	}
	comparison, err := Compare(repo, definition.ID, first.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comparison.Attempts) != 2 || comparison.BaseCommit != definition.Readiness.Base.CommitSHA {
		t.Fatalf("comparison = %#v", comparison)
	}
	byID := make(map[primitives.AttemptID]AttemptComparison)
	for _, attempt := range comparison.Attempts {
		byID[attempt.AttemptID] = attempt
	}
	if byID[first.AttemptID].Patch == "" || !strings.Contains(byID[first.AttemptID].Patch, "+first") {
		t.Fatalf("first patch = %q", byID[first.AttemptID].Patch)
	}
	if !byID[second.AttemptID].Selected || byID[second.AttemptID].Additions != 2 || byID[second.AttemptID].Deletions != 1 {
		t.Fatalf("second comparison = %#v", byID[second.AttemptID])
	}
}

func TestApplyPreviewsThenRestoresSelectedAttemptWithSafetyCheckpoint(t *testing.T) {
	repo, definition := experimentCase(t)
	result, err := Execute(context.Background(), repo, Request{Case: definition, Command: []string{"runner"}, Runner: runnerFunc(func(_ context.Context, root string, _ []string, _ []string) (int, error) {
		return 0, os.WriteFile(filepath.Join(root, "app.txt"), []byte("applied result\n"), 0o644)
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cases.SelectAttempt(repo, definition.ID, result.AttemptID); err != nil {
		t.Fatal(err)
	}
	preview, err := Apply(repo, ApplyRequest{CaseID: definition.ID, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.DryRun || len(preview.Changes) != 1 || preview.SafetyRef != "" {
		t.Fatalf("preview = %#v", preview)
	}
	data, _ := os.ReadFile(filepath.Join(repo.WorkspaceRoot.String(), "app.txt"))
	if string(data) != "before\n" {
		t.Fatalf("dry-run changed workspace: %q", data)
	}
	applied, err := Apply(repo, ApplyRequest{CaseID: definition.ID})
	if err != nil {
		t.Fatal(err)
	}
	if applied.DryRun || applied.SafetyRef == "" || applied.SafetyCommit == "" {
		t.Fatalf("applied result = %#v", applied)
	}
	data, _ = os.ReadFile(filepath.Join(repo.WorkspaceRoot.String(), "app.txt"))
	if string(data) != "applied result\n" {
		t.Fatalf("applied workspace = %q", data)
	}
	safetyData, exists, err := repo.CommitFileBytesIfExists(applied.SafetyCommit, "app.txt")
	if err != nil || !exists || string(safetyData) != "before\n" {
		t.Fatalf("apply safety = %q %t %v", safetyData, exists, err)
	}
	projection, err := cases.Rebuild(repo)
	if err != nil {
		t.Fatal(err)
	}
	updated, _ := projection.Case(definition.ID)
	if len(updated.Applications) != 1 || updated.Applications[0].AttemptID != result.AttemptID {
		t.Fatalf("applications = %#v", updated.Applications)
	}
	second, err := Execute(context.Background(), repo, Request{Case: definition, Command: []string{"runner"}, Runner: runnerFunc(func(_ context.Context, root string, _ []string, _ []string) (int, error) {
		return 0, os.WriteFile(filepath.Join(root, "app.txt"), []byte("second result\n"), 0o644)
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cases.SelectAttempt(repo, definition.ID, second.AttemptID); err != nil {
		t.Fatal(err)
	}
	projection, err = cases.Rebuild(repo)
	if err != nil {
		t.Fatalf("Rebuild after reselection: %v", err)
	}
	updated, _ = projection.Case(definition.ID)
	if updated.Selection == nil || updated.Selection.AttemptID != second.AttemptID || len(updated.Applications) != 1 || updated.Applications[0].AttemptID != result.AttemptID {
		t.Fatalf("reselected Case history = selection %#v applications %#v", updated.Selection, updated.Applications)
	}
}

func TestApplyRejectsDivergedWorkspaceBeforeCreatingSafetyState(t *testing.T) {
	repo, definition := experimentCase(t)
	result, err := Execute(context.Background(), repo, Request{Case: definition, Command: []string{"runner"}, Runner: runnerFunc(func(_ context.Context, root string, _ []string, _ []string) (int, error) {
		return 0, os.WriteFile(filepath.Join(root, "app.txt"), []byte("candidate\n"), 0o644)
	})})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo.WorkspaceRoot.String(), "app.txt"), []byte("unrelated live work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(repo, ApplyRequest{CaseID: definition.ID, AttemptID: result.AttemptID}); err == nil || !strings.Contains(err.Error(), "exact-base only") {
		t.Fatalf("diverged apply error = %v", err)
	}
	projection, err := cases.Rebuild(repo)
	if err != nil {
		t.Fatal(err)
	}
	updated, _ := projection.Case(definition.ID)
	if updated.Selection != nil || len(updated.Applications) != 0 {
		t.Fatalf("rejected apply wrote case decisions: %#v %#v", updated.Selection, updated.Applications)
	}
}

func TestApplyRecoveryFinalizesCaseApplicationFromRollbackJournal(t *testing.T) {
	repo, definition := experimentCase(t)
	result, err := Execute(context.Background(), repo, Request{Case: definition, Command: []string{"runner"}, Runner: runnerFunc(func(_ context.Context, root string, _ []string, _ []string) (int, error) {
		return 0, os.WriteFile(filepath.Join(root, "app.txt"), []byte("recovered application\n"), 0o644)
	})})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := cases.SelectAttempt(repo, definition.ID, result.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	link := selected.AttemptLinks[0]
	plan, err := repo.PlanRestoreCommit(result.PostCommit)
	if err != nil {
		t.Fatal(err)
	}
	safety, err := repo.CreateSnapshotRef("refs/agent-vcs/rollback-safety/test/apply-recovery", "apply recovery safety")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RestoreCommit(result.PostCommit); err != nil {
		t.Fatal(err)
	}
	target, err := primitives.NewTargetRef(link.Execution.SessionID, link.Execution.TurnID, primitives.CheckpointPhasePost)
	if err != nil {
		t.Fatal(err)
	}
	journal := rollbackengine.Journal{
		Version: 1, State: "restored", RestorePhase: "restored", Mode: primitives.RollbackModeCheckpoint.String(),
		Target: target.String(), CheckpointRef: result.PostRef.String(), TargetCommitSHA: result.PostCommit.String(),
		SafetyRef: safety.Ref, SafetyCommitSHA: safety.Commit.String(), Changes: plan.Changes,
		CaseApplication: &rollbackengine.ApplicationMetadata{CaseID: definition.ID, AttemptID: result.AttemptID, PostCommit: result.PostCommit},
		RepoID:          repo.RepoID, StoreID: repo.StoreID, WorktreeID: repo.WorktreeID, WorkspaceRoot: repo.WorkspaceRoot.String(),
	}
	encoded, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rollbackengine.JournalPath(repo), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rollbackengine.New(repo).ResumeRecovery(); err != nil {
		t.Fatalf("ResumeRecovery: %v", err)
	}
	if _, err := os.Stat(rollbackengine.JournalPath(repo)); !os.IsNotExist(err) {
		t.Fatalf("rollback journal remains after finalization: %v", err)
	}
	projection, err := cases.Rebuild(repo)
	if err != nil {
		t.Fatal(err)
	}
	updated, _ := projection.Case(definition.ID)
	if len(updated.Applications) != 1 || updated.Applications[0].AttemptID != result.AttemptID || updated.Applications[0].SafetyRef != safety.Ref {
		t.Fatalf("recovered application = %#v", updated.Applications)
	}
}

func TestExecuteRunsFrozenCaseVerifiersAgainstPostCheckpoint(t *testing.T) {
	t.Setenv("TURNAL_FORK_VERIFY_EXPECT", "verified result\n")
	verifierConfig := fmt.Sprintf("version = 1\n[[verify]]\nname = \"result-content\"\ncommand = %q\nargs = [\"-test.run=^TestForkVerifierHelper$\"]\ntimeout = \"10s\"\n", os.Args[0])
	repo, definition := experimentCaseWithConfig(t, verifierConfig)
	result, err := Execute(context.Background(), repo, Request{Case: definition, Command: []string{"runner"}, Runner: runnerFunc(func(_ context.Context, root string, _ []string, _ []string) (int, error) {
		return 0, os.WriteFile(filepath.Join(root, "app.txt"), []byte("verified result\n"), 0o644)
	})})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verification == nil || !result.Verification.Successful() || result.Verification.Summary.Passed != 1 {
		t.Fatalf("verification = %#v", result.Verification)
	}
	projection, err := cases.Rebuild(repo)
	if err != nil {
		t.Fatal(err)
	}
	updated, _ := projection.Case(definition.ID)
	if updated.AttemptLinks[0].Result.Verification == nil || updated.AttemptLinks[0].Result.Verification.Target.Commit != result.PostCommit.String() {
		t.Fatalf("durable verification = %#v", updated.AttemptLinks[0].Result.Verification)
	}
}

func TestForkVerifierHelper(t *testing.T) {
	want := os.Getenv("TURNAL_FORK_VERIFY_EXPECT")
	if want == "" {
		return
	}
	data, err := os.ReadFile("app.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("verified app.txt = %q, want %q", data, want)
	}
}

func TestExecRunnerScrubsInheritedGitEnvironment(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh required")
	}
	root := t.TempDir()
	output := filepath.Join(root, "environment.txt")
	runner := ExecRunner{Env: []string{"PATH=" + os.Getenv("PATH"), "GIT_DIR=/danger", "GIT_WORK_TREE=/danger", "PWD=/stale"}}
	code, err := runner.Run(context.Background(), root, []string{"sh", "-c", `printf '%s|%s|%s' "${GIT_DIR-unset}" "${GIT_WORK_TREE-unset}" "$PWD" > environment.txt`}, []string{EnvRunID + "=run_11111111111111111111111111111111"})
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "unset|unset|"+root {
		t.Fatalf("child environment = %q", data)
	}
}

func TestRecoverAbandonedTerminalizesAttemptAndRemovesManagedWorkspace(t *testing.T) {
	repo, definition := experimentCase(t)
	runID, err := primitives.NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := forkSessionID(runID)
	if err != nil {
		t.Fatal(err)
	}
	release, err := runs.Begin(repo, runID, sessionID, []string{"runner"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runs.LinkCapture(repo, runID, runs.CaptureWrapper, sessionID, primitives.AdapterCodex); err != nil {
		release()
		t.Fatal(err)
	}
	gitSync := false
	recorder := turnevents.Recorder{Log: repo.EventLog(), Manager: turns.NewManager(repo), Adapter: primitives.AdapterCodex}
	recorder.Manager.GitSyncEnabled = &gitSync
	started, err := recorder.Start(sessionID, 1)
	if err != nil {
		release()
		t.Fatal(err)
	}
	attemptID, err := runs.EnsureWrapperAttempt(repo, runID, sessionID, started.TurnID, primitives.AdapterCodex)
	if err != nil {
		release()
		t.Fatal(err)
	}
	workspace, err := createManagedForkWorkspace(repo, runID)
	if err != nil {
		release()
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "leaked.txt"), []byte("temporary\n"), 0o600); err != nil {
		release()
		t.Fatal(err)
	}
	if _, err := cases.LinkAttempt(repo, cases.LinkAttemptRequest{CaseID: definition.ID, RunID: runID, AttemptID: attemptID, Command: []string{"runner"}, Workspace: workspace}); err != nil {
		release()
		t.Fatal(err)
	}
	finished, err := recorder.Finish(sessionID, started.TurnID)
	if err != nil {
		release()
		t.Fatal(err)
	}
	release()

	if err := RecoverAbandoned(repo); err != nil {
		t.Fatalf("RecoverAbandoned: %v", err)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("abandoned workspace remains: %v", err)
	}
	projection, err := cases.Rebuild(repo)
	if err != nil {
		t.Fatal(err)
	}
	updated, _ := projection.Case(definition.ID)
	if len(updated.AttemptLinks) != 1 || updated.AttemptLinks[0].Result == nil || updated.AttemptLinks[0].Result.Status != cases.AttemptStatusIncomplete || updated.AttemptLinks[0].Result.PostRef != finished.Post.Ref {
		t.Fatalf("recovered attempt = %#v", updated.AttemptLinks)
	}
	comparison, err := Compare(repo, definition.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(comparison.Attempts) != 1 || comparison.Attempts[0].Status != cases.AttemptStatusIncomplete || comparison.Attempts[0].PostRef != finished.Post.Ref {
		t.Fatalf("recovered comparison = %#v", comparison.Attempts)
	}
}

func TestRecoverAbandonedRemovesUnlinkedManagedWorkspace(t *testing.T) {
	repo, _ := experimentCase(t)
	runID, _ := primitives.NewRunID()
	workspace, err := createManagedForkWorkspace(repo, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecoverAbandoned(repo); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("unlinked managed workspace remains: %v", err)
	}
}

func TestRecoverRemovesTerminalNonKeptWorkspace(t *testing.T) {
	repo, definition := experimentCase(t)
	result, err := Execute(context.Background(), repo, Request{Case: definition, Command: []string{"runner"}, Runner: runnerFunc(func(context.Context, string, []string, []string) (int, error) { return 0, nil })})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := createManagedForkWorkspace(repo, result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecoverAbandoned(repo); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("terminal non-kept workspace remains: %v", err)
	}
}

func TestDeletedCasePreservesExplicitlyKeptWorkspace(t *testing.T) {
	repo, definition := experimentCase(t)
	result, err := Execute(context.Background(), repo, Request{Case: definition, Command: []string{"runner"}, Keep: true, Runner: runnerFunc(func(context.Context, string, []string, []string) (int, error) { return 0, nil })})
	if err != nil {
		t.Fatal(err)
	}
	if !result.WorkspaceKept || result.Workspace == "" {
		t.Fatalf("kept result = %#v", result)
	}
	marker := keepMarkerPath(repo, result.RunID)
	t.Cleanup(func() { _ = os.RemoveAll(result.Workspace); _ = os.Remove(marker) })
	if _, err := cases.Delete(repo, definition.ID); err != nil {
		t.Fatal(err)
	}
	if err := RecoverAbandoned(repo); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(result.Workspace); err != nil {
		t.Fatalf("kept workspace removed after Case deletion: %v", err)
	}
}

func TestVerificationCleanupFailureTerminalizesAttempt(t *testing.T) {
	verifierConfig := fmt.Sprintf("version = 1\n[[verify]]\nname = \"pass\"\ncommand = %q\nargs = [\"-test.run=^TestForkVerifierHelper$\"]\ntimeout = \"10s\"\n", os.Args[0])
	repo, definition := experimentCaseWithConfig(t, verifierConfig)
	originalRemove := removeManagedWorkspace
	removeManagedWorkspace = func(path string) error {
		if strings.HasPrefix(filepath.Base(path), "verify-") {
			return errors.New("injected cleanup failure")
		}
		return os.RemoveAll(path)
	}
	defer func() { removeManagedWorkspace = originalRemove }()
	result, err := Execute(context.Background(), repo, Request{Case: definition, Command: []string{"runner"}, Runner: runnerFunc(func(context.Context, string, []string, []string) (int, error) { return 0, nil })})
	if err == nil || !strings.Contains(err.Error(), "injected cleanup failure") || result.Status != cases.AttemptStatusIncomplete {
		t.Fatalf("cleanup failure result = %#v, %v", result, err)
	}
}

func TestValidateExecutionSymlinksRejectsWorkspaceEscapes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(outside, filepath.Join(root, "absolute")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := validateExecutionSymlinks(root); err == nil || !strings.Contains(err.Error(), "absolute target") {
		t.Fatalf("absolute symlink error = %v", err)
	}
	if err := os.Remove(filepath.Join(root, "absolute")); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(filepath.Join("..", "..", "outside"), filepath.Join(root, "nested", "relative")); err != nil {
		t.Fatal(err)
	}
	if err := validateExecutionSymlinks(root); err == nil || !strings.Contains(err.Error(), "escapes the isolated workspace") {
		t.Fatalf("relative symlink error = %v", err)
	}
	if err := os.Remove(filepath.Join(root, "nested", "relative")); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "target.txt"), []byte("safe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "target.txt"), filepath.Join(root, "nested", "safe")); err != nil {
		t.Fatal(err)
	}
	if err := validateExecutionSymlinks(root); err != nil {
		t.Fatalf("safe in-root symlink rejected: %v", err)
	}
}

func experimentCase(t *testing.T) (*checkpoint.Repo, cases.Case) {
	return experimentCaseWithConfig(t, "")
}

func experimentCaseWithConfig(t *testing.T, workspaceConfig string) (*checkpoint.Repo, cases.Case) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required")
	}
	root, _ := primitives.ParseWorkspaceRoot(t.TempDir())
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	if workspaceConfig != "" {
		if err := os.WriteFile(filepath.Join(repo.MetadataDir, "config.toml"), []byte(workspaceConfig), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root.String(), "app.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessionID, _ := primitives.ParseSessionID("experiment-source")
	turnID, _ := primitives.NewTurnID(1)
	if _, err := repo.EventLog().Append(eventlog.AppendInput{SessionID: sessionID, Type: primitives.EventTypeSessionStart, Adapter: primitives.AdapterCodex, Payload: json.RawMessage(`{"provider_session_id":"source"}`)}); err != nil {
		t.Fatal(err)
	}
	gitSync := false
	recorder := turnevents.Recorder{Log: repo.EventLog(), Manager: turns.NewManager(repo), Adapter: primitives.AdapterCodex}
	recorder.Manager.GitSyncEnabled = &gitSync
	if _, err := recorder.Start(sessionID, turnID); err != nil {
		t.Fatal(err)
	}
	prompt, _ := json.Marshal(map[string]string{"text": "Fix the parser"})
	if _, err := repo.EventLog().Append(eventlog.AppendInput{SessionID: sessionID, TurnID: &turnID, Type: primitives.EventTypePromptUser, Adapter: primitives.AdapterCodex, Payload: prompt}); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.Finish(sessionID, turnID); err != nil {
		t.Fatal(err)
	}
	created, err := cases.Create(repo, cases.CreateRequest{SessionID: sessionID, TurnID: turnID})
	if err != nil {
		t.Fatal(err)
	}
	return repo, created.Case
}

func environmentMap(entries []string) map[string]string {
	result := make(map[string]string)
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			result[name] = value
		}
	}
	return result
}
