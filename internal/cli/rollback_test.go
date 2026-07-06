package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agent-vcs-again/internal/checkpoint"
	eventlog "agent-vcs-again/internal/events"
	"agent-vcs-again/internal/primitives"
	rollbackengine "agent-vcs-again/internal/rollback"
)

func TestRollbackCommandRestoresCheckpoint(t *testing.T) {
	root, repo, sessionID, turnID := createTurnWithDiff(t)
	t.Chdir(root.String())

	if err := os.WriteFile(filepath.Join(root.String(), "app.txt"), []byte("working copy\n"), 0o644); err != nil {
		t.Fatalf("write app.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root.String(), "extra.txt"), []byte("remove me\n"), 0o644); err != nil {
		t.Fatalf("write extra.txt: %v", err)
	}

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"rollback", "--to", sessionID.String() + ":turn:" + turnID.String() + ":pre"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("rollback command: %v\n%s", err, out.String())
	}

	content, err := os.ReadFile(filepath.Join(root.String(), "app.txt"))
	if err != nil {
		t.Fatalf("read app.txt: %v", err)
	}
	if string(content) != "before\n" {
		t.Fatalf("app.txt = %q, want pre-checkpoint content", content)
	}
	if _, err := os.Stat(filepath.Join(root.String(), "extra.txt")); !os.IsNotExist(err) {
		t.Fatalf("extra.txt still exists or stat failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo.MetadataDir, "git")); err != nil {
		t.Fatalf("agent-vcs metadata missing after rollback: %v", err)
	}
	if !strings.Contains(out.String(), "rolled back to") {
		t.Fatalf("rollback output = %q", out.String())
	}
	if !strings.Contains(out.String(), "safety checkpoint") {
		t.Fatalf("rollback output missing safety checkpoint: %q", out.String())
	}
	if _, err := os.Stat(rollbackengine.JournalPath(repo)); !os.IsNotExist(err) {
		t.Fatalf("rollback journal still exists or stat failed: %v", err)
	}

	events, err := eventlog.Open(repo.MetadataDir).Read(sessionID)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if countCLIEvents(events, primitives.EventTypeRollback) != 1 {
		t.Fatalf("rollback events = %d, want 1; events=%#v", countCLIEvents(events, primitives.EventTypeRollback), events)
	}
	rollbackEvent := lastCLIEvent(events, primitives.EventTypeRollback)
	if rollbackEvent.RawRef != "demo:turn:1:pre" {
		t.Fatalf("rollback raw ref = %q, want demo:turn:1:pre", rollbackEvent.RawRef)
	}
	var payload rollbackengine.EventPayload
	if err := json.Unmarshal(rollbackEvent.Payload, &payload); err != nil {
		t.Fatalf("unmarshal rollback payload: %v\n%s", err, rollbackEvent.Payload)
	}
	if payload.SafetyRef == "" || payload.SafetyCommitSHA == "" {
		t.Fatalf("rollback payload missing safety metadata: %#v", payload)
	}
	if !strings.HasPrefix(payload.SafetyRef, "refs/agent-vcs/rollback-safety/demo/turn/000001/pre/") {
		t.Fatalf("safety ref = %q", payload.SafetyRef)
	}
	safetyCommit, err := repo.RefCommit(payload.SafetyRef)
	if err != nil {
		t.Fatalf("resolve safety ref: %v", err)
	}
	if safetyCommit.String() != payload.SafetyCommitSHA {
		t.Fatalf("safety commit = %s, want payload %s", safetyCommit, payload.SafetyCommitSHA)
	}
	safetyApp := gitShow(t, repo.GitDir, payload.SafetyCommitSHA+":app.txt")
	if safetyApp != "working copy\n" {
		t.Fatalf("safety app.txt = %q, want working copy", safetyApp)
	}
}

func TestRollbackCommandWorkspaceGitFlagCanDisableConfigDefault(t *testing.T) {
	writeGlobalAgentConfig(t, `
version = 1

[rollback]
mode = "workspace-git"
`)
	root, _, sessionID, turnID := createTurnWithDiff(t)
	t.Chdir(root.String())

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"rollback", "--workspace-git=false", "--to", sessionID.String() + ":turn:" + turnID.String() + ":pre"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("rollback command: %v\n%s", err, out.String())
	}

	output := out.String()
	if !strings.Contains(output, "rolled back to") {
		t.Fatalf("rollback output missing checkpoint mode:\n%s", output)
	}
	if strings.Contains(output, "workspace git") {
		t.Fatalf("rollback used workspace-git despite explicit false flag:\n%s", output)
	}
}

