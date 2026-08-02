package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
)

func TestSearchCommandUsesRebuiltIndex(t *testing.T) {
	root, repo, sessionID, turnID := createTurnWithDiff(t)
	t.Chdir(root.String())

	if _, err := eventlog.Open(repo.MetadataDir).Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypePromptUser,
		Adapter:   primitives.AdapterCodex,
		Payload:   json.RawMessage(`{"text":"change app.txt with search command","model":"gpt-5.6-sol"}`),
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

	cmd := NewRootCmd()
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"search", "app.txt"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "search index is missing") {
		t.Fatalf("search without index error = %v, want missing index", err)
	}

	_ = runRootStdout(t, "reindex")
	output := stripANSI(runRootStdout(t, "search", "app.txt"))
	for _, want := range []string{
		"demo:1",
		"codex / gpt-5.6-sol",
		"prompt: change app.txt with search command",
		"tools: apply_patch",
		"files: app.txt",
		"match:",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("search output missing %q:\n%s", want, output)
		}
	}

	filtered := stripANSI(runRootStdout(t, "search", "app.txt", "--session", "other"))
	if !strings.Contains(filtered, "no matches") {
		t.Fatalf("filtered search output = %q, want no matches", filtered)
	}
}
