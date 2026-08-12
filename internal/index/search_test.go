package index

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/primitives"
)

func TestSearchFindsPromptToolPathAndEventOnlyTurns(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	demoSession := sessionID(t, "demo")
	firstTurn, _ := primitives.NewTurnID(1)
	secondTurn, _ := primitives.NewTurnID(2)

	writeFile(t, root, "app.txt", "before\n")
	if _, err := repo.CreateCheckpoint(demoSession, firstTurn, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("pre checkpoint: %v", err)
	}
	writeFile(t, root, "app.txt", "after\n")
	if _, err := repo.CreateCheckpoint(demoSession, firstTurn, primitives.CheckpointPhasePost); err != nil {
		t.Fatalf("post checkpoint: %v", err)
	}

	appendEvent(t, repo.MetadataDir, eventlog.AppendInput{
		SessionID: demoSession,
		TurnID:    &firstTurn,
		Type:      primitives.EventTypePromptUser,
		Adapter:   primitives.AdapterCodex,
		Time:      timestamp(t, time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)),
		Payload:   json.RawMessage(`{"text":"update the app file","model":"gpt-5.6-sol"}`),
	})
	appendEvent(t, repo.MetadataDir, eventlog.AppendInput{
		SessionID: demoSession,
		TurnID:    &firstTurn,
		Type:      primitives.EventTypeToolCall,
		Adapter:   primitives.AdapterCodex,
		Time:      timestamp(t, time.Date(2026, 7, 6, 12, 1, 0, 0, time.UTC)),
		Payload:   json.RawMessage(`{"tool_name":"apply_patch"}`),
	})
	appendEvent(t, repo.MetadataDir, eventlog.AppendInput{
		SessionID: demoSession,
		TurnID:    &firstTurn,
		Type:      primitives.EventTypeAgentIntent,
		Adapter:   primitives.AdapterCodex,
		Time:      timestamp(t, time.Date(2026, 7, 6, 12, 1, 30, 0, time.UTC)),
		Payload:   json.RawMessage(`{"problem":"retry delay survives a successful request"}`),
	})
	appendEvent(t, repo.MetadataDir, eventlog.AppendInput{
		SessionID: demoSession,
		TurnID:    &secondTurn,
		Type:      primitives.EventTypePromptUser,
		Adapter:   primitives.AdapterCodex,
		Time:      timestamp(t, time.Date(2026, 7, 6, 12, 2, 0, 0, time.UTC)),
		Payload:   json.RawMessage(`{"text":"event only search needle"}`),
	})

	stats, err := Rebuild(repo)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if stats.Turns != 1 || stats.SearchDocuments != 2 {
		t.Fatalf("stats turns/search docs = %d/%d, want 1/2", stats.Turns, stats.SearchDocuments)
	}

	store, err := Open(repo.MetadataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	pathResults, err := store.Search(SearchQuery{Query: "app.txt"})
	if err != nil {
		t.Fatalf("Search path: %v", err)
	}
	if len(pathResults) != 1 || pathResults[0].TurnID != firstTurn || len(pathResults[0].Paths) != 1 || pathResults[0].Paths[0] != "app.txt" {
		t.Fatalf("path results = %#v, want first turn touching app.txt", pathResults)
	}

	toolResults, err := store.Search(SearchQuery{Query: "apply_patch"})
	if err != nil {
		t.Fatalf("Search tool: %v", err)
	}
	if len(toolResults) != 1 || toolResults[0].TurnID != firstTurn || len(toolResults[0].ToolNames) != 1 || toolResults[0].ToolNames[0] != "apply_patch" {
		t.Fatalf("tool results = %#v, want first turn using apply_patch", toolResults)
	}
	modelResults, err := store.Search(SearchQuery{Query: "gpt-5.6-sol"})
	if err != nil {
		t.Fatalf("Search model: %v", err)
	}
	if len(modelResults) != 1 || modelResults[0].TurnID != firstTurn || modelResults[0].Model != "gpt-5.6-sol" {
		t.Fatalf("model results = %#v, want first turn using gpt-5.6-sol", modelResults)
	}

	intentResults, err := store.Search(SearchQuery{Query: "retry delay"})
	if err != nil {
		t.Fatalf("Search intent: %v", err)
	}
	if len(intentResults) != 1 || intentResults[0].TurnID != firstTurn {
		t.Fatalf("intent results = %#v, want first turn with matching agent intent", intentResults)
	}

	eventOnlyResults, err := store.Search(SearchQuery{Query: "needle"})
	if err != nil {
		t.Fatalf("Search event-only: %v", err)
	}
	if len(eventOnlyResults) != 1 || eventOnlyResults[0].TurnID != secondTurn || eventOnlyResults[0].Prompt != "event only search needle" {
		t.Fatalf("event-only results = %#v, want second turn prompt", eventOnlyResults)
	}

	otherSession := sessionID(t, "other")
	filtered, err := store.Search(SearchQuery{Query: "needle", Session: otherSession})
	if err != nil {
		t.Fatalf("Search filtered: %v", err)
	}
	if len(filtered) != 0 {
		t.Fatalf("filtered results = %#v, want none", filtered)
	}

	documents, err := store.SearchDocuments(SearchQuery{})
	if err != nil {
		t.Fatalf("SearchDocuments: %v", err)
	}
	if len(documents) != 2 {
		t.Fatalf("search documents = %d, want every indexed turn", len(documents))
	}
	if documents[0].Result.TurnID != firstTurn {
		t.Fatalf("first document turn = %s, want %s", documents[0].Result.TurnID, firstTurn)
	}
	if !strings.Contains(documents[0].Text, "update the app file") {
		t.Fatalf("document text lost the prompt:\n%s", documents[0].Text)
	}
	// Meaning matching depends on a narrow document: keyword surfaces such as
	// the model name, tool names, and paths must stay out of the embedded text.
	for _, unwanted := range []string{"gpt-5.6-sol", "apply_patch", "app.txt", "codex"} {
		if strings.Contains(documents[0].Text, unwanted) {
			t.Fatalf("document text should not carry %q:\n%s", unwanted, documents[0].Text)
		}
	}

	filteredDocuments, err := store.SearchDocuments(SearchQuery{Session: otherSession})
	if err != nil {
		t.Fatalf("SearchDocuments filtered: %v", err)
	}
	if len(filteredDocuments) != 0 {
		t.Fatalf("filtered documents = %#v, want none", filteredDocuments)
	}
}
