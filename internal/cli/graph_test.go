package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	queryindex "github.com/AadiJo/turnal/internal/index"
	"github.com/AadiJo/turnal/internal/primitives"
)

func TestGraphCommandShowsCheckpointGraph(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)

	writeFile(t, root, "app.txt", "before\n")
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePre); err != nil {
		t.Fatalf("pre checkpoint: %v", err)
	}
	writeFile(t, root, "app.txt", "after\n")
	if _, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost); err != nil {
		t.Fatalf("post checkpoint: %v", err)
	}

	if _, err := eventlog.Open(repo.MetadataDir).Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypePromptUser,
		Adapter:   primitives.AdapterCodex,
		Payload:   json.RawMessage(`{"text":"change app.txt"}`),
	}); err != nil {
		t.Fatalf("append prompt event: %v", err)
	}

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"graph", "--session", "demo", "--verbose"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("graph command: %v\n%s", err, out.String())
	}

	output := stripANSI(out.String())
	for _, want := range []string{
		"checkpoint graph: 1 session, 1 turn",
		"sessions: [codex ",
		"turn 1",
		"complete",
		"] turn 1      Prompt complete",
		"1 file +1 -1",
		"| session: demo",
		"events: 1 event; codex",
		"Human: \"change app.txt\"",
		"| pre:",
		"| post:",
		"| file: app.txt +1 -1",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("graph output missing %q:\n%s", want, output)
		}
	}
}

func TestReindexThenIndexedLogMatchesDurableAndFallsBack(t *testing.T) {
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

	durable := stripANSI(runRootStdout(t, "log", "--durable", "--session", sessionID.String(), "--verbose"))

	_ = runRootStdout(t, "reindex")
	indexed := stripANSI(runRootStdout(t, "log", "--index", "--session", sessionID.String(), "--verbose"))
	if indexed != durable {
		t.Fatalf("indexed log differs from durable log:\n--- durable ---\n%s\n--- indexed ---\n%s", durable, indexed)
	}

	if err := os.Remove(queryindex.PathsForMetadata(repo.MetadataDir).DBPath); err != nil {
		t.Fatalf("remove index database: %v", err)
	}
	fallback := stripANSI(runRootStdout(t, "log", "--index", "--session", sessionID.String(), "--verbose"))
	if fallback != durable {
		t.Fatalf("missing-index fallback differs from durable log:\n--- durable ---\n%s\n--- fallback ---\n%s", durable, fallback)
	}

	_ = runRootStdout(t, "reindex")
	reindexed := stripANSI(runRootStdout(t, "log", "--index", "--session", sessionID.String(), "--verbose"))
	if reindexed != durable {
		t.Fatalf("reindexed log differs from durable log:\n--- durable ---\n%s\n--- reindexed ---\n%s", durable, reindexed)
	}
}

func TestRenderCheckpointGraphShowsLimitTruncation(t *testing.T) {
	sessionID := sessionID(t, "demo")
	firstTurn, _ := primitives.NewTurnID(1)

	var out bytes.Buffer
	err := renderCheckpointGraph(&out, []graphSession{
		{
			ID:         sessionID,
			TotalTurns: 2,
			Turns: []graphTurn{
				{TurnID: firstTurn},
			},
		},
	}, graphRenderOptions{})
	if err != nil {
		t.Fatalf("renderCheckpointGraph: %v", err)
	}

	output := stripANSI(out.String())
	for _, want := range []string{
		"checkpoint graph: 1 session, showing 1 of 2 turns",
		"sessions: [demo](1/2)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("limited graph output missing %q:\n%s", want, output)
		}
	}
}

func TestRenderCheckpointGraphShowsOverlappingSessionLanes(t *testing.T) {
	sessionA := sessionID(t, "session-a")
	sessionB := sessionID(t, "session-b")
	turn1, _ := primitives.NewTurnID(1)
	turn2, _ := primitives.NewTurnID(2)
	base := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)

	var out bytes.Buffer
	err := renderCheckpointGraph(&out, []graphSession{
		{
			ID:         sessionA,
			TotalTurns: 2,
			Turns: []graphTurn{
				{TurnID: turn2, Post: checkpointInfo(sessionA, turn2, base.Add(10*time.Minute), "2")},
				{TurnID: turn1, Post: checkpointInfo(sessionA, turn1, base, "1")},
			},
		},
		{
			ID:         sessionB,
			TotalTurns: 1,
			Turns: []graphTurn{
				{TurnID: turn1, Post: checkpointInfo(sessionB, turn1, base.Add(5*time.Minute), "3")},
			},
		},
	}, graphRenderOptions{})
	if err != nil {
		t.Fatalf("renderCheckpointGraph: %v", err)
	}

	output := stripANSI(out.String())
	for _, want := range []string{
		"sessions: [session-a] [session-b]",
		"*   222222222222 - 12:10 [session-a] turn 2",
		"| * 333333333333 - 12:05 [session-b] turn 1",
		"*   111111111111 - 12:00 [session-a] turn 1",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("overlap graph output missing %q:\n%s", want, output)
		}
	}
}