func TestRollbackCommandDryRunShowsPlannedChangesWithoutMutating(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	writeFile(t, root, "added.txt", "target only\n")
	writeFile(t, root, "modified.txt", "target\n")
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("pre checkpoint: %v", err)
	}
	if err := os.Remove(filepath.Join(root.String(), "added.txt")); err != nil {
		t.Fatalf("remove added.txt: %v", err)
	}
	writeFile(t, root, "modified.txt", "current\n")
	writeFile(t, root, "deleted.txt", "current only\n")

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"rollback", "--to", "demo:turn:1:pre", "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("rollback dry-run: %v\n%s", err, out.String())
	}

	output := out.String()
	for _, want := range []string{
		"dry-run rollback to",
		"added:",
		"  added.txt",
		"modified:",
		"  modified.txt",
		"deleted:",
		"  deleted.txt",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, output)
		}
	}
	if _, err := os.Stat(filepath.Join(root.String(), "added.txt")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created added.txt or stat failed: %v", err)
	}
	modified, err := os.ReadFile(filepath.Join(root.String(), "modified.txt"))
	if err != nil {
		t.Fatalf("read modified.txt: %v", err)
	}
	if string(modified) != "current\n" {
		t.Fatalf("dry-run modified workspace: %q", modified)
	}
	if _, err := os.Stat(filepath.Join(root.String(), "deleted.txt")); err != nil {
		t.Fatalf("dry-run deleted deleted.txt: %v", err)
	}
	if _, err := os.Stat(rollbackengine.JournalPath(repo)); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote rollback journal or stat failed: %v", err)
	}
	events, err := eventlog.Open(repo.MetadataDir).Read(sessionID)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if countCLIEvents(events, primitives.EventTypeRollback) != 0 {
		t.Fatalf("dry-run rollback events = %d, want 0", countCLIEvents(events, primitives.EventTypeRollback))
	}
}

func TestRollbackCommandReportsActiveJournal(t *testing.T) {
	root, repo, sessionID, turnID := createTurnWithDiff(t)
	t.Chdir(root.String())

	if err := os.WriteFile(rollbackengine.JournalPath(repo), []byte(`{
  "version": 1,
  "state": "restoring",
  "target": "demo:turn:1:pre",
  "safety_ref": "refs/agent-vcs/rollback-safety/demo/turn/000001/pre/example",
  "safety_commit_sha": "abc123"
}
`), 0o644); err != nil {
		t.Fatalf("write rollback journal: %v", err)
	}

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"rollback", "--to", sessionID.String() + ":turn:" + turnID.String() + ":pre"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("rollback succeeded with active journal")
	}
	for _, want := range []string{"rollback invariant failed", "active rollback journal", "state=restoring", "safety_ref="} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q:\n%v", want, err)
		}
	}
}

func countCLIEvents(events []eventlog.Event, eventType primitives.EventType) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func lastCLIEvent(events []eventlog.Event, eventType primitives.EventType) eventlog.Event {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == eventType {
			return events[i]
		}
	}
	return eventlog.Event{}
}

func gitShow(t *testing.T, gitDir string, object string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", gitDir, "show", object)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git show %s: %v\n%s", object, err, output)
	}
	return string(output)
}
