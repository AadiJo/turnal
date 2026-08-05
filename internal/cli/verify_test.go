package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/turnevents"
	"github.com/AadiJo/turnal/internal/turns"
	"github.com/AadiJo/turnal/internal/verifier"
)

func TestParseVerifyTarget(t *testing.T) {
	for _, value := range []string{"demo:1:pre", "demo:turn:1:post"} {
		sessionID, turnID, phase, err := parseVerifyTarget(value)
		if err != nil {
			t.Fatalf("parseVerifyTarget(%q): %v", value, err)
		}
		if sessionID != "demo" || turnID.Uint64() != 1 || (phase != primitives.CheckpointPhasePre && phase != primitives.CheckpointPhasePost) {
			t.Fatalf("parseVerifyTarget(%q) = %s %s %s", value, sessionID, turnID, phase)
		}
	}
	for _, value := range []string{"", "demo:1", "demo:turn:1", "demo:1:middle"} {
		if _, _, _, err := parseVerifyTarget(value); err == nil {
			t.Fatalf("parseVerifyTarget(%q) succeeded", value)
		}
	}
}

func TestVerifyCurrentWorkspacePreservesTurnalAndUserGitState(t *testing.T) {
	repo := cliVerifyRepoWithUserGit(t)
	initializeUserGitFixture(t, repo.WorkspaceRoot.String())
	writeVerifyConfig(t, repo, []verifyConfigEntry{{Name: "workspace", Mode: "inspect", Args: []string{"app.txt", "working copy\n"}}})

	refsBefore, err := repo.ListAllPrivateRefs()
	if err != nil {
		t.Fatalf("ListAllPrivateRefs before: %v", err)
	}
	gitBefore := captureUserGitState(t, repo.WorkspaceRoot.String())
	workspaceBefore := readCLIFile(t, repo.WorkspaceRoot.String(), "app.txt")

	output, runErr := executeVerifyCommand(t, repo.WorkspaceRoot.String(), "verify", "--json")
	if runErr != nil {
		t.Fatalf("verify current workspace: %v", runErr)
	}
	var report verifier.Report
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, output)
	}
	if report.SchemaVersion != verifier.SchemaVersion || report.Target.Kind != verifier.TargetLiveWorkspace || !report.Target.Mutable || report.Target.Reproducible {
		t.Fatalf("target = %#v", report.Target)
	}
	if report.Target.WorktreeID != repo.WorktreeID.String() || report.Checks[0].Status != verifier.StatusPassed {
		t.Fatalf("report = %#v", report)
	}
	if !strings.Contains(report.Checks[0].Stdout, repo.WorkspaceRoot.String()) {
		t.Fatalf("helper did not run in workspace: %q", report.Checks[0].Stdout)
	}

	refsAfter, err := repo.ListAllPrivateRefs()
	if err != nil {
		t.Fatalf("ListAllPrivateRefs after: %v", err)
	}
	if strings.Join(refsBefore, "\n") != strings.Join(refsAfter, "\n") {
		t.Fatalf("private refs changed: before=%v after=%v", refsBefore, refsAfter)
	}
	if got := readCLIFile(t, repo.WorkspaceRoot.String(), "app.txt"); got != workspaceBefore {
		t.Fatalf("workspace file changed: got %q want %q", got, workspaceBefore)
	}
	gitAfter := captureUserGitState(t, repo.WorkspaceRoot.String())
	if gitAfter != gitBefore {
		t.Fatalf("user Git state changed\nbefore:\n%s\nafter:\n%s", gitBefore, gitAfter)
	}
}

func TestVerifyRecordedPreAndPostCheckpoints(t *testing.T) {
	repo, sessionID, turnID := cliRecordedVerifyRepo(t)
	gitBefore := captureUserGitState(t, repo.WorkspaceRoot.String())

	for _, test := range []struct {
		phase primitives.CheckpointPhase
		want  string
	}{
		{phase: primitives.CheckpointPhasePre, want: "before\n"},
		{phase: primitives.CheckpointPhasePost, want: "after\n"},
	} {
		t.Run(test.phase.String(), func(t *testing.T) {
			writeVerifyConfig(t, repo, []verifyConfigEntry{{Name: "content", Mode: "inspect", Args: []string{"app.txt", test.want}}})
			target := fmt.Sprintf("%s:%s:%s", sessionID, turnID, test.phase)
			output, runErr := executeVerifyCommand(t, repo.WorkspaceRoot.String(), "verify", target, "--json")
			if runErr != nil {
				t.Fatalf("verify checkpoint: %v", runErr)
			}
			var report verifier.Report
			if err := json.Unmarshal([]byte(output), &report); err != nil {
				t.Fatalf("decode report: %v\n%s", err, output)
			}
			if report.Target.Kind != verifier.TargetCheckpoint || report.Target.Phase != test.phase.String() || report.Target.CheckpointRef == "" || report.Target.Commit == "" {
				t.Fatalf("target = %#v", report.Target)
			}
			if report.Checks[0].Status != verifier.StatusPassed {
				t.Fatalf("check = %#v", report.Checks[0])
			}
			assertVerifyTempEmpty(t, repo)
		})
	}
	if got := readCLIFile(t, repo.WorkspaceRoot.String(), "app.txt"); got != "after\n" {
		t.Fatalf("active workspace changed to %q", got)
	}
	if gitAfter := captureUserGitState(t, repo.WorkspaceRoot.String()); gitAfter != gitBefore {
		t.Fatalf("user Git state changed during checkpoint verification\nbefore:\n%s\nafter:\n%s", gitBefore, gitAfter)
	}
}

