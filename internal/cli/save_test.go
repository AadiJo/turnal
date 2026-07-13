package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/manualcheckpoints"
	rollbackengine "github.com/AadiJo/turnal/internal/rollback"
)

func TestSaveCommandCapturesLogsAndRollsBackByHash(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	gitInit := exec.Command("git", "init")
	gitInit.Dir = root.String()
	if output, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	gitSentinel := filepath.Join(root.String(), ".git", "turnal-sentinel")
	if err := os.WriteFile(gitSentinel, []byte("user git metadata\n"), 0o600); err != nil {
		t.Fatalf("write user Git sentinel: %v", err)
	}

	original := []byte{'b', 'e', 'f', 'o', 'r', 'e', 0, '\n'}
	path := filepath.Join(root.String(), "app.bin")
	if err := os.WriteFile(path, original, 0o755); err != nil {
		t.Fatalf("write original: %v", err)
	}
	t.Setenv("GIT_DIR", filepath.Join(root.String(), "redirected-git-dir"))
	t.Setenv("GIT_WORK_TREE", filepath.Join(root.String(), "redirected-worktree"))

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"save", "tests", "passing", "before", "refactor", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("save: %v\n%s", err, out.String())
	}
	var saved struct {
		CheckpointID string `json:"checkpoint_id"`
		CommitSHA    string `json:"commit_sha"`
		Ref          string `json:"ref"`
		Message      string `json:"message"`
	}
	if err := json.Unmarshal(out.Bytes(), &saved); err != nil {
		t.Fatalf("decode save output: %v\n%s", err, out.String())
	}
	if saved.CommitSHA == "" || saved.CheckpointID == "" || !strings.Contains(saved.Ref, "/manual/") {
		t.Fatalf("save output missing identity: %#v", saved)
	}
	if saved.Message != "tests passing before refactor" {
		t.Fatalf("message = %q", saved.Message)
	}
	if _, err := os.Stat(filepath.Join(root.String(), "redirected-git-dir")); !os.IsNotExist(err) {
		t.Fatalf("inherited GIT_DIR was used: %v", err)
	}

	sessions, err := eventlog.Open(repo.MetadataDir).ListSessions()
	if err != nil {
		t.Fatalf("list agent sessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("save fabricated agent sessions: %v", sessions)
	}
	saves, err := manualcheckpoints.Read(repo, false)
	if err != nil {
		t.Fatalf("read manual checkpoints: %v", err)
	}
	if len(saves) != 1 || saves[0].Event.TurnID != nil || saves[0].Message != saved.Message {
		t.Fatalf("manual checkpoint events = %#v", saves)
	}

	if err := os.WriteFile(path, []byte("after\n"), 0o644); err != nil {
		t.Fatalf("mutate file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root.String(), "extra.txt"), []byte("remove\n"), 0o644); err != nil {
		t.Fatalf("write extra: %v", err)
	}
	rollback := NewRootCmd()
	out.Reset()
	rollback.SetOut(&out)
	rollback.SetErr(&out)
	rollback.SetArgs([]string{"rollback", "--to", saved.CommitSHA[:12]})
	if err := rollback.Execute(); err != nil {
		t.Fatalf("rollback saved checkpoint: %v\n%s", err, out.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("restored bytes = %q, want %q", got, original)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("executable bit was not restored: info=%v err=%v", info, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root.String(), "extra.txt")); !os.IsNotExist(err) {
		t.Fatalf("extra file survived rollback: %v", err)
	}
	if _, err := os.Stat(repo.GitDir); err != nil {
		t.Fatalf("Turnal metadata was restored or removed: %v", err)
	}
	if got, err := os.ReadFile(gitSentinel); err != nil || string(got) != "user git metadata\n" {
		t.Fatalf("user .git was mutated: content=%q err=%v", got, err)
	}

	reindex := NewRootCmd()
	out.Reset()
	reindex.SetOut(&out)
	reindex.SetErr(&out)
	reindex.SetArgs([]string{"reindex"})
	if err := reindex.Execute(); err != nil {
		t.Fatalf("reindex with manual checkpoint: %v\n%s", err, out.String())
	}

	logCmd := NewRootCmd()
	out.Reset()
	logCmd.SetOut(&out)
	logCmd.SetErr(&out)
	logCmd.SetArgs([]string{"log", "--index", "--no-pager"})
	if err := logCmd.Execute(); err != nil {
		t.Fatalf("log: %v\n%s", err, out.String())
	}
	for _, want := range []string{"1 save", "saved " + saved.CommitSHA[:12], saved.Message, "reverted to saved"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("log output missing %q:\n%s", want, out.String())
		}
	}
}

func TestSaveRejectsOversizedMessageBeforeCheckpointing(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"save", strings.Repeat("x", maxSaveMessageBytes+1)})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("save error = %v", err)
	}
	infos, listErr := repo.ListAllCheckpointRefInfos()
	if listErr != nil {
		t.Fatalf("list checkpoints: %v", listErr)
	}
	if len(infos) != 0 {
		t.Fatalf("invalid save created checkpoints: %#v", infos)
	}
}

func TestManualSaveRejectsWorkspaceGitRollback(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	created, err := repo.CreateManualCheckpoint()
	if err != nil {
		t.Fatalf("CreateManualCheckpoint: %v", err)
	}
	if _, err := manualcheckpoints.Append(repo, created, ""); err != nil {
		t.Fatalf("append event: %v", err)
	}

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"rollback", "--workspace-git", "--to", created.Commit.String()})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "unavailable for manual checkpoints") {
		t.Fatalf("workspace-git rollback error = %v", err)
	}
}

func TestSaveRefusesPendingRollbackRecovery(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	journalPath := rollbackengine.JournalPath(repo)
	if err := os.MkdirAll(filepath.Dir(journalPath), 0o700); err != nil {
		t.Fatalf("mkdir journal dir: %v", err)
	}
	if err := os.WriteFile(journalPath, []byte("{\"version\":1,\"state\":\"intent\"}\n"), 0o600); err != nil {
		t.Fatalf("write rollback journal: %v", err)
	}

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"save", "unsafe"})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "rollback recovery is pending") {
		t.Fatalf("save error = %v", err)
	}
	infos, err := repo.ListAllCheckpointRefInfos()
	if err != nil {
		t.Fatalf("list checkpoints: %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("save created checkpoint during rollback recovery: %#v", infos)
	}
}
