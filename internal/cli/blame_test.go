package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	blameengine "github.com/AadiJo/turnal/internal/blame"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
)

func TestBlameCommandShowsLineAttribution(t *testing.T) {
	root, repo, sessionID, turnID := createTurnWithDiff(t)
	t.Chdir(root.String())

	if _, err := eventlog.Open(repo.MetadataDir).Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypePromptUser,
		Adapter:   primitives.AdapterCodex,
		Payload:   json.RawMessage(`{"text":"change app.txt"}`),
	}); err != nil {
		t.Fatalf("append prompt event: %v", err)
	}
	if _, err := eventlog.Open(repo.MetadataDir).Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypeToolCall,
		Adapter:   primitives.AdapterCodex,
		Payload:   json.RawMessage(`{"tool_name":"apply_patch"}`),
	}); err != nil {
		t.Fatalf("append tool event: %v", err)
	}

	jsonOutput := runRootStdout(t, "blame", "app.txt:1", "--json")
	var result blameengine.Result
	if err := json.Unmarshal([]byte(jsonOutput), &result); err != nil {
		t.Fatalf("unmarshal blame json: %v\n%s", err, jsonOutput)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("entries len = %d, want 1", len(result.Entries))
	}
	entry := result.Entries[0]
	if entry.Text != "after" {
		t.Fatalf("entry text = %q, want after", entry.Text)
	}
	if entry.Origin.SessionID != sessionID || entry.Origin.TurnID != turnID {
		t.Fatalf("origin = %#v, want %s turn %s", entry.Origin, sessionID, turnID)
	}
	if entry.Origin.Prompt != "change app.txt" {
		t.Fatalf("prompt = %q, want change app.txt", entry.Origin.Prompt)
	}
	if len(entry.Origin.ToolNames) != 1 || entry.Origin.ToolNames[0] != "apply_patch" {
		t.Fatalf("tools = %#v, want apply_patch", entry.Origin.ToolNames)
	}

	textOutput := runRootStdout(t, "blame", "app.txt:1", "--verbose")
	for _, want := range []string{
		"[codex ",
		"] turn 1",
		"1 | after",
		"adapter: codex",
		"problem: no agent intent recorded for this change",
		"human request: change app.txt",
		"tools: apply_patch",
	} {
		if !strings.Contains(textOutput, want) {
			t.Fatalf("text output missing %q:\n%s", want, textOutput)
		}
	}
}

func TestBlameTextUsesLogStyleTurnLabelAndPrompt(t *testing.T) {
	sessionID := sessionID(t, "codex-sess_7f3a9c2d")
	turnID, _ := primitives.NewTurnID(2)
	sessionStartedAt := time.Date(2026, 7, 6, 3, 12, 0, 0, time.UTC)
	turnAt := time.Date(2026, 7, 6, 3, 15, 0, 0, time.UTC)

	var out bytes.Buffer
	err := writeBlameText(&out, blameengine.Result{
		Path: "hello_world.py",
		Sessions: []blameengine.SessionSummary{
			{
				ID:        sessionID,
				Adapter:   "codex",
				StartedAt: sessionStartedAt,
			},
		},
		Entries: []blameengine.Entry{
			{
				Line: 2,
				Text: "    for _ in range(5):",
				Origin: blameengine.Origin{
					Kind:      "turn",
					SessionID: sessionID,
					TurnID:    turnID,
					Time:      turnAt,
					Adapter:   "codex",
					Prompt:    "make a python script to say hello world",
				},
			},
		},
	}, false)
	if err != nil {
		t.Fatalf("writeBlameText: %v", err)
	}

	output := out.String()
	for _, want := range []string{
		"03:15 [codex 03:12] turn 2      2 |     for _ in range(5):",
		"Human request: \"make a python script to say hello world\"",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("blame text missing %q:\n%s", want, output)
		}
	}
}