func TestVerifyRunsAllChecksAndReturnsAggregateExit(t *testing.T) {
	repo := cliVerifyRepo(t)
	writeCLIFile(t, repo.WorkspaceRoot.String(), "app.txt", "content\n")
	writeVerifyConfig(t, repo, []verifyConfigEntry{
		{Name: "failure", Mode: "fail"},
		{Name: "later-pass", Mode: "inspect", Args: []string{"app.txt", "content\n"}},
	})
	output, err := executeVerifyCommand(t, repo.WorkspaceRoot.String(), "verify")
	if err == nil {
		t.Fatal("verify succeeded, want aggregate failure")
	}
	code, ok := commandExitCode(err)
	if !ok || code != verifyFailureExitCode {
		t.Fatalf("exit code = %d %t, want %d true; err=%v", code, ok, verifyFailureExitCode, err)
	}
	for _, want := range []string{"1 passed, 1 failed", "FAIL", "failure", "PASS", "later-pass"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestVerifyRejectsTargetBeforeLaunchingCommands(t *testing.T) {
	repo, sessionID, _ := cliRecordedVerifyRepo(t)
	sentinel := filepath.Join(t.TempDir(), "launched")
	writeVerifyConfig(t, repo, []verifyConfigEntry{{Name: "must-not-run", Mode: "touch", Args: []string{sentinel}}})
	_, err := executeVerifyCommand(t, repo.WorkspaceRoot.String(), "verify", sessionID.String()+":2:pre")
	if err == nil {
		t.Fatal("missing checkpoint verify succeeded")
	}
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Fatalf("verifier launched before target resolution: %v", statErr)
	}
}

func TestVerifyCancellationCleansCheckpointEvaluation(t *testing.T) {
	repo, sessionID, turnID := cliRecordedVerifyRepo(t)
	startedMarker := filepath.Join(t.TempDir(), "started")
	writeVerifyConfig(t, repo, []verifyConfigEntry{{Name: "wait", Mode: "wait", Args: []string{startedMarker}}})

	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(repo.WorkspaceRoot.String()); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(previous) }()
	t.Setenv("TURNAL_CONFIG", filepath.Join(t.TempDir(), "missing-global.toml"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := NewRootCmd()
	command.SetContext(ctx)
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"verify", fmt.Sprintf("%s:%s:post", sessionID, turnID)})
	cancelled := make(chan struct{})
	go func() {
		defer close(cancelled)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(startedMarker); err == nil {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()

	started := time.Now()
	err = command.Execute()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("verify error = %v, want context cancellation", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cancelled verify took %s", elapsed)
	}
	<-cancelled
	if _, err := os.Stat(startedMarker); err != nil {
		t.Fatalf("verifier did not start before cancellation: %v", err)
	}
	assertVerifyTempEmpty(t, repo)
}

func TestVerifyRequiresConfiguredVerifiers(t *testing.T) {
	repo := cliVerifyRepo(t)
	_, err := executeVerifyCommand(t, repo.WorkspaceRoot.String(), "verify")
	if err == nil || !strings.Contains(err.Error(), "no repository verifiers") {
		t.Fatalf("verify error = %v", err)
	}
}

type verifyConfigEntry struct {
	Name string
	Mode string
	Args []string
}

func writeVerifyConfig(t *testing.T, repo *checkpoint.Repo, entries []verifyConfigEntry) {
	t.Helper()
	executable, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	var body strings.Builder
	body.WriteString("version = 1\n")
	for _, entry := range entries {
		args := []string{"-test.run=^TestVerifyCLIHelperProcess$", "--", entry.Mode}
		args = append(args, entry.Args...)
		encodedArgs := make([]string, len(args))
		for index, arg := range args {
			encodedArgs[index] = fmt.Sprintf("%q", arg)
		}
		fmt.Fprintf(&body, "\n[[verify]]\nname = %q\ncommand = %q\nargs = [%s]\ntimeout = \"5s\"\n", entry.Name, executable, strings.Join(encodedArgs, ", "))
	}
	if err := os.WriteFile(filepath.Join(repo.MetadataDir, "config.toml"), []byte(body.String()), 0o600); err != nil {
		t.Fatalf("write verifier config: %v", err)
	}
}

func executeVerifyCommand(t *testing.T, root string, args ...string) (string, error) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	t.Setenv("TURNAL_CONFIG", filepath.Join(t.TempDir(), "missing-global.toml"))
	cmd := NewRootCmd()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return output.String(), err
}

func cliVerifyRepo(t *testing.T) *checkpoint.Repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	// Keep this test's store out of the developer's real project registry.
	t.Setenv("TURNAL_STATE_DIR", t.TempDir())
	root, err := primitives.ParseWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot: %v", err)
	}
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("checkpoint.Init: %v", err)
	}
	return repo
}

