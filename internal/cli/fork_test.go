package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
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
	root, sessionID, _ := createForkReadyTurn(t, "Fix the parser", true)
	t.Chdir(root.String())
	before := snapshotForkMetadata(t, filepath.Join(root.String(), ".turnal"))

	output := runRootStdout(t, "fork", sessionID.String()+":1", "--dry-run")
	for _, want := range []string{
		"fork readiness: ready",
		"target:         fork-cli:turn:1:pre",
		"fidelity:       L1",
		"source turn:    fork-cli:1 (complete)",
		"adapter:        codex",
		"model:          cli-test-model",
		"base:           refs/agent-vcs/",
		"captured files: 1",
		"instruction:    available",
		"Fix the parser",
		"workspace files",
		"reauthorization_required",
		"Git-ignored and secrets-denied paths",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("fork output missing %q:\n%s", want, output)
		}
	}

	after := snapshotForkMetadata(t, filepath.Join(root.String(), ".turnal"))
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
	if report.Version != 1 || report.Readiness != forkengine.ReadinessReady || report.FidelityLevel != "L1" {
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
	root, sessionID, _ := createForkReadyTurn(t, "[redacted by turnal secrets policy]", true)
	t.Chdir(root.String())

	output := runRootStdout(t, "fork", sessionID.String()+":1", "--dry-run")
	if !strings.Contains(output, "fork readiness: needs_instruction") || !strings.Contains(output, "instruction:    redacted") {
		t.Fatalf("fork output = %s", output)
	}
	if strings.Contains(output, "[redacted by turnal secrets policy]") {
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
	return root, sessionID, turnID
}

func snapshotForkMetadata(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if strings.HasPrefix(relative, "tmp/") || strings.HasPrefix(relative, "worktrees/") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		snapshot[relative] = hex.EncodeToString(digest[:])
		return nil
	}); err != nil {
		t.Fatalf("snapshot metadata: %v", err)
	}
	return snapshot
}
