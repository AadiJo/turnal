package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/sessionhistory"
)

func TestImportCommandDryRunApplySearchAndIdempotence(t *testing.T) {
	root, repo, head := initializedImportWorkspace(t)
	t.Chdir(root.String())
	transcriptDir := t.TempDir()
	providerSessionID := "019ff7d0-df43-7c42-9be5-85d7c7886d4c"
	writeCodexImportTranscript(t, transcriptDir, root.String(), providerSessionID)

	dryRunOutput := runRootStdout(t, "import", "codex", "--path", transcriptDir, "--dry-run", "--json")
	var plan sessionhistory.ImportPlan
	if err := json.Unmarshal([]byte(dryRunOutput), &plan); err != nil {
		t.Fatalf("decode dry-run plan: %v\n%s", err, dryRunOutput)
	}
	if !plan.DryRun || len(plan.Sessions) != 1 || plan.Sessions[0].State != "ready" || plan.Sessions[0].TurnCount != 1 {
		t.Fatalf("dry-run plan = %#v", plan)
	}
	if sessions, err := repo.EventLog().ListSessions(); err != nil || len(sessions) != 0 {
		t.Fatalf("dry-run durable sessions = %v, %v", sessions, err)
	}

	output := runRootStdout(t, "import", "codex", "--path", transcriptDir, "--json")
	var applied struct {
		Result sessionhistory.ImportResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(output), &applied); err != nil {
		t.Fatalf("decode import result: %v\n%s", err, output)
	}
	if applied.Result.ImportedSessions != 1 || applied.Result.ImportedTurns != 1 || applied.Result.AppendedEvents != 6 {
		t.Fatalf("import result = %#v", applied.Result)
	}

	sessionID, _ := primitives.ParseSessionID(providerSessionID)
	events, err := repo.EventLog().Read(sessionID)
	if err != nil {
		t.Fatalf("read imported events: %v", err)
	}
	if len(events) != 6 {
		t.Fatalf("imported events = %d, want 6", len(events))
	}
	info := sessionhistory.InspectSession(events)
	if info.Origin != sessionhistory.OriginImported || !info.ReadOnly || info.ProviderSessionID != providerSessionID {
		t.Fatalf("imported session info = %#v", info)
	}

	sessionsOutput := stripANSI(runRootStdout(t, "sessions"))
	for _, want := range []string{"[IMPORTED] " + providerSessionID, "origin   imported / read-only", "tools    apply_patch"} {
		if !strings.Contains(sessionsOutput, want) {
			t.Fatalf("sessions output missing %q:\n%s", want, sessionsOutput)
		}
	}
	showOutput := runRootStdout(t, "show", providerSessionID+":1")
	for _, want := range []string{"session.import", "prompt.user", "assistant.message", "make the queue idempotent", "complete: false"} {
		if !strings.Contains(showOutput, want) {
			t.Fatalf("show output missing %q:\n%s", want, showOutput)
		}
	}

	_ = runRootStdout(t, "reindex")
	searchOutput := stripANSI(runRootStdout(t, "search", "idempotent queue"))
	if !strings.Contains(searchOutput, providerSessionID+":1") || !strings.Contains(searchOutput, "prompt: make the queue idempotent") {
		t.Fatalf("search output omitted imported turn:\n%s", searchOutput)
	}

	repeatOutput := runRootStdout(t, "import", "codex", "--path", transcriptDir, "--json")
	if err := json.Unmarshal([]byte(repeatOutput), &applied); err != nil {
		t.Fatalf("decode repeat import: %v\n%s", err, repeatOutput)
	}
	if applied.Result.ImportedSessions != 0 || applied.Result.AppendedEvents != 0 || applied.Result.SkippedSessions != 1 {
		t.Fatalf("repeat import result = %#v", applied.Result)
	}
	if got := strings.TrimSpace(runUserGit(t, root.String(), "rev-parse", "HEAD")); got != head {
		t.Fatalf("import changed source HEAD: got %s, want %s", got, head)
	}
}