func cliVerifyRepoWithUserGit(t *testing.T) *checkpoint.Repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	// Keep this test's store out of the developer's real project registry.
	t.Setenv("TURNAL_STATE_DIR", t.TempDir())
	root, err := primitives.ParseWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot: %v", err)
	}
	runUserGit(t, root.String(), "init")
	runUserGit(t, root.String(), "config", "user.name", "Verifier Test")
	runUserGit(t, root.String(), "config", "user.email", "verifier@example.invalid")
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("checkpoint.Init: %v", err)
	}
	return repo
}

func cliRecordedVerifyRepo(t *testing.T) (*checkpoint.Repo, primitives.SessionID, primitives.TurnID) {
	t.Helper()
	repo := cliVerifyRepoWithUserGit(t)
	initializeUserGitFixture(t, repo.WorkspaceRoot.String())
	writeCLIFile(t, repo.WorkspaceRoot.String(), ".gitignore", "ignored/\n")
	writeCLIFile(t, repo.WorkspaceRoot.String(), "ignored/cache.txt", "ignored\n")
	writeCLIFile(t, repo.WorkspaceRoot.String(), ".env", "SECRET=value\n")
	writeCLIFile(t, repo.WorkspaceRoot.String(), "app.txt", "before\n")
	sessionID, _ := primitives.ParseSessionID("verify-cli")
	turnID, _ := primitives.NewTurnID(1)
	log := eventlog.OpenFor(repo.MetadataDir, repo.WorkspaceRoot.String(), repo.RepoID, repo.StoreID, repo.WorktreeID, repo.EventProducerID)
	recorder := turnevents.Recorder{Log: log, Manager: turns.NewManager(repo), Adapter: primitives.AdapterManual}
	if _, err := recorder.Start(sessionID, turnID); err != nil {
		t.Fatalf("start turn: %v", err)
	}
	writeCLIFile(t, repo.WorkspaceRoot.String(), "app.txt", "after\n")
	if _, err := recorder.Finish(sessionID, turnID); err != nil {
		t.Fatalf("finish turn: %v", err)
	}
	return repo, sessionID, turnID
}

func initializeUserGitFixture(t *testing.T, root string) {
	t.Helper()
	runUserGit(t, root, "init")
	runUserGit(t, root, "config", "user.name", "Verifier Test")
	runUserGit(t, root, "config", "user.email", "verifier@example.invalid")
	writeCLIFile(t, root, "app.txt", "committed\n")
	runUserGit(t, root, "add", "app.txt")
	runUserGit(t, root, "commit", "-m", "fixture")
	writeCLIFile(t, root, "app.txt", "staged\n")
	runUserGit(t, root, "add", "app.txt")
	writeCLIFile(t, root, "app.txt", "working copy\n")
}

func captureUserGitState(t *testing.T, root string) string {
	t.Helper()
	parts := []string{
		runUserGit(t, root, "symbolic-ref", "--short", "HEAD"),
		runUserGit(t, root, "rev-parse", "HEAD"),
		runUserGit(t, root, "diff", "--cached", "--binary"),
		runUserGit(t, root, "config", "--local", "--list", "--show-origin"),
	}
	index, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatalf("read user index: %v", err)
	}
	return strings.Join(parts, "\x00") + "\x00" + string(index)
}

func runUserGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = cleanCLIGitEnv(os.Environ())
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func cleanCLIGitEnv(environment []string) []string {
	cleaned := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		if ok && !strings.HasPrefix(key, "GIT_") {
			cleaned = append(cleaned, entry)
		}
	}
	return cleaned
}

func assertVerifyTempEmpty(t *testing.T, repo *checkpoint.Repo) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(repo.TmpDir, "verify"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read verify temp: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("verify temp is not empty: %#v", entries)
	}
}

func writeCLIFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readCLIFile(t *testing.T, root, relative string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatalf("read %s: %v", relative, err)
	}
	return string(data)
}

func TestVerifyCLIHelperProcess(t *testing.T) {
	separator := -1
	for index, arg := range os.Args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	mode := os.Args[separator+1]
	args := os.Args[separator+2:]
	switch mode {
	case "inspect":
		if len(args) != 2 {
			os.Exit(20)
		}
		data, err := os.ReadFile(args[0])
		if err != nil || string(data) != args[1] {
			fmt.Fprintf(os.Stderr, "content=%q err=%v want=%q", data, err, args[1])
			os.Exit(21)
		}
		cwd, _ := os.Getwd()
		fmt.Fprintln(os.Stdout, cwd)
	case "fail":
		os.Exit(17)
	case "touch":
		if len(args) != 1 || os.WriteFile(args[0], []byte(time.Now().String()), 0o600) != nil {
			os.Exit(22)
		}
	case "wait":
		if len(args) != 1 || os.WriteFile(args[0], []byte("started"), 0o600) != nil {
			os.Exit(24)
		}
		time.Sleep(30 * time.Second)
	default:
		os.Exit(23)
	}
	os.Exit(0)
}
