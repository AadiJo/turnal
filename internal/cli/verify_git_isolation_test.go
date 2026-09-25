package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/verifier"
)

// Ways a checkpoint check can reach the project's Git repository, or lose
// Git behavior it legitimately needs, each covered below through the real
// `turnal verify` command:
//
//   - the evaluation directory lives under .turnal/tmp inside the workspace,
//     so a check's `git` walks up from it and finds the project repository,
//     then reads it (wrong answers) or writes it (index, refs, stash);
//   - a check that runs from a parent of the evaluation root (`git -C ..`)
//     starts discovery above a single ceiling and walks into the project;
//   - inherited GIT_DIR, GIT_WORK_TREE, or GIT_INDEX_FILE, which Git sets for
//     hooks, point `git` at the project repository from any directory;
//   - Windows environment names are case-insensitive, so a mixed-case
//     spelling such as Git_Dir slips past a case-sensitive filter;
//   - Git splits GIT_CEILING_DIRECTORIES on the path-list separator with no
//     escaping, so a workspace path containing one silently disables the
//     ceiling;
//   - the isolation drops Git settings that do not select a repository
//     (author identity, SSH command), so checks behave differently than live;
//   - the isolation bounds discovery too tightly and breaks a repository the
//     check creates inside the evaluation root;
//   - the isolation leaks into live verification, which legitimately runs
//     against the project repository with the caller's Git environment.

func TestVerifyCheckpointChecksCannotReachTheProjectRepository(t *testing.T) {
	for _, test := range []struct {
		name        string
		environment func(root string) map[string]string
		windowsOnly bool
	}{
		{name: "discovery", environment: func(string) map[string]string { return nil }},
		{name: "hook variables", environment: func(root string) map[string]string {
			return map[string]string{
				"GIT_DIR":        filepath.Join(root, ".git"),
				"GIT_WORK_TREE":  root,
				"GIT_INDEX_FILE": filepath.Join(root, ".git", "index"),
			}
		}},
		{name: "mixed-case hook variables", windowsOnly: true, environment: func(root string) map[string]string {
			return map[string]string{
				"Git_Dir":       filepath.Join(root, ".git"),
				"Git_Work_Tree": root,
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.windowsOnly && runtime.GOOS != "windows" {
				t.Skip("environment names are case-sensitive on this platform")
			}
			repo, sessionID, turnID := cliRecordedVerifyRepo(t)
			root := repo.WorkspaceRoot.String()
			writeGitVerifyConfig(t, repo, []gitVerifyEntry{
				{Name: "toplevel", Args: []string{"rev-parse", "--show-toplevel"}},
				{Name: "stage-all", Args: []string{"add", "-A"}},
				{Name: "parent-toplevel", Args: []string{"-C", "..", "rev-parse", "--show-toplevel"}},
				{Name: "ancestor-stage-all", Args: []string{"-C", "../../..", "add", "-A"}},
			})
			gitBefore := captureUserGitState(t, root)
			for name, value := range test.environment(root) {
				t.Setenv(name, value)
			}

			output, err := executeVerifyCommand(t, root, "verify", sessionID.String()+":"+turnID.String()+":pre", "--json")
			if code, ok := commandExitCode(err); !ok || code != verifyFailureExitCode {
				t.Fatalf("exit = %d %t err = %v\n%s", code, ok, err, output)
			}
			for _, check := range decodeVerifyReport(t, output).Checks {
				if check.Status != verifier.StatusFailed || check.ExitCode == nil || *check.ExitCode != 128 {
					t.Fatalf("check %s reached a repository: %#v", check.Name, check)
				}
			}
			if gitAfter := captureUserGitState(t, root); gitAfter != gitBefore {
				t.Fatal("a checkpoint check changed the project's Git state")
			}
			assertVerifyTempEmpty(t, repo)
		})
	}
}

func TestVerifyCheckpointRefusesPathsGitCannotBound(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "odd"+string(os.PathListSeparator)+"name")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Skipf("this filesystem cannot hold the path-list separator in a name: %v", err)
	}
	repo, sessionID, turnID := cliRecordedVerifyRepoAt(t, directory)
	root := repo.WorkspaceRoot.String()
	writeGitVerifyConfig(t, repo, []gitVerifyEntry{{Name: "stage-all", Args: []string{"add", "-A"}}})
	gitBefore := captureUserGitState(t, root)

	output, err := executeVerifyCommand(t, root, "verify", sessionID.String()+":"+turnID.String()+":pre", "--json")
	if err == nil || !strings.Contains(err.Error(), "path-list separator") {
		t.Fatalf("verify error = %v\n%s", err, output)
	}
	if _, classified := commandExitCode(err); classified {
		t.Fatalf("refusal reported as a check outcome: %v", err)
	}
	if gitAfter := captureUserGitState(t, root); gitAfter != gitBefore {
		t.Fatal("a checkpoint check changed the project's Git state")
	}
	assertVerifyTempEmpty(t, repo)
}

