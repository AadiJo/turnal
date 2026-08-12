package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/sharedhistory"
	"github.com/AadiJo/turnal/internal/turnevents"
	"github.com/AadiJo/turnal/internal/turns"
)

func TestSharedHistoryCLIEndToEnd(t *testing.T) {
	requireGit(t)
	temp := t.TempDir()
	remote := filepath.Join(temp, "history.git")
	if output, err := exec.Command("git", "init", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, output)
	}
	root, err := primitives.ParseWorkspaceRoot(filepath.Join(temp, "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("checkpoint.Init: %v", err)
	}
	sessionID, err := primitives.ParseSessionID("cli-share")
	if err != nil {
		t.Fatal(err)
	}
	recorder := turnevents.Recorder{Log: repo.EventLog(), Manager: turns.NewManager(repo), Adapter: primitives.AdapterManual}
	started, err := recorder.Start(sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EventLog().Append(eventlog.AppendInput{SessionID: sessionID, TurnID: &started.TurnID, Type: primitives.EventTypePromptUser, Adapter: primitives.AdapterManual, Payload: json.RawMessage(`{"text":"private prompt"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.Finish(sessionID, started.TurnID); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root.String())

	var status sharedhistory.Status
	decodeCLIJSON(t, runRootStdout(t, "share", "enable", "--remote", remote, "--prompt-mode", "omit", "--json"), &status)
	if !status.Configured || status.Approved {
		t.Fatalf("enabled status = %#v", status)
	}

	var plan sharedhistory.Plan
	decodeCLIJSON(t, runRootStdout(t, "share", "preview", sessionID.String()+":"+started.TurnID.String(), "--approve", "--json"), &plan)
	if plan.Locator == "" || plan.ApprovalRequired {
		t.Fatalf("approved plan = %#v", plan)
	}
	var dryRun sharedhistory.PushPlan
	decodeCLIJSON(t, runRootStdout(t, "sync", "push", "--dry-run", "--json"), &dryRun)
	if dryRun.Publishable != 1 || dryRun.BatchSize != 1 || len(dryRun.Pending) != 1 {
		t.Fatalf("push dry run = %#v", dryRun)
	}

	var pushed sharedhistory.Result
	decodeCLIJSON(t, runRootStdout(t, "sync", "push", "--json"), &pushed)
	if pushed.Published != 1 || pushed.Head == "" {
		t.Fatalf("push result = %#v", pushed)
	}

	var bundle sharedhistory.StoredBundle
	decodeCLIJSON(t, runRootStdout(t, "share", "show", plan.Locator, "--json"), &bundle)
	if bundle.Manifest.BundleID != plan.Manifest.BundleID {
		t.Fatalf("shown bundle = %#v", bundle.Manifest)
	}
	if output := runRootStdout(t, "share", "show", plan.Locator); !strings.Contains(output, "prompt: [omitted by policy]") {
		t.Fatalf("human-readable bundle = %q", output)
	}
	var listed []sharedhistory.BundleSummary
	decodeCLIJSON(t, runRootStdout(t, "share", "list", "--json"), &listed)
	if len(listed) != 1 || listed[0].Locator != plan.Locator || !listed[0].Local {
		t.Fatalf("listed bundles = %#v", listed)
	}
	if output := runRootStdout(t, "share", "enable", "--remote", remote, "--prompt-mode", "omit"); !strings.Contains(output, "approval:    current") {
		t.Fatalf("unchanged configuration output = %q", output)
	}

	receiverRoot, err := primitives.ParseWorkspaceRoot(filepath.Join(temp, "receiver"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := checkpoint.Init(receiverRoot); err != nil {
		t.Fatalf("initialize receiver: %v", err)
	}
	t.Chdir(receiverRoot.String())
	var joined sharedhistory.Status
	decodeCLIJSON(t, runRootStdout(t, "share", "enable", "--remote", remote, "--repo-id", status.RepoID.String(), "--prompt-mode", "omit", "--json"), &joined)
	if joined.RepoID != status.RepoID {
		t.Fatalf("joined repository id = %s, want %s", joined.RepoID, status.RepoID)
	}
	var pulled sharedhistory.Result
	decodeCLIJSON(t, runRootStdout(t, "sync", "pull", "--json"), &pulled)
	if pulled.Pulled != 1 {
		t.Fatalf("pull result = %#v", pulled)
	}
	var pulledBundle sharedhistory.StoredBundle
	decodeCLIJSON(t, runRootStdout(t, "share", "show", plan.Locator, "--json"), &pulledBundle)
	if pulledBundle.Manifest.BundleID != plan.Manifest.BundleID {
		t.Fatalf("pulled bundle = %#v", pulledBundle.Manifest)
	}
	decodeCLIJSON(t, runRootStdout(t, "share", "list", "--json"), &listed)
	if len(listed) != 1 || listed[0].Locator != plan.Locator || listed[0].Local {
		t.Fatalf("receiver bundles = %#v", listed)
	}
}

func decodeCLIJSON(t *testing.T, output string, target any) {
	t.Helper()
	if err := json.Unmarshal([]byte(output), target); err != nil {
		t.Fatalf("decode CLI JSON: %v\n%s", err, output)
	}
}

func TestSharedHistoryHumanTextEscapesTerminalControls(t *testing.T) {
	got := indentSharedText("before\x1b[31mred\r \u202eevil\nafter")
	want := "before\\u001b[31mred\\u000d \\u202eevil\n    after"
	if got != want {
		t.Fatalf("indented shared text = %q, want %q", got, want)
	}
}

func TestSharedHistoryRedactionDiagnosticsAndReviewCLI(t *testing.T) {
	root, err := primitives.ParseWorkspaceRoot(filepath.Join(t.TempDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := checkpoint.Init(root); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root.String())

	diagnostics := runRootStdout(t, "share", "redaction", "diagnose")
	for _, want := range []string{"scanner: " + sharedhistory.ScannerVersion, "high_entropy", "known_secret", "golden corpus:", "false positives: 0", "false negatives: 0"} {
		if !strings.Contains(diagnostics, want) {
			t.Fatalf("redaction diagnostics missing %q:\n%s", want, diagnostics)
		}
	}

	passingPath := filepath.Join(t.TempDir(), "passing.jsonl")
	passing := strings.Join([]string{
		`{"id":"known-leak","text":"password=hunter2","expect":"redact"}`,
		`{"id":"known-safe","text":"ordinary sentence","expect":"allow"}`,
	}, "\n")
	if err := os.WriteFile(passingPath, []byte(passing), 0o600); err != nil {
		t.Fatal(err)
	}
	output := runRootStdout(t, "share", "redaction", "review", passingPath)
	if !strings.Contains(output, "review: 2 cases") || !strings.Contains(output, "false positives: 0") || !strings.Contains(output, "false negatives: 0") {
		t.Fatalf("passing redaction review = %q", output)
	}

	failingPath := filepath.Join(t.TempDir(), "failing.jsonl")
	failing := strings.Join([]string{
		`{"id":"expected-fp","text":"password=hunter2","expect":"allow"}`,
		`{"id":"expected-fn","text":"ordinary sentence","expect":"redact"}`,
	}, "\n")
	if err := os.WriteFile(failingPath, []byte(failing), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := NewRootCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"share", "redaction", "review", failingPath, "--json"})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "1 false positive(s) and 1 false negative(s)") {
		t.Fatalf("failing review error = %v, output = %s", err, stdout.String())
	}
	var report sharedhistory.RedactionReviewReport
	decodeCLIJSON(t, stdout.String(), &report)
	if report.FalsePositives != 1 || report.FalseNegatives != 1 {
		t.Fatalf("failing review report = %#v", report)
	}
	for _, sourceText := range []string{"password=hunter2", "ordinary sentence"} {
		if strings.Contains(stdout.String(), sourceText) {
			t.Fatalf("review output echoed source text %q: %s", sourceText, stdout.String())
		}
	}
}
