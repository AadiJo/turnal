package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/recall"
)

func TestShowCommandResolvesBareTurn(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	appendPromptEvent(t, repo.MetadataDir, sessionID, turnID, "hello", time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC))

	recalled := runShowJSON(t, "1", "--json")
	if recalled.SessionID != sessionID || recalled.TurnID != turnID {
		t.Fatalf("show target = %s turn %s, want %s turn %s", recalled.SessionID, recalled.TurnID, sessionID, turnID)
	}
	if len(recalled.RawRecords) != 0 || recalled.Transcript != nil {
		t.Fatalf("show should omit raw/transcript by default: raw=%#v transcript=%#v", recalled.RawRecords, recalled.Transcript)
	}
}

func TestShowCommandDefaultsToLatestTurn(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	firstSession := sessionID(t, "first")
	secondSession := sessionID(t, "second")
	turnID, _ := primitives.NewTurnID(1)
	appendPromptEvent(t, repo.MetadataDir, firstSession, turnID, "first", time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC))
	appendPromptEvent(t, repo.MetadataDir, secondSession, turnID, "second", time.Date(2026, 7, 6, 12, 5, 0, 0, time.UTC))

	recalled := runShowJSON(t, "--json")
	if recalled.SessionID != secondSession || recalled.TurnID != turnID {
		t.Fatalf("latest = %s turn %s, want %s turn %s", recalled.SessionID, recalled.TurnID, secondSession, turnID)
	}
}

func TestShowCommandReportsAmbiguousBareTurn(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	turnID, _ := primitives.NewTurnID(1)
	appendPromptEvent(t, repo.MetadataDir, sessionID(t, "first"), turnID, "first", time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC))
	appendPromptEvent(t, repo.MetadataDir, sessionID(t, "second"), turnID, "second", time.Date(2026, 7, 6, 12, 5, 0, 0, time.UTC))

	cmd := NewRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"show", "1"})
	err = cmd.Execute()
	if err == nil {
		t.Fatal("show 1 succeeded for ambiguous turn")
	}
	if !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "first,second") {
		t.Fatalf("show ambiguous error = %v", err)
	}
}

func TestShowCommandFullIncludesTranscript(t *testing.T) {
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
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"full transcript answer"}]}}`,
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
	appendPromptEvent(t, repo.MetadataDir, sessionID, turnID, "hello", time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC))

	recalled := runShowJSON(t, "demo:latest", "--json", "--full")
	if recalled.Transcript == nil || len(recalled.Transcript.Messages) != 2 {
		t.Fatalf("transcript = %#v", recalled.Transcript)
	}
	if recalled.Transcript.Messages[0].Role != "user" || recalled.Transcript.Messages[1].Text != "full transcript answer" {
		t.Fatalf("transcript messages = %#v", recalled.Transcript.Messages)
	}
}

func runShowJSON(t *testing.T, args ...string) recall.Turn {
	t.Helper()
	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(append([]string{"show"}, args...))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute show %v: %v", args, err)
	}
	var recalled recall.Turn
	if err := json.Unmarshal(out.Bytes(), &recalled); err != nil {
		t.Fatalf("unmarshal show output: %v\n%s", err, out.String())
	}
	return recalled
}

func appendPromptEvent(t *testing.T, metadataDir string, sessionID primitives.SessionID, turnID primitives.TurnID, text string, timestamp time.Time) {
	t.Helper()
	eventTime, err := primitives.NewTimestamp(timestamp)
	if err != nil {
		t.Fatalf("NewTimestamp: %v", err)
	}
	if _, err := eventlog.Open(metadataDir).Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypePromptUser,
		Adapter:   primitives.AdapterClaudeCode,
		Time:      eventTime,
		Payload:   json.RawMessage(`{"text":` + quote(text) + `}`),
	}); err != nil {
		t.Fatalf("Append prompt: %v", err)
	}
}