func TestVerifyCheckpointChecksKeepGitBehaviorInsideTheEvaluation(t *testing.T) {
	repo, sessionID, turnID := cliRecordedVerifyRepo(t)
	writeGitVerifyConfig(t, repo, []gitVerifyEntry{
		{Name: "init", Args: []string{"init", "-q"}},
		{Name: "toplevel", Args: []string{"rev-parse", "--show-toplevel"}},
		{Name: "identity", Args: []string{"var", "GIT_AUTHOR_IDENT"}},
	})
	t.Setenv("GIT_AUTHOR_NAME", "turnal-probe")
	t.Setenv("GIT_AUTHOR_EMAIL", "probe@example.invalid")

	output, err := executeVerifyCommand(t, repo.WorkspaceRoot.String(), "verify", sessionID.String()+":"+turnID.String()+":post", "--json")
	if err != nil {
		t.Fatalf("verify: %v\n%s", err, output)
	}
	checks := decodeVerifyReport(t, output).Checks
	toplevel := strings.TrimSpace(checks[1].Stdout)
	if !strings.HasSuffix(toplevel, "/workspace") || !strings.Contains(toplevel, "evaluation-") {
		t.Fatalf("toplevel = %q, want the materialized evaluation root", toplevel)
	}
	if !strings.Contains(checks[2].Stdout, "turnal-probe <probe@example.invalid>") {
		t.Fatalf("author identity was not inherited: %q", checks[2].Stdout)
	}
}

func TestVerifyLiveWorkspaceKeepsTheCallersGitEnvironment(t *testing.T) {
	repo := cliVerifyRepoWithUserGit(t)
	root := repo.WorkspaceRoot.String()
	initializeUserGitFixture(t, root)
	writeGitVerifyConfig(t, repo, []gitVerifyEntry{{Name: "git-dir", Args: []string{"rev-parse", "--absolute-git-dir"}}})

	output, err := executeVerifyCommand(t, root, "verify", "--json")
	if err != nil {
		t.Fatalf("live verify: %v\n%s", err, output)
	}
	assertSamePath(t, decodeVerifyReport(t, output).Checks[0].Stdout, filepath.Join(root, ".git"))

	// A caller's GIT_DIR must still select the repository for live checks.
	other := t.TempDir()
	runUserGit(t, other, "init", "-q")
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	output, err = executeVerifyCommand(t, root, "verify", "--json")
	if err != nil {
		t.Fatalf("live verify with GIT_DIR: %v\n%s", err, output)
	}
	assertSamePath(t, decodeVerifyReport(t, output).Checks[0].Stdout, filepath.Join(other, ".git"))
}

func assertSamePath(t *testing.T, reported, want string) {
	t.Helper()
	got, err := filepath.EvalSymlinks(filepath.FromSlash(strings.TrimSpace(reported)))
	if err != nil {
		t.Fatalf("resolve reported path %q: %v", reported, err)
	}
	want, err = filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatalf("resolve expected path: %v", err)
	}
	if !strings.EqualFold(got, want) {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

type gitVerifyEntry struct {
	Name string
	Args []string
}

// writeGitVerifyConfig declares checks that run the real git executable.
func writeGitVerifyConfig(t *testing.T, repo *checkpoint.Repo, entries []gitVerifyEntry) {
	t.Helper()
	var body strings.Builder
	body.WriteString("version = 1\n")
	for _, entry := range entries {
		args := make([]string, len(entry.Args))
		for index, arg := range entry.Args {
			args[index] = `"` + arg + `"`
		}
		body.WriteString("\n[[verify]]\nname = \"" + entry.Name + "\"\ncommand = \"git\"\nargs = [" + strings.Join(args, ", ") + "]\ntimeout = \"30s\"\n")
	}
	if err := os.WriteFile(filepath.Join(repo.MetadataDir, "config.toml"), []byte(body.String()), 0o600); err != nil {
		t.Fatalf("write verifier config: %v", err)
	}
}

func decodeVerifyReport(t *testing.T, output string) verifier.Report {
	t.Helper()
	var report verifier.Report
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("decode verify report: %v\n%s", err, output)
	}
	return report
}
