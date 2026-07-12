package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	forkengine "github.com/AadiJo/turnal/internal/fork"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/turnevents"
	"github.com/AadiJo/turnal/internal/turns"
)

func TestForkDryRunReportsReadinessWithoutWritingState(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("TURNAL_STATE_DIR", stateDir)
	parent := t.TempDir()
	mainPath := filepath.Join(parent, "main")
	linkedPath := filepath.Join(parent, "linked")
	if err := os.MkdirAll(mainPath, 0o755); err != nil {
		t.Fatalf("mkdir main worktree: %v", err)
	}
	runForkUserGit(t, mainPath, "init")
	runForkUserGit(t, mainPath, "config", "user.email", "turnal@example.test")
	runForkUserGit(t, mainPath, "config", "user.name", "Turnal Test")
	if err := os.WriteFile(filepath.Join(mainPath, "tracked.txt"), []byte("tracked\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	runForkUserGit(t, mainPath, "add", "tracked.txt")
	runForkUserGit(t, mainPath, "commit", "-m", "initial")
	mainRoot, err := primitives.ParseWorkspaceRoot(mainPath)
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot(main): %v", err)
	}
	if _, err := checkpoint.Init(mainRoot); err != nil {
		t.Fatalf("Init(main): %v", err)
	}
	runForkUserGit(t, mainPath, "worktree", "add", "-b", "fork-readonly-test", linkedPath)
	linkedRoot, err := primitives.ParseWorkspaceRoot(linkedPath)
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot(linked): %v", err)
	}
	linkedRepo, err := checkpoint.Open(linkedRoot)
	if err != nil {
		t.Fatalf("Open(linked): %v", err)
	}
	sessionID, _ := recordForkReadyTurn(t, linkedRepo, linkedRoot, "Fix the parser", true)
	t.Chdir(linkedRoot.String())

	snapshotRoots := []string{filepath.Join(mainPath, ".turnal"), stateDir, filepath.Join(mainPath, ".git")}
	makeForkPathsReadOnly(t, snapshotRoots...)
	before := snapshotForkPaths(t, snapshotRoots...)

	output := runRootStdout(t, "fork", sessionID.String()+":1", "--dry-run")
	for _, want := range []string{
		"fork readiness: needs_context",
		"target:         fork-cli:turn:1:pre",
		"fidelity:       L1",
		"source turn:    fork-cli:1 (complete)",
		"adapter:        codex",
		"metadata adapter: codex",
		"model:          cli-test-model",
		"base:           refs/agent-vcs/",
		"captured files: 2",
		"instruction:    available",
		"Fix the parser",
		"workspace files",
		"workspace VCS    metadata_only",
		"conversation     not_recorded",
		"reauthorization_required",
		"Git-ignored and secrets-denied paths",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("fork output missing %q:\n%s", want, output)
		}
	}

	after := snapshotForkPaths(t, snapshotRoots...)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("fork dry-run changed durable metadata\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestForkDryRunJSONIsStructuredAndStable(t *testing.T) {
	root, sessionID, _ := createForkReadyTurn(t, "Fix the parser", true)
	t.Chdir(root.String())

	output := runRootStdout(t, "fork", sessionID.String()+":turn:1", "--dry-run", "--json")
	var report forkengine.Report
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("decode fork JSON: %v\n%s", err, output)
	}
	if report.Version != 1 || report.Readiness != forkengine.ReadinessNeedsContext || report.FidelityLevel != "L1" {
		t.Fatalf("report header = %#v", report)
	}
	if report.Target != "fork-cli:turn:1:pre" || report.Source.SessionID != sessionID || report.Source.TurnID.Uint64() != 1 {
		t.Fatalf("report target/source = %#v / %#v", report.Target, report.Source)
	}
	if report.Instruction.Text != "Fix the parser" || report.Base.CapturedFiles != 1 {
		t.Fatalf("report instruction/base = %#v / %#v", report.Instruction, report.Base)
	}
}

func TestForkRequiresDryRunUntilExecutionExists(t *testing.T) {
	root, sessionID, _ := createForkReadyTurn(t, "Fix the parser", true)
	t.Chdir(root.String())

	cmd := NewRootCmd()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"fork", sessionID.String() + ":1"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("fork without --dry-run succeeded")
	}
	if !strings.Contains(err.Error(), "fork execution is not implemented") {
		t.Fatalf("fork error = %v", err)
	}
}

