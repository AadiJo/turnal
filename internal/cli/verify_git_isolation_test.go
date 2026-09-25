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

// Ways a checkpoint check can reach the project's Git repository, each
// covered below through the real `turnal verify` command:
//
//   - the evaluation directory lives under .turnal/tmp inside the workspace,
//     so a check's `git` walks up from it and finds the project repository,
//     then reads it (wrong answers) or writes it (index, refs, stash);
//   - inherited GIT_DIR, GIT_WORK_TREE, or GIT_INDEX_FILE, which Git sets for
//     hooks, point `git` at the project repository from any directory;
//   - Windows environment names are case-insensitive, so a mixed-case
//     spelling such as Git_Dir slips past a case-sensitive filter;
//   - the isolation bounds discovery too tightly and breaks a repository the
//     check creates inside the evaluation root;
//   - the isolation leaks into live verification, which legitimately runs
//     against the project repository.

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
			})
			gitBefore := captureUserGitState(t, root)
			for name, value := range test.environment(root) {
				t.Setenv(name, value)
			}

			output, err := executeVerifyCommand(t, root, "verify", sessionID.String()+":"+turnID.String()+":pre", "--json")
			if code, ok := commandExitCode(err); !ok || code != verifyFailureExitCode {
				t.Fatalf("exit = %d %t err = %v\n%s", code, ok, err, output)
			}
			report := decodeVerifyReport(t, output)
			for _, check := range report.Checks {
				if check.Status != verifier.StatusFailed || check.ExitCode == nil || *check.ExitCode != 128 {
					t.Fatalf("check %s reached a repository: %#v", check.Name, check)
				}
				if strings.Contains(check.Stdout, filepath.ToSlash(root)) {
					t.Fatalf("check %s printed the project root: %q", check.Name, check.Stdout)
				}
			}
			if gitAfter := captureUserGitState(t, root); gitAfter != gitBefore {
				t.Fatal("a checkpoint check changed the project's Git state")
			}
			assertVerifyTempEmpty(t, repo)
		})
	}
}

func TestVerifyCheckpointChecksCanUseARepositoryTheyCreate(t *testing.T) {
	repo, sessionID, turnID := cliRecordedVerifyRepo(t)
	writeGitVerifyConfig(t, repo, []gitVerifyEntry{
		{Name: "init", Args: []string{"init", "-q"}},
		{Name: "toplevel", Args: []string{"rev-parse", "--show-toplevel"}},
	})

	output, err := executeVerifyCommand(t, repo.WorkspaceRoot.String(), "verify", sessionID.String()+":"+turnID.String()+":post", "--json")
	if err != nil {
		t.Fatalf("verify: %v\n%s", err, output)
	}
	report := decodeVerifyReport(t, output)
	toplevel := strings.TrimSpace(report.Checks[1].Stdout)
	if !strings.HasSuffix(toplevel, "/workspace") || !strings.Contains(toplevel, "evaluation-") {
		t.Fatalf("toplevel = %q, want the materialized evaluation root", toplevel)
	}
}

func TestVerifyLiveWorkspaceStillUsesTheProjectRepository(t *testing.T) {
	repo := cliVerifyRepoWithUserGit(t)
	initializeUserGitFixture(t, repo.WorkspaceRoot.String())
	writeGitVerifyConfig(t, repo, []gitVerifyEntry{{Name: "toplevel", Args: []string{"rev-parse", "--show-toplevel"}}})

	output, err := executeVerifyCommand(t, repo.WorkspaceRoot.String(), "verify", "--json")
	if err != nil {
		t.Fatalf("live verify: %v\n%s", err, output)
	}
	report := decodeVerifyReport(t, output)
	got, err := filepath.EvalSymlinks(filepath.FromSlash(strings.TrimSpace(report.Checks[0].Stdout)))
	if err != nil {
		t.Fatalf("resolve reported toplevel %q: %v", report.Checks[0].Stdout, err)
	}
	want, err := filepath.EvalSymlinks(repo.WorkspaceRoot.String())
	if err != nil {
		t.Fatalf("resolve workspace root: %v", err)
	}
	if !strings.EqualFold(got, want) {
		t.Fatalf("live toplevel = %q, want %q", got, want)
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