func TestSessionAttachImportsMissedSessionWithoutRewritingCommit(t *testing.T) {
	root, repo, head := initializedImportWorkspace(t)
	t.Chdir(root.String())
	transcriptDir := t.TempDir()
	providerSessionID := "019ff7d3-eb26-7532-9c10-aead9430c37a"
	writeCodexImportTranscript(t, transcriptDir, root.String(), providerSessionID)
	commitBefore := runUserGit(t, root.String(), "cat-file", "-p", "HEAD")
	statusBefore := runUserGit(t, root.String(), "status", "--porcelain=v1", "--untracked-files=all")

	dryRun := runRootStdout(t, "session", "attach", providerSessionID, "--adapter", "codex", "--path", transcriptDir, "--dry-run", "--json")
	if !strings.Contains(dryRun, `"history_rewritten": false`) || !strings.Contains(dryRun, `"import_plan"`) {
		t.Fatalf("attach dry-run output = %s", dryRun)
	}
	if sessions, err := repo.EventLog().ListSessions(); err != nil || len(sessions) != 0 {
		t.Fatalf("attach dry-run durable sessions = %v, %v", sessions, err)
	}

	attachOutput := runRootStdout(t, "session", "attach", providerSessionID, "--adapter", "codex", "--path", transcriptDir, "--json")
	if !strings.Contains(attachOutput, `"attached": true`) || !strings.Contains(attachOutput, `"history_rewritten": false`) {
		t.Fatalf("attach output = %s", attachOutput)
	}
	if got := strings.TrimSpace(runUserGit(t, root.String(), "rev-parse", "HEAD")); got != head {
		t.Fatalf("attach changed source HEAD: got %s, want %s", got, head)
	}
	if got := runUserGit(t, root.String(), "cat-file", "-p", "HEAD"); got != commitBefore {
		t.Fatalf("attach rewrote commit object:\n%s", got)
	}
	if got := runUserGit(t, root.String(), "status", "--porcelain=v1", "--untracked-files=all"); got != statusBefore {
		t.Fatalf("attach changed source worktree status:\nbefore=%q\nafter=%q", statusBefore, got)
	}

	sessionID, _ := primitives.ParseSessionID(providerSessionID)
	events, err := repo.EventLog().Read(sessionID)
	if err != nil {
		t.Fatalf("read attached session: %v", err)
	}
	info := sessionhistory.InspectSession(events)
	if len(info.Attachments) != 1 || info.Attachments[0].CommitSHA.String() != head {
		t.Fatalf("session attachments = %#v", info.Attachments)
	}

	repeat := runRootStdout(t, "session", "attach", providerSessionID, "--commit", head, "--json")
	if !strings.Contains(repeat, `"attached": false`) {
		t.Fatalf("repeat attach was not idempotent: %s", repeat)
	}
	if got := len(sessionhistory.InspectSession(mustSessionEvents(t, repo, sessionID)).Attachments); got != 1 {
		t.Fatalf("attachment count after repeat = %d, want 1", got)
	}
}

func TestImportSkipsNativelyRecordedSession(t *testing.T) {
	root, repo, _ := initializedImportWorkspace(t)
	t.Chdir(root.String())
	transcriptDir := t.TempDir()
	providerSessionID := "019ff7d6-f107-78e2-8657-909a16053bc2"
	writeCodexImportTranscript(t, transcriptDir, root.String(), providerSessionID)
	sessionID, _ := primitives.ParseSessionID(providerSessionID)
	if _, err := repo.EventLog().Append(eventlog.AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypeSessionStart,
		Adapter:   primitives.AdapterCodex,
		SourceID:  "native-session-start",
		Payload:   json.RawMessage(`{"model":"gpt-5.6-sol"}`),
	}); err != nil {
		t.Fatalf("append native session event: %v", err)
	}

	previewOutput := runRootStdout(t, "import", "codex", "--path", transcriptDir, "--dry-run", "--json")
	var preview sessionhistory.ImportPlan
	if err := json.Unmarshal([]byte(previewOutput), &preview); err != nil {
		t.Fatalf("decode import preview: %v\n%s", err, previewOutput)
	}
	if len(preview.Sessions) != 1 || preview.Sessions[0].State != "already-recorded" || preview.Sessions[0].PendingEvents != 0 {
		t.Fatalf("native-session import preview = %#v", preview)
	}

	_ = runRootStdout(t, "import", "codex", "--path", transcriptDir)
	events := mustSessionEvents(t, repo, sessionID)
	if len(events) != 1 || sessionhistory.InspectSession(events).Origin != sessionhistory.OriginNative {
		t.Fatalf("native session was changed by import: %#v", events)
	}
}