func TestRenderCheckpointGraphShowsReadableProviderSessionLabels(t *testing.T) {
	sessionID := sessionID(t, "codex-sess_7f3a9c2d")
	turnID, _ := primitives.NewTurnID(1)
	at := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	session := graphSession{
		ID:         sessionID,
		TotalTurns: 1,
		Turns: []graphTurn{
			{
				TurnID: turnID,
				Post:   checkpointInfo(sessionID, turnID, at, "1"),
				Events: turnEventSummary{Adapter: "codex"},
			},
		},
	}

	var out bytes.Buffer
	if err := renderCheckpointGraph(&out, []graphSession{session}, graphRenderOptions{}); err != nil {
		t.Fatalf("renderCheckpointGraph: %v", err)
	}

	output := stripANSI(out.String())
	for _, want := range []string{
		"sessions: [codex 12:00]",
		"[codex 12:00] turn 1",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("provider graph output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, sessionID.String()) {
		t.Fatalf("default graph output should not show full generated session id:\n%s", output)
	}

	out.Reset()
	if err := renderCheckpointGraph(&out, []graphSession{session}, graphRenderOptions{Verbose: true}); err != nil {
		t.Fatalf("renderCheckpointGraph verbose: %v", err)
	}
	output = stripANSI(out.String())
	if !strings.Contains(output, "session: "+sessionID.String()) {
		t.Fatalf("verbose graph output missing full session id:\n%s", output)
	}
}

func TestGraphSessionLabelsDisambiguateDuplicateGeneratedNames(t *testing.T) {
	firstID := sessionID(t, "codex-sess_11111111")
	secondID := sessionID(t, "codex-sess_22222222")
	turnID, _ := primitives.NewTurnID(1)
	at := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)

	labels := buildGraphSessionLabels([]graphSession{
		{
			ID:         firstID,
			TotalTurns: 1,
			Turns: []graphTurn{
				{TurnID: turnID, Post: checkpointInfo(firstID, turnID, at, "1"), Events: turnEventSummary{Adapter: "codex"}},
			},
		},
		{
			ID:         secondID,
			TotalTurns: 1,
			Turns: []graphTurn{
				{TurnID: turnID, Post: checkpointInfo(secondID, turnID, at, "2"), Events: turnEventSummary{Adapter: "codex"}},
			},
		},
	})

	if labels[0] != "codex 12:00 11111111" {
		t.Fatalf("first label = %q, want codex 12:00 11111111", labels[0])
	}
	if labels[1] != "codex 12:00 22222222" {
		t.Fatalf("second label = %q, want codex 12:00 22222222", labels[1])
	}
}

func TestGraphSessionLabelsPreserveHumanTopicNames(t *testing.T) {
	sessionID := sessionID(t, "feature-12345678")

	labels := buildGraphSessionLabels([]graphSession{
		{ID: sessionID, TotalTurns: 1},
	})

	if labels[0] != "feature-12345678" {
		t.Fatalf("label = %q, want feature-12345678", labels[0])
	}
}

func TestTruncateTextKeepsValidUTF8(t *testing.T) {
	got := truncateText("hello 世界🙂 again", 11)
	if got != "hello 世界..." {
		t.Fatalf("truncateText = %q, want %q", got, "hello 世界...")
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("truncateText produced replacement rune: %q", got)
	}
}

func TestShouldPageRenderedOutputOnlyWhenGraphOverflows(t *testing.T) {
	data := []byte("one\ntwo\nthree\n")
	if shouldPageRenderedOutput(false, data, 3, 80) {
		t.Fatal("expected no pager when rendered output fits terminal height")
	}
	if !shouldPageRenderedOutput(false, data, 2, 80) {
		t.Fatal("expected pager when rendered output exceeds terminal height")
	}
	if shouldPageRenderedOutput(true, data, 2, 80) {
		t.Fatal("expected --no-pager override to disable pager")
	}
}

func TestRenderedLineCountIgnoresANSIAndAccountsForWrapping(t *testing.T) {
	data := []byte("short\n\x1b[38;5;120m12345678901\x1b[0m\n")
	if got := renderedLineCount(data, 10); got != 3 {
		t.Fatalf("renderedLineCount = %d, want 3", got)
	}
	if got := printableColumns("\x1b[38;5;120mgreen\x1b[0m"); got != 5 {
		t.Fatalf("printableColumns = %d, want 5", got)
	}
}

func checkpointInfo(sessionID primitives.SessionID, turnID primitives.TurnID, at time.Time, digit string) *checkpoint.CheckpointRefInfo {
	commit := primitives.CommitSHA(strings.Repeat(digit, 40))
	return &checkpoint.CheckpointRefInfo{
		SessionID: sessionID,
		TurnID:    turnID,
		Phase:     primitives.CheckpointPhasePost,
		HasPhase:  true,
		Commit:    commit,
		Time:      at,
	}
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(value string) string {
	return ansiPattern.ReplaceAllString(value, "")
}

func runRootStdout(t *testing.T, args ...string) string {
	t.Helper()
	cmd := NewRootCmd()
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("turnal %s: %v\nstdout=%s\nstderr=%s", strings.Join(args, " "), err, out.String(), stderr.String())
	}
	if stderr.Len() > 0 {
		t.Fatalf("turnal %s wrote stderr:\n%s", strings.Join(args, " "), stderr.String())
	}
	return out.String()
}

func writeFile(t *testing.T, root primitives.WorkspaceRoot, relPath, content string) {
	t.Helper()
	path := filepath.Join(root.String(), filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
}
