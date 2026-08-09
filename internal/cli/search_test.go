package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/discovery"
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
		"why: keyword match",
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

func TestSearchCommandSearchesEveryRegisteredProject(t *testing.T) {
	t.Setenv("TURNAL_STATE_DIR", t.TempDir())

	firstRoot, firstRepo, firstSession, firstTurn := createRegisteredSearchTurn(t)
	appendSearchPrompt(t, firstRepo.MetadataDir, firstSession, firstTurn, "first discoveryneedle")
	t.Chdir(firstRoot.String())
	_ = runRootStdout(t, "reindex")

	secondRoot, secondRepo, secondSession, secondTurn := createRegisteredSearchTurn(t)
	appendSearchPrompt(t, secondRepo.MetadataDir, secondSession, secondTurn, "second discoveryneedle")
	t.Chdir(secondRoot.String())
	_ = runRootStdout(t, "reindex")

	t.Chdir(t.TempDir())
	output := runRootStdout(t, "search", "discoveryneedle", "--all-projects", "--json")
	var results []discovery.Result
	if err := json.Unmarshal([]byte(output), &results); err != nil {
		t.Fatalf("decode cross-project search: %v\n%s", err, output)
	}
	if len(results) != 2 {
		t.Fatalf("cross-project results = %#v, want 2", results)
	}
	roots := map[string]bool{
		firstRoot.String():  false,
		secondRoot.String(): false,
	}
	for _, result := range results {
		if result.Project == nil {
			t.Fatalf("cross-project result omitted project: %#v", result)
		}
		if _, ok := roots[result.Project.Root]; !ok {
			t.Fatalf("unexpected project root in result: %#v", result.Project)
		}
		roots[result.Project.Root] = true
		if result.Match.Kind != "keyword" {
			t.Fatalf("cross-project match = %#v, want keyword", result.Match)
		}
	}
	for root, found := range roots {
		if !found {
			t.Fatalf("cross-project search omitted %s", root)
		}
	}
}

func TestSearchCommandFindsMeaningWithoutSharedTerms(t *testing.T) {
	t.Setenv("TURNAL_STATE_DIR", t.TempDir())
	root, repo, sessionID, turnID := createTurnWithDiff(t)
	appendSearchPrompt(t, repo.MetadataDir, sessionID, turnID, "Context upload must not block the source push.")
	t.Chdir(root.String())
	_ = runRootStdout(t, "reindex")

	previous := newSearchEncoder
	newSearchEncoder = func(context.Context) (discovery.Encoder, error) {
		return searchFixtureEncoder{}, nil
	}
	t.Cleanup(func() { newSearchEncoder = previous })

	output := stripANSI(runRootStdout(t, "search", "why does history sync fail open", "--semantic"))
	for _, want := range []string{
		"demo:1",
		"prompt: Context upload must not block the source push.",
		"why: meaning similarity",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("semantic search output missing %q:\n%s", want, output)
		}
	}
}

func TestSearchCommandRejectsOverlappingMachineScopes(t *testing.T) {
	cmd := NewRootCmd()
	cmd.SetArgs([]string{"search", "needle", "--all-projects", "--all-worktrees"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "already includes every worktree") {
		t.Fatalf("overlapping scope error = %v", err)
	}
}

func appendSearchPrompt(t *testing.T, metadataDir string, sessionID primitives.SessionID, turnID primitives.TurnID, prompt string) {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"text": prompt})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eventlog.Open(metadataDir).Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypePromptUser,
		Adapter:   primitives.AdapterCodex,
		Payload:   payload,
	}); err != nil {
		t.Fatalf("append search prompt: %v", err)
	}
}

func createRegisteredSearchTurn(t *testing.T) (primitives.WorkspaceRoot, *checkpoint.Repo, primitives.SessionID, primitives.TurnID) {
	t.Helper()
	requireGit(t)
	root, err := primitives.ParseWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := repo.RegisterStore(); err != nil {
		t.Fatalf("RegisterStore: %v", err)
	}
	turnID, _ := primitives.NewTurnID(1)
	return root, repo, sessionID(t, "demo"), turnID
}

type searchFixtureEncoder struct{}

func (searchFixtureEncoder) EncodeMany(texts []string) ([][]float32, error) {
	vectors := make([][]float32, 0, len(texts))
	for _, text := range texts {
		switch {
		case strings.Contains(text, "history sync fail open"):
			vectors = append(vectors, []float32{1, 0})
		case strings.Contains(text, "Context upload must not block"):
			vectors = append(vectors, []float32{0.9, 0.1})
		default:
			vectors = append(vectors, []float32{0, 1})
		}
	}
	return vectors, nil
}