func TestImportAppliesWorkspaceSecretsPolicy(t *testing.T) {
	root, repo, _ := initializedImportWorkspace(t)
	t.Chdir(root.String())
	config := "version = 1\n\n[secrets]\nstore_prompts = false\nstore_tool_io = false\n"
	if err := os.WriteFile(filepath.Join(repo.MetadataDir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}
	transcriptDir := t.TempDir()
	providerSessionID := "019ff7d8-14f5-712f-9199-5e10539052e1"
	writeCodexImportTranscript(t, transcriptDir, root.String(), providerSessionID)

	_ = runRootStdout(t, "import", "codex", "--path", transcriptDir)
	sessionID, _ := primitives.ParseSessionID(providerSessionID)
	encoded, err := json.Marshal(mustSessionEvents(t, repo, sessionID))
	if err != nil {
		t.Fatalf("marshal imported events: %v", err)
	}
	text := string(encoded)
	for _, secret := range []string{"make the queue idempotent", "queue.go", "done", "the queue now retries safely"} {
		if strings.Contains(text, secret) {
			t.Fatalf("imported history retained redacted value %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, primitives.SecretsRedactionText) || !strings.Contains(text, `"redacted":true`) {
		t.Fatalf("imported history omitted redaction markers: %s", text)
	}
}

func initializedImportWorkspace(t *testing.T) (primitives.WorkspaceRoot, *checkpoint.Repo, string) {
	t.Helper()
	requireGit(t)
	root := workspaceRoot(t)
	runUserGit(t, root.String(), "init", "-q")
	runUserGit(t, root.String(), "config", "user.email", "turnal-tests@example.com")
	runUserGit(t, root.String(), "config", "user.name", "Turnal Tests")
	writeFile(t, root, "README.md", "import fixture\n")
	runUserGit(t, root.String(), "add", "README.md")
	runUserGit(t, root.String(), "commit", "-q", "-m", "initial")
	head := strings.TrimSpace(runUserGit(t, root.String(), "rev-parse", "HEAD"))
	bootstrap, err := checkpoint.Bootstrap(root)
	if err != nil {
		t.Fatalf("checkpoint.Bootstrap: %v", err)
	}
	return root, bootstrap.Repo, head
}

func writeCodexImportTranscript(t *testing.T, dir, root, sessionID string) {
	t.Helper()
	records := []map[string]any{
		{"timestamp": "2026-08-01T10:00:00Z", "type": "session_meta", "payload": map[string]any{"id": sessionID, "cwd": root}},
		{"timestamp": "2026-08-01T10:00:01Z", "type": "turn_context", "payload": map[string]any{"model": "gpt-5.6-sol"}},
		{"timestamp": "2026-08-01T10:00:02Z", "type": "event_msg", "payload": map[string]any{"type": "user_message", "message": "make the queue idempotent"}},
		{"timestamp": "2026-08-01T10:00:03Z", "type": "response_item", "payload": map[string]any{"type": "function_call", "name": "apply_patch", "call_id": "call-1", "arguments": `{"path":"queue.go"}`}},
		{"timestamp": "2026-08-01T10:00:04Z", "type": "response_item", "payload": map[string]any{"type": "function_call_output", "call_id": "call-1", "output": "done"}},
		{"timestamp": "2026-08-01T10:00:05Z", "type": "response_item", "payload": map[string]any{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "the queue now retries safely"}}}},
		{"timestamp": "2026-08-01T10:00:06Z", "type": "event_msg", "payload": map[string]any{"type": "task_complete", "last_agent_message": "the queue now retries safely"}},
	}
	var lines []string
	for _, record := range records {
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("marshal transcript record: %v", err)
		}
		lines = append(lines, string(encoded))
	}
	path := filepath.Join(dir, fmt.Sprintf("rollout-%s.jsonl", sessionID))
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write Codex transcript: %v", err)
	}
}

func mustSessionEvents(t *testing.T, repo *checkpoint.Repo, sessionID primitives.SessionID) []eventlog.Event {
	t.Helper()
	events, err := repo.EventLog().Read(sessionID)
	if err != nil {
		t.Fatalf("read session events: %v", err)
	}
	return events
}
