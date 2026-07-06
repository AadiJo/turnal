package cli

import (
	"encoding/json"
	"strings"
	"testing"

	blameengine "agent-vcs-again/internal/blame"
	eventlog "agent-vcs-again/internal/events"
	"agent-vcs-again/internal/primitives"
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
		"demo:turn:1",
		"1 | after",
		"adapter: codex",
		"prompt: change app.txt",
		"tools: apply_patch",
	} {
		if !strings.Contains(textOutput, want) {
			t.Fatalf("text output missing %q:\n%s", want, textOutput)
		}
	}
}
