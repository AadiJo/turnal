package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/discovery"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/fsidentity"
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

	// Standing outside any recorded project proves the search does not depend
	// on the current workspace.
	t.Chdir(t.TempDir())
	output := runRootStdout(t, "search", "discoveryneedle", "--all-projects", "--json")
	var results []discovery.Result
	if err := json.Unmarshal([]byte(output), &results); err != nil {
		t.Fatalf("decode cross-project search: %v\n%s", err, output)
	}
	if len(results) != 2 {
		t.Fatalf("cross-project results = %#v, want 2", results)
	}

	wantRoots := []string{firstRoot.String(), secondRoot.String()}
	found := make([]bool, len(wantRoots))
	for _, result := range results {
		if result.Project == nil {
			t.Fatalf("cross-project result omitted its project: %#v", result)
		}
		if result.Match.Kind != "keyword" {
			t.Fatalf("match = %#v, want keyword", result.Match)
		}
		matched := false
		for index, root := range wantRoots {
			if fsidentity.Same(result.Project.Root, root) {
				found[index] = true
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("unexpected project root in result: %#v", result.Project)
		}
	}
	for index, ok := range found {
		if !ok {
			t.Fatalf("cross-project search omitted %s", wantRoots[index])
		}
	}
}

// A project whose index is missing must degrade to a warning rather than
// failing the whole machine-wide query.
func TestSearchAllProjectsWarnsAboutUnusableIndex(t *testing.T) {
	t.Setenv("TURNAL_STATE_DIR", t.TempDir())

	healthyRoot, healthyRepo, healthySession, healthyTurn := createRegisteredSearchTurn(t)
	appendSearchPrompt(t, healthyRepo.MetadataDir, healthySession, healthyTurn, "healthy discoveryneedle")
	t.Chdir(healthyRoot.String())
	_ = runRootStdout(t, "reindex")

	// Registered, but never reindexed, so it has no index to open.
	brokenRoot, brokenRepo, brokenSession, brokenTurn := createRegisteredSearchTurn(t)
	appendSearchPrompt(t, brokenRepo.MetadataDir, brokenSession, brokenTurn, "broken discoveryneedle")

	t.Chdir(t.TempDir())
	cmd := NewRootCmd()
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"search", "discoveryneedle", "--all-projects"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("search --all-projects: %v\nstderr=%s", err, stderr.String())
	}

	if !strings.Contains(stripANSI(out.String()), "healthy discoveryneedle") {
		t.Fatalf("healthy project result missing:\n%s", out.String())
	}
	warning := stderr.String()
	if !strings.Contains(warning, "warning:") || !strings.Contains(warning, filepath.Base(brokenRoot.String())) {
		t.Fatalf("stderr did not warn about the unusable project:\n%s", warning)
	}
	if !strings.Contains(warning, "turnal reindex") {
		t.Fatalf("warning did not say how to fix the project:\n%s", warning)
	}
}

// When no project is searchable the error says only that, so the per-project
// warnings are the user's only record of which projects failed and why.
func TestSearchAllProjectsWarnsWhenEveryIndexIsUnusable(t *testing.T) {
	t.Setenv("TURNAL_STATE_DIR", t.TempDir())

	brokenRoot, brokenRepo, brokenSession, brokenTurn := createRegisteredSearchTurn(t)
	appendSearchPrompt(t, brokenRepo.MetadataDir, brokenSession, brokenTurn, "broken discoveryneedle")

	t.Chdir(t.TempDir())
	cmd := NewRootCmd()
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"search", "discoveryneedle", "--all-projects"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "no local project has a usable search index") {
		t.Fatalf("search --all-projects error = %v, want no usable index", err)
	}

	warning := stderr.String()
	if !strings.Contains(warning, filepath.Base(brokenRoot.String())) {
		t.Fatalf("stderr did not name the unusable project:\n%s", warning)
	}
	if !strings.Contains(warning, "turnal reindex") {
		t.Fatalf("warning did not say how to fix the project:\n%s", warning)
	}
}

func TestSearchCommandFindsMeaningWithoutSharedTerms(t *testing.T) {
	root, repo, sessionID, turnID := createTurnWithDiff(t)
	appendSearchPrompt(t, repo.MetadataDir, sessionID, turnID, "Context upload must not block the source push.")
	t.Chdir(root.String())
	_ = runRootStdout(t, "reindex")

	stubSearchEncoder(t)

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

	// Without --semantic the same query shares no terms with the prompt, so
	// keyword search alone must find nothing.
	keywordOnly := stripANSI(runRootStdout(t, "search", "why does history sync fail open"))
	if !strings.Contains(keywordOnly, "no matches") {
		t.Fatalf("keyword-only search = %q, want no matches", keywordOnly)
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

// stubSearchEncoder replaces the downloaded model with a fixed vector space so
// semantic tests stay offline and deterministic.
func stubSearchEncoder(t *testing.T) {
	t.Helper()
	previous := newSearchEncoder
	newSearchEncoder = func(context.Context) (discovery.Encoder, error) {
		return searchFixtureEncoder{}, nil
	}
	t.Cleanup(func() { newSearchEncoder = previous })
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
