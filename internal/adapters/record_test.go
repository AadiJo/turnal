package adapters

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agent-vcs-again/internal/checkpoint"
	"agent-vcs-again/internal/primitives"
)

func TestRecordHookPayloadAppendsRawAdapterLog(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	if _, err := checkpoint.Init(root); err != nil {
		t.Fatalf("Init: %v", err)
	}

	payload := []byte(`{"cwd":` + quote(root.String()) + `,"session_id":"claude-session","prompt":"hello"}`)
	rawRef, err := RecordHookPayload(primitives.AdapterClaudeCode, "UserPromptSubmit", payload)
	if err != nil {
		t.Fatalf("RecordHookPayload: %v", err)
	}
	if rawRef != "claude-code:1" {
		t.Fatalf("raw ref = %q, want claude-code:1", rawRef)
	}

	records := readRecords(t, filepath.Join(root.String(), ".agent-vcs", "log", "adapter", "claude-code.jsonl"))
	if len(records) != 1 {
		t.Fatalf("records len = %d, want 1", len(records))
	}
	record := records[0]
	if record.Version != 1 || record.Adapter != primitives.AdapterClaudeCode || record.Hook != "UserPromptSubmit" {
		t.Fatalf("unexpected record: %#v", record)
	}
	if !json.Valid(record.Payload) {
		t.Fatalf("payload is not valid JSON: %s", record.Payload)
	}
	if record.Raw != "" || record.Error != "" {
		t.Fatalf("valid payload recorded raw/error: %#v", record)
	}
}

func TestRecordHookPayloadPreservesMalformedPayload(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	if _, err := checkpoint.Init(root); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	rawRef, err := RecordHookPayload(primitives.AdapterCodex, "CodexHook", []byte("{broken\n"))
	if err != nil {
		t.Fatalf("RecordHookPayload malformed: %v", err)
	}
	if rawRef != "codex:1" {
		t.Fatalf("raw ref = %q, want codex:1", rawRef)
	}

	records := readRecords(t, filepath.Join(root.String(), ".agent-vcs", "log", "adapter", "codex.jsonl"))
	if len(records) != 1 {
		t.Fatalf("records len = %d, want 1", len(records))
	}
	record := records[0]
	if record.Raw != "{broken\n" {
		t.Fatalf("raw = %q", record.Raw)
	}
	if !strings.Contains(record.Error, "malformed JSON") {
		t.Fatalf("error = %q, want malformed JSON", record.Error)
	}
}

func TestRecordHookPayloadNoopsOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	payload := []byte(`{"cwd":` + quote(root) + `,"session_id":"missing"}`)
	rawRef, err := RecordHookPayload(primitives.AdapterCodex, "CodexHook", payload)
	if err != nil {
		t.Fatalf("RecordHookPayload: %v", err)
	}
	if rawRef != "" {
		t.Fatalf("raw ref outside workspace = %q, want empty", rawRef)
	}
	if _, err := os.Stat(filepath.Join(root, ".agent-vcs")); !os.IsNotExist(err) {
		t.Fatalf("RecordHookPayload created workspace metadata, stat err=%v", err)
	}
}

func TestRecordHookPayloadUsesProcessWorkspaceBeforePayloadCWD(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	if _, err := checkpoint.Init(root); err != nil {
		t.Fatalf("Init root: %v", err)
	}
	other := workspaceRoot(t)
	if _, err := checkpoint.Init(other); err != nil {
		t.Fatalf("Init other: %v", err)
	}
	t.Chdir(root.String())

	payload := []byte(`{"cwd":` + quote(other.String()) + `,"session_id":"other"}`)
	rawRef, err := RecordHookPayload(primitives.AdapterCodex, "PostToolUse", payload)
	if err != nil {
		t.Fatalf("RecordHookPayload: %v", err)
	}
	if rawRef != "codex:1" {
		t.Fatalf("raw ref = %q, want codex:1", rawRef)
	}

	rootRecords := filepath.Join(root.String(), ".agent-vcs", "log", "adapter", "codex.jsonl")
	if _, err := os.Stat(rootRecords); err != nil {
		t.Fatalf("expected process workspace record: %v", err)
	}
	otherRecords := filepath.Join(other.String(), ".agent-vcs", "log", "adapter", "codex.jsonl")
	if _, err := os.Stat(otherRecords); !os.IsNotExist(err) {
		t.Fatalf("payload cwd workspace should not receive record, stat err=%v", err)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable not found")
	}
}

func workspaceRoot(t *testing.T) primitives.WorkspaceRoot {
	t.Helper()
	root, err := primitives.ParseWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot: %v", err)
	}
	return root
}

func sessionID(t *testing.T, value string) primitives.SessionID {
	t.Helper()
	sessionID, err := primitives.ParseSessionID(value)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	return sessionID
}

func readRecords(t *testing.T, path string) []RawHookRecord {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = file.Close() }()

	var records []RawHookRecord
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record RawHookRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("parse record: %v\n%s", err, scanner.Text())
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan records: %v", err)
	}
	return records
}

func quote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}
