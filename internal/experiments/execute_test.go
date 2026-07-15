package experiments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/cases"
	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
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