func TestForkDryRunDoesNotExposeRedactedPrompt(t *testing.T) {
	root, sessionID, _ := createForkReadyTurn(t, primitives.SecretsRedactionText, true)
	t.Chdir(root.String())

	output := runRootStdout(t, "fork", sessionID.String()+":1", "--dry-run")
	if !strings.Contains(output, "fork readiness: needs_instruction") || !strings.Contains(output, "instruction:    redacted") {
		t.Fatalf("fork output = %s", output)
	}
	if strings.Contains(output, primitives.SecretsRedactionText) {
		t.Fatalf("fork output exposes redaction marker: %s", output)
	}
}

func createForkReadyTurn(t *testing.T, prompt string, finish bool) (primitives.WorkspaceRoot, primitives.SessionID, primitives.TurnID) {
	t.Helper()
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sessionID, turnID := recordForkReadyTurn(t, repo, root, prompt, finish)
	return root, sessionID, turnID
}

func recordForkReadyTurn(t *testing.T, repo *checkpoint.Repo, root primitives.WorkspaceRoot, prompt string, finish bool) (primitives.SessionID, primitives.TurnID) {
	t.Helper()
	writeFile(t, root, "app.txt", "before\n")
	sessionID := sessionID(t, "fork-cli")
	turnID, err := primitives.NewTurnID(1)
	if err != nil {
		t.Fatalf("NewTurnID: %v", err)
	}
	adapter := primitives.AdapterCodex
	log := repo.EventLog()
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypeSessionStart,
		Adapter:   adapter,
		Payload:   json.RawMessage(`{"provider_session_id":"fork-cli","model":"cli-test-model","permission_mode":"workspace"}`),
	}); err != nil {
		t.Fatalf("append session start: %v", err)
	}
	recorder := turnevents.Recorder{Log: log, Manager: turns.NewManager(repo), Adapter: adapter}
	if _, err := recorder.Start(sessionID, turnID); err != nil {
		t.Fatalf("start turn: %v", err)
	}
	promptPayload, _ := json.Marshal(map[string]string{"text": prompt})
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypePromptUser,
		Adapter:   adapter,
		Payload:   promptPayload,
	}); err != nil {
		t.Fatalf("append prompt: %v", err)
	}
	if finish {
		writeFile(t, root, "app.txt", "after\n")
		if _, err := recorder.Finish(sessionID, turnID); err != nil {
			t.Fatalf("finish turn: %v", err)
		}
	}
	return sessionID, turnID
}

func snapshotForkPaths(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	for _, root := range roots {
		if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			value := fmt.Sprintf("mode=%s mtime=%d", info.Mode(), info.ModTime().UnixNano())
			switch {
			case info.Mode().IsRegular():
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				digest := sha256.Sum256(data)
				value += " sha256=" + hex.EncodeToString(digest[:])
			case info.Mode()&os.ModeSymlink != 0:
				target, err := os.Readlink(path)
				if err != nil {
					return err
				}
				value += " target=" + target
			}
			snapshot[filepath.ToSlash(root)+":"+relative] = value
			return nil
		}); err != nil {
			t.Fatalf("snapshot metadata: %v", err)
		}
	}
	return snapshot
}

func makeForkPathsReadOnly(t *testing.T, roots ...string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	originalModes := map[string]fs.FileMode{}
	for _, root := range roots {
		if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil
			}
			originalModes[path] = info.Mode().Perm()
			mode := fs.FileMode(0o400)
			if info.IsDir() {
				mode = 0o500
			}
			return os.Chmod(path, mode)
		}); err != nil {
			t.Fatalf("make fork metadata read-only: %v", err)
		}
	}
	t.Cleanup(func() {
		for path, mode := range originalModes {
			_ = os.Chmod(path, mode)
		}
	})
}

func runForkUserGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = cleanForkGitEnv(os.Environ())
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func cleanForkGitEnv(env []string) []string {
	clean := make([]string, 0, len(env))
	for _, item := range env {
		name, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(name, "GIT_") {
			continue
		}
		clean = append(clean, item)
	}
	return clean
}
