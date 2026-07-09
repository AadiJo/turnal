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
	"github.com/AadiJo/turnal/internal/recall"
)

func TestTurnRecallCommandJSON(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	if _, err := eventlog.Open(repo.MetadataDir).Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypePromptUser,
		Adapter:   primitives.AdapterCodex,
		Payload:   json.RawMessage(`{"text":"hello"}`),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"turn", "recall", "--session", "demo", "--turn", "1", "--json", "--raw=false"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var recalled recall.Turn
	if err := json.Unmarshal(out.Bytes(), &recalled); err != nil {
		t.Fatalf("unmarshal recall output: %v\n%s", err, out.String())
	}
	if recalled.SessionID != sessionID || recalled.TurnID != turnID {
		t.Fatalf("recalled target = %s turn %s, want %s turn %s", recalled.SessionID, recalled.TurnID, sessionID, turnID)
	}
	if len(recalled.Events) != 1 || recalled.Events[0].Type != primitives.EventTypePromptUser {
		t.Fatalf("events = %#v", recalled.Events)
	}
	if len(recalled.RawRecords) != 0 || len(recalled.RawRecordErrors) != 0 {
		t.Fatalf("raw output should be disabled: records=%#v errors=%#v", recalled.RawRecords, recalled.RawRecordErrors)
	}
}

func TestManualTurnCommandsAppendEventLog(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	writeFile(t, root, "app.txt", "before\n")
	start := NewRootCmd()
	var startOut bytes.Buffer
	start.SetOut(&startOut)
	start.SetErr(&startOut)
	start.SetArgs([]string{"turn", "start", "--session", "demo"})
	if err := start.Execute(); err != nil {
		t.Fatalf("turn start: %v\n%s", err, startOut.String())
	}

	writeFile(t, root, "app.txt", "after\n")
	finish := NewRootCmd()
	var finishOut bytes.Buffer
	finish.SetOut(&finishOut)
	finish.SetErr(&finishOut)
	finish.SetArgs([]string{"turn", "finish", "--session", "demo"})
	if err := finish.Execute(); err != nil {
		t.Fatalf("turn finish: %v\n%s", err, finishOut.String())
	}

	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	events, err := eventlog.Open(repo.MetadataDir).Read(sessionID)
	if err != nil {
		t.Fatalf("Read events: %v", err)
	}
	wantTypes := []primitives.EventType{
		primitives.EventTypeTurnStart,
		primitives.EventTypeCheckpoint,
		primitives.EventTypeTurnFinish,
		primitives.EventTypeCheckpoint,
	}
	wantSources := []string{
		"manual:turn:1:start",
		"manual:turn:1:checkpoint:pre",
		"manual:turn:1:finish",
		"manual:turn:1:checkpoint:post",
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("events len = %d, want %d: %#v", len(events), len(wantTypes), events)
	}
	for i, event := range events {
		if event.TurnID == nil || *event.TurnID != turnID {
			t.Fatalf("event %d turn = %#v, want %s", i, event.TurnID, turnID)
		}
		if event.Type != wantTypes[i] {
			t.Fatalf("event %d type = %s, want %s", i, event.Type, wantTypes[i])
		}
		if event.Adapter != primitives.AdapterManual {
			t.Fatalf("event %d adapter = %s, want manual", i, event.Adapter)
		}
		if !strings.HasSuffix(event.SourceID, ":"+wantSources[i]) {
			t.Fatalf("event %d source = %q, want stream-scoped suffix %q", i, event.SourceID, wantSources[i])
		}
		if event.RawRef != "" {
			t.Fatalf("event %d raw ref = %q, want empty", i, event.RawRef)
		}
	}

	recalled, err := recall.NewReader(repo.MetadataDir).RecallTurn(sessionID, turnID, recall.Options{IncludeRaw: true})
	if err != nil {
		t.Fatalf("RecallTurn: %v", err)
	}
	if !recalled.Complete {
		t.Fatalf("recalled turn complete=false: %#v", recalled)
	}
	if recalled.StartedAt == nil || recalled.FinishedAt == nil {
		t.Fatalf("recalled timestamps missing: started=%v finished=%v", recalled.StartedAt, recalled.FinishedAt)
	}
	if recalled.PreCheckpoint == nil || recalled.PostCheckpoint == nil {
		t.Fatalf("recalled checkpoints missing: pre=%#v post=%#v", recalled.PreCheckpoint, recalled.PostCheckpoint)
	}
	if len(recalled.Adapters) != 1 || recalled.Adapters[0] != primitives.AdapterManual {
		t.Fatalf("recalled adapters = %#v, want manual", recalled.Adapters)
	}
	if len(recalled.RawRecords) != 0 || len(recalled.RawRecordErrors) != 0 {
		t.Fatalf("manual turn should not reference raw adapter records: records=%#v errors=%#v", recalled.RawRecords, recalled.RawRecordErrors)
	}
}

func TestTurnRecallCommandIncludesTranscript(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	t.Setenv("CLAUDE_CONFIG_DIR", root.String())

	transcriptPath := filepath.Join(root.String(), "transcript.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"hello"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"first transcript answer"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"final transcript answer"}]}}`,
	}, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	log := eventlog.Open(repo.MetadataDir)
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: sessionID,
		Type:      primitives.EventTypeSessionStart,
		Adapter:   primitives.AdapterClaudeCode,
		Payload:   json.RawMessage(`{"provider_session_id":"demo","transcript_path":` + quote(transcriptPath) + `}`),
	}); err != nil {
		t.Fatalf("Append session: %v", err)
	}
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypePromptUser,
		Adapter:   primitives.AdapterClaudeCode,
		Payload:   json.RawMessage(`{"text":"hello"}`),
	}); err != nil {
		t.Fatalf("Append prompt: %v", err)
	}

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"turn", "recall", "--session", "demo", "--turn", "1", "--json", "--raw=false", "--transcript"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var recalled recall.Turn
	if err := json.Unmarshal(out.Bytes(), &recalled); err != nil {
		t.Fatalf("unmarshal recall output: %v\n%s", err, out.String())
	}
	if recalled.Transcript == nil || len(recalled.Transcript.Messages) != 3 {
		t.Fatalf("transcript = %#v", recalled.Transcript)
	}
	if recalled.Transcript.Messages[0].Role != "user" || recalled.Transcript.Messages[0].Text != "hello" {
		t.Fatalf("first transcript message = %#v", recalled.Transcript.Messages[0])
	}
	if recalled.Transcript.Messages[1].Text != "first transcript answer" || recalled.Transcript.Messages[2].Text != "final transcript answer" {
		t.Fatalf("transcript messages = %#v", recalled.Transcript.Messages)
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

func quote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}
