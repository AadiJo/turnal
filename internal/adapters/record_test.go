package adapters

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
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
	if rawRef != "v2:claude-session:claude-code:1" {
		t.Fatalf("raw ref = %q, want v2 session ref", rawRef)
	}

	records := readRecords(t, filepath.Join(root.String(), ".turnal", "log", "raw", "claude-session", "claude-code.jsonl"))
	if len(records) != 1 {
		t.Fatalf("records len = %d, want 1", len(records))
	}
	record := records[0]
	if record.Version != 2 || record.Sequence != 1 || record.SessionID != "claude-session" || record.Adapter != primitives.AdapterClaudeCode || record.Hook != "UserPromptSubmit" {
		t.Fatalf("unexpected record: %#v", record)
	}
	if !json.Valid(record.Payload) {
		t.Fatalf("payload is not valid JSON: %s", record.Payload)
	}
	if record.Raw != "" || record.Error != "" {
		t.Fatalf("valid payload recorded raw/error: %#v", record)
	}
}

func TestRecordExternalHookPayloadRedactsProviderAliases(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	config := []byte("version = 1\n\n[secrets]\nstore_prompts = false\nstore_tool_io = false\n")
	if err := os.WriteFile(filepath.Join(repo.MetadataDir, "config.toml"), config, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	sessionID, _ := primitives.ParseSessionID("external-session")
	raw := []byte(`{"sessionId":"external-session","prompt":"prompt-secret","toolArgs":{"token":"tool-secret"},"toolResult":{"textResultForLlm":"result-secret"}}`)
	ref, err := RecordExternalHookPayload("copilot-cli", "postToolUse", raw, root.String(), sessionID)
	if err != nil {
		t.Fatalf("RecordExternalHookPayload: %v", err)
	}
	record, err := ReadRawHookRecord(repo.MetadataDir, ref)
	if err != nil {
		t.Fatalf("ReadRawHookRecord: %v", err)
	}
	stored := string(record.Payload)
	for _, secret := range []string{"prompt-secret", "tool-secret", "result-secret"} {
		if strings.Contains(stored, secret) {
			t.Fatalf("external raw payload retained %q: %s", secret, stored)
		}
	}
	if !strings.Contains(stored, "redacted") {
		t.Fatalf("external raw payload has no redaction markers: %s", stored)
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
	if rawRef != "v2:unassigned:codex:1" {
		t.Fatalf("raw ref = %q, want unassigned v2 ref", rawRef)
	}

	records := readRecords(t, filepath.Join(root.String(), ".turnal", "log", "raw", "unassigned", "codex.jsonl"))
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

func TestRecordHookPayloadRecoversTrailingPartialRecord(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	path := filepath.Join(repo.MetadataDir, "log", "raw", "recovered", "codex.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir adapter log: %v", err)
	}
	if err := os.WriteFile(path, []byte("{\"partial\":"), 0o600); err != nil {
		t.Fatalf("write partial adapter log: %v", err)
	}
	payload := []byte("{\"cwd\":" + quote(root.String()) + ",\"session_id\":\"recovered\"}")
	ref, err := RecordHookPayload(primitives.AdapterCodex, "CodexHook", payload)
	if err != nil {
		t.Fatalf("RecordHookPayload: %v", err)
	}
	if ref != "v2:recovered:codex:1" {
		t.Fatalf("ref = %q, want v2 recovered ref", ref)
	}
	record, err := ReadRawHookRecord(repo.MetadataDir, ref)
	if err != nil {
		t.Fatalf("ReadRawHookRecord: %v", err)
	}
	if record.Hook != "CodexHook" {
		t.Fatalf("record = %#v", record)
	}
}

func TestRedactRawHookSessionPreservesReferencesAndRemovesPayload(t *testing.T) {
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	demo, _ := primitives.ParseSessionID("demo")
	records := []RawHookRecord{
		{Version: 1, Adapter: primitives.AdapterCodex, Hook: "turn", ReceivedAt: "2026-01-01T00:00:00Z", Payload: json.RawMessage(`{"session_id":"demo","secret":"remove-me"}`)},
		{Version: 1, Adapter: primitives.AdapterCodex, Hook: "turn", ReceivedAt: "2026-01-01T00:00:01Z", Payload: json.RawMessage(`{"session_id":"keep","value":"retain-me"}`)},
	}
	for _, record := range records {
		if _, err := appendRawHookRecord(repo.MetadataDir, record); err != nil {
			t.Fatalf("appendRawHookRecord: %v", err)
		}
	}

	changed, err := RedactRawHookSession(repo.MetadataDir, demo, false)
	if err != nil {
		t.Fatalf("RedactRawHookSession: %v", err)
	}
	if len(changed) != 1 {
		t.Fatalf("changed files = %#v, want one", changed)
	}
	deleted, err := ReadRawHookRecord(repo.MetadataDir, "codex:1")
	if err != nil {
		t.Fatalf("read tombstone: %v", err)
	}
	if deleted.Hook != "deleted-session-record" || len(deleted.Payload) != 0 {
		t.Fatalf("deleted record = %#v, want payload-free tombstone", deleted)
	}
	kept, err := ReadRawHookRecord(repo.MetadataDir, "codex:2")
	if err != nil {
		t.Fatalf("read retained record: %v", err)
	}
	if !bytes.Contains(kept.Payload, []byte("retain-me")) {
		t.Fatalf("retained payload = %s", kept.Payload)
	}
	data, err := os.ReadFile(changed[0])
	if err != nil {
		t.Fatalf("read rewritten log: %v", err)
	}
	if bytes.Contains(data, []byte("remove-me")) {
		t.Fatal("deleted session payload remains in raw adapter log")
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
	if _, err := os.Stat(filepath.Join(root, ".turnal")); !os.IsNotExist(err) {
		t.Fatalf("RecordHookPayload created workspace metadata, stat err=%v", err)
	}
}

func TestRecordHookPayloadUsesAbsolutePayloadCWDBeforeProcessWorkspace(t *testing.T) {
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
	if rawRef != "v2:other:codex:1" {
		t.Fatalf("raw ref = %q, want v2 other ref", rawRef)
	}

	rootRecords := filepath.Join(root.String(), ".turnal", "log", "raw", "other", "codex.jsonl")
	if _, err := os.Stat(rootRecords); !os.IsNotExist(err) {
		t.Fatalf("process workspace should not receive payload for another workspace, stat err=%v", err)
	}
	otherRecords := filepath.Join(other.String(), ".turnal", "log", "raw", "other", "codex.jsonl")
	if _, err := os.Stat(otherRecords); err != nil {
		t.Fatalf("expected payload cwd workspace record: %v", err)
	}
}

func TestReadRawHookRecord(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	payload := []byte(`{"cwd":` + quote(root.String()) + `,"session_id":"claude-session","prompt":"hello"}`)
	rawRef, err := RecordHookPayload(primitives.AdapterClaudeCode, "UserPromptSubmit", payload)
	if err != nil {
		t.Fatalf("RecordHookPayload: %v", err)
	}

	record, err := ReadRawHookRecord(repo.MetadataDir, rawRef)
	if err != nil {
		t.Fatalf("ReadRawHookRecord: %v", err)
	}
	if record.Adapter != primitives.AdapterClaudeCode || record.Hook != "UserPromptSubmit" {
		t.Fatalf("record = %#v", record)
	}
	if string(record.Payload) != string(payload) {
		t.Fatalf("payload = %s, want %s", record.Payload, payload)
	}
}

func TestReadRawHookRecordRejectsInvalidRef(t *testing.T) {
	for _, rawRef := range []string{"", "../codex:1", "codex:0", "codex:not-a-number", "codex:1:2"} {
		if _, err := ReadRawHookRecord(t.TempDir(), rawRef); err == nil {
			t.Fatalf("ReadRawHookRecord(%q) succeeded", rawRef)
		}
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
