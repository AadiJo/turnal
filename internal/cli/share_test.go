package cli

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
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
}

func decodeCLIJSON(t *testing.T, output string, target any) {
	t.Helper()
	if err := json.Unmarshal([]byte(output), target); err != nil {
		t.Fatalf("decode CLI JSON: %v\n%s", err, output)
	}
}
