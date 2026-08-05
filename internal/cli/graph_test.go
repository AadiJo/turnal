package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
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
		Type:      primitives.EventTypeSessionStart,
		Adapter:   primitives.AdapterCodex,
		Payload:   json.RawMessage(`{"provider_session_id":"demo","model":"gpt-5.6-sol"}`),
	}); err != nil {
		t.Fatalf("append session event: %v", err)
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
		"events: 1 event; codex; gpt-5.6-sol",
		"Prompt: \"change app.txt\"",
		"| pre:",
		"| post:",
		"| file: app.txt +1 -1",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("graph output missing %q:\n%s", want, output)
		}
	}
}

func TestLogCommandAppliesLaneAndSessionLimits(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	sessionA := sessionID(t, "session-a")
	sessionB := sessionID(t, "session-b")
	turn1, _ := primitives.NewTurnID(1)
	turn2, _ := primitives.NewTurnID(2)

	base := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	create := func(sessionID primitives.SessionID, turnID primitives.TurnID, content string, at time.Time) {
		t.Helper()
		writeFile(t, root, "app.txt", content)
		created, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost)
		if err != nil {
			t.Fatalf("checkpoint %s turn %s: %v", sessionID, turnID, err)
		}
		rewriteCheckpointTime(t, repo, created, at)
	}
	create(sessionA, turn1, "a1\n", base)
	create(sessionB, turn1, "b1\n", base.Add(time.Minute))
	create(sessionA, turn2, "a2\n", base.Add(2*time.Minute))

	bounded := stripANSI(runRootStdout(t, "log", "--max-lanes", "1", "--no-pager"))
	if !strings.Contains(bounded, "checkpoint graph: 2 sessions, 3 turns, 1 lane, overflow used") {
		t.Fatalf("--max-lanes was not applied:\n%s", bounded)
	}
	unlimited := stripANSI(runRootStdout(t, "log", "--max-lanes", "0", "--no-pager"))
	if !strings.Contains(unlimited, "checkpoint graph: 2 sessions, 3 turns, 2 lanes") || strings.Contains(unlimited, "overflow used") {
		t.Fatalf("--max-lanes 0 was not applied:\n%s", unlimited)
	}

	limited := stripANSI(runRootStdout(t, "log", "--session-limit", "1", "--no-pager"))
	if !strings.Contains(limited, "checkpoint graph: showing 1 of 2 sessions, 2 of 3 turns, 1 lane") || strings.Contains(limited, "[session-b]") {
		t.Fatalf("--session-limit was not applied to graph output:\n%s", limited)
	}
	transcript := stripANSI(runRootStdout(t, "log", "--transcript", "--session-limit", "1", "--no-pager"))
	if !strings.Contains(transcript, "transcript log: showing 1 of 2 sessions, 2 of 3 turns") ||
		!strings.Contains(transcript, "Session: [session-a]") || strings.Contains(transcript, "Session: [session-b]") {
		t.Fatalf("--session-limit was not applied to transcript output:\n%s", transcript)
	}
}

// rewriteCheckpointTime makes the lane-layout integration test independent of
// the host clock while keeping the refs and tree produced by CreateCheckpoint.
func rewriteCheckpointTime(t *testing.T, repo *checkpoint.Repo, created checkpoint.Checkpoint, at time.Time) {
	t.Helper()
	treeCommand := exec.Command("git", "--git-dir", repo.GitDir, "rev-parse", created.Commit.String()+"^{tree}")
	treeOutput, err := treeCommand.Output()
	if err != nil {
		t.Fatalf("read checkpoint tree: %v", err)
	}
	date := at.Format(time.RFC3339)
	commitCommand := exec.Command("git", "--git-dir", repo.GitDir, "commit-tree", strings.TrimSpace(string(treeOutput)), "-m", "graph timing fixture")
	commitCommand.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=turnal-test", "GIT_AUTHOR_EMAIL=turnal-test@localhost", "GIT_AUTHOR_DATE="+date,
		"GIT_COMMITTER_NAME=turnal-test", "GIT_COMMITTER_EMAIL=turnal-test@localhost", "GIT_COMMITTER_DATE="+date,
	)
	commitOutput, err := commitCommand.Output()
	if err != nil {
		t.Fatalf("create checkpoint timing fixture: %v", err)
	}
	commit := strings.TrimSpace(string(commitOutput))
	for _, ref := range []primitives.CheckpointRef{created.Ref, created.CanonicalRef} {
		if output, err := exec.Command("git", "--git-dir", repo.GitDir, "update-ref", ref.String(), commit).CombinedOutput(); err != nil {
			t.Fatalf("update checkpoint timing ref %s: %v: %s", ref, err, output)
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

func TestLogCommandTranscriptShowsEventOnlyTurn(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	sessionID := sessionID(t, "demo")
	turnID, _ := primitives.NewTurnID(1)
	log := eventlog.Open(repo.MetadataDir)
	events := []eventlog.AppendInput{
		{
			SessionID: sessionID,
			TurnID:    &turnID,
			Type:      primitives.EventTypePromptUser,
			Adapter:   primitives.AdapterCodex,
			Time:      testTimestamp(t, time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)),
			Payload:   json.RawMessage(`{"text":"update app.txt and run tests"}`),
		},
		{
			SessionID: sessionID,
			TurnID:    &turnID,
			Type:      primitives.EventTypeToolCall,
			Adapter:   primitives.AdapterCodex,
			Time:      testTimestamp(t, time.Date(2026, 7, 6, 12, 1, 0, 0, time.UTC)),
			Payload:   json.RawMessage(`{"tool_name":"Write","input":{"file_path":"app.txt","content":"hidden"}}`),
		},
		{
			SessionID: sessionID,
			TurnID:    &turnID,
			Type:      primitives.EventTypeToolCall,
			Adapter:   primitives.AdapterCodex,
			Time:      testTimestamp(t, time.Date(2026, 7, 6, 12, 2, 0, 0, time.UTC)),
			Payload:   json.RawMessage(`{"tool_name":"Bash","input":{"command":"go test ./..."}}`),
		},
		{
			SessionID: sessionID,
			TurnID:    &turnID,
			Type:      primitives.EventTypeAssistantMessage,
			Adapter:   primitives.AdapterCodex,
			Time:      testTimestamp(t, time.Date(2026, 7, 6, 12, 3, 0, 0, time.UTC)),
			Payload:   json.RawMessage(`{"text":"I updated app.txt and ran the test suite."}`),
		},
	}
	for _, event := range events {
		if _, err := log.Append(event); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	output := stripANSI(runRootStdout(t, "log", "--transcript", "--session", "demo", "--no-pager"))
	for _, want := range []string{
		"transcript log: 1 session, 1 turn",
		"Session: [codex 12:03]",
		"* Write +1 - 12:03 - turn 1",
		"Human: update app.txt and run tests",
		"    ↓",
		"Agent: I updated app.txt and ran the test suite.",
		"├─ Write (file_path: app.txt)",
		"└─ Bash (command: go test ./...)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("transcript output missing %q:\n%s", want, output)
		}
	}
	if !strings.HasPrefix(output, "\n") || !strings.HasSuffix(output, "\n\n") {
		t.Fatalf("transcript output should have leading and trailing spacing:\n%q", output)
	}
}

func TestLogCommandTranscriptShowsFileDiffs(t *testing.T) {
	root, repo, sessionID, turnID := createTurnWithDiff(t)
	t.Chdir(root.String())

	log := eventlog.Open(repo.MetadataDir)
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypePromptUser,
		Adapter:   primitives.AdapterCodex,
		Time:      testTimestamp(t, time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)),
		Payload:   json.RawMessage(`{"text":"change app.txt"}`),
	}); err != nil {
		t.Fatalf("Append prompt: %v", err)
	}
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypeToolCall,
		Adapter:   primitives.AdapterCodex,
		Time:      testTimestamp(t, time.Date(2026, 7, 6, 12, 1, 0, 0, time.UTC)),
		Payload:   json.RawMessage(`{"tool_name":"Edit","input":{"file_path":"app.txt"}}`),
	}); err != nil {
		t.Fatalf("Append tool: %v", err)
	}
	if _, err := log.Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypeAssistantMessage,
		Adapter:   primitives.AdapterCodex,
		Time:      testTimestamp(t, time.Date(2026, 7, 6, 12, 2, 0, 0, time.UTC)),
		Payload:   json.RawMessage(`{"text":"changed app.txt"}`),
	}); err != nil {
		t.Fatalf("Append assistant: %v", err)
	}

	output := stripANSI(runRootStdout(t, "log", "--transcript", "--session", sessionID.String(), "--no-pager"))
	for _, want := range []string{
		"* ",
		"Edit",
		"turn 1",
		"app.txt +1 -1",
		"Human: change app.txt",
		"Agent: changed app.txt",
		"└─ Edit (file_path: app.txt)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("transcript diff output missing %q:\n%s", want, output)
		}
	}
}

func TestLogCommandShowsRollbackEventRow(t *testing.T) {
	root, _, sessionID, turnID := createTurnWithDiff(t)
	t.Chdir(root.String())

	writeFile(t, root, "app.txt", "working copy\n")
	_ = runRootStdout(t, "rollback", "--to", sessionID.String()+":turn:"+turnID.String()+":pre")

	output := stripANSI(runRootStdout(t, "log", "--durable", "--session", sessionID.String(), "--verbose"))
	for _, want := range []string{
		"checkpoint graph: 1 session, 1 turn, 1 lane, 1 rollback",
		"------------ reverted to [demo] turn 1 pre",
		"target: demo:turn:1:pre",
		"mode: checkpoint",
		"safety:",
		"ref: refs/agent-vcs/rollback-safety/demo/turn/000001/pre/",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("rollback log output missing %q:\n%s", want, output)
		}
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
		"checkpoint graph: 1 session, showing 1 of 2 turns, 1 lane",
		"[demo] turn 1",
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
		"checkpoint graph: 2 sessions, 3 turns, 2 lanes",
		"*   222222222222 - 12:10 [session-a] turn 2",
		"| * 333333333333 - 12:05 [session-b] turn 1",
		"*   111111111111 - 12:00 [session-a] turn 1",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("overlap graph output missing %q:\n%s", want, output)
		}
	}
}

func TestGraphLanePackingReusesSequentialSessionsAndExpandsResumedSessions(t *testing.T) {
	turn1, _ := primitives.NewTurnID(1)
	turn2, _ := primitives.NewTurnID(2)
	turn3, _ := primitives.NewTurnID(3)
	base := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	sessionA := sessionID(t, "session-a")
	sessionB := sessionID(t, "session-b")
	sessionC := sessionID(t, "session-c")

	sequential := []graphSession{
		{ID: sessionA, TotalTurns: 2, Turns: []graphTurn{
			{TurnID: turn2, Post: checkpointInfo(sessionA, turn2, base.Add(10*time.Minute), "2")},
			{TurnID: turn1, Post: checkpointInfo(sessionA, turn1, base, "1")},
		}},
		{ID: sessionC, TotalTurns: 2, Turns: []graphTurn{
			{TurnID: turn2, Post: checkpointInfo(sessionC, turn2, base.Add(30*time.Minute), "4")},
			{TurnID: turn1, Post: checkpointInfo(sessionC, turn1, base.Add(20*time.Minute), "3")},
		}},
	}
	sequentialRows := buildGraphTimelineRows(sequential)
	sequentialLayout := buildGraphLaneLayout(sequentialRows, sequential, 8)
	if sequentialLayout.LaneCount != 1 || sequentialLayout.SessionLanes[0] != sequentialLayout.SessionLanes[1] {
		t.Fatalf("sequential layout = %#v, want one reused lane", sequentialLayout)
	}

	resumed := []graphSession{
		{ID: sessionA, TotalTurns: 2, Turns: []graphTurn{
			{TurnID: turn2, Post: checkpointInfo(sessionA, turn2, base.Add(20*time.Minute), "6")},
			{TurnID: turn1, Post: checkpointInfo(sessionA, turn1, base.Add(10*time.Minute), "5")},
		}},
		{ID: sessionB, TotalTurns: 3, Turns: []graphTurn{
			{TurnID: turn3, Post: checkpointInfo(sessionB, turn3, base.Add(30*time.Minute), "9")},
			{TurnID: turn2, Post: checkpointInfo(sessionB, turn2, base.Add(5*time.Minute), "8")},
			{TurnID: turn1, Post: checkpointInfo(sessionB, turn1, base, "7")},
		}},
	}
	resumedLayout := buildGraphLaneLayout(buildGraphTimelineRows(resumed), resumed, 8)
	if resumedLayout.LaneCount != 2 || resumedLayout.SessionLanes[0] == resumedLayout.SessionLanes[1] {
		t.Fatalf("resumed layout = %#v, want overlapping spans in separate lanes", resumedLayout)
	}

	touching := []graphSession{
		{ID: sessionA, TotalTurns: 2, Turns: []graphTurn{
			{TurnID: turn2, Post: checkpointInfo(sessionA, turn2, base.Add(10*time.Minute), "2")},
			{TurnID: turn1, Post: checkpointInfo(sessionA, turn1, base, "1")},
		}},
		{ID: sessionB, TotalTurns: 2, Turns: []graphTurn{
			{TurnID: turn2, Post: checkpointInfo(sessionB, turn2, base.Add(20*time.Minute), "4")},
			{TurnID: turn1, Post: checkpointInfo(sessionB, turn1, base.Add(10*time.Minute), "3")},
		}},
	}
	touchingRows := buildGraphTimelineRows(touching)
	touchingLayout := buildGraphLaneLayout(touchingRows, touching, 8)
	if touchingLayout.LaneCount != 1 || touchingLayout.SessionLanes[0] != touchingLayout.SessionLanes[1] {
		t.Fatalf("equal-timestamp layout = %#v, want touching spans to reuse a lane", touchingLayout)
	}
	newerSpan := touchingLayout.Spans[1]
	olderSpan := touchingLayout.Spans[0]
	if newerSpan.Last >= olderSpan.First {
		t.Fatalf("touching row spans overlap: newer=%#v older=%#v", newerSpan, olderSpan)
	}
	if prefix := stripANSI(renderTimelineContinuationPrefix(newerSpan.Last, touchingRows[newerSpan.Last], touching, touchingLayout, graphRenderOptions{})); prefix != "  " {
		t.Fatalf("touching sessions were joined by a connector: %q", prefix)
	}
}

func TestRenderCheckpointGraphCapsOverflowLaneWithoutConnectingIt(t *testing.T) {
	turn1, _ := primitives.NewTurnID(1)
	turn2, _ := primitives.NewTurnID(2)
	base := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	var sessions []graphSession
	for _, suffix := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} {
		id := sessionID(t, "session-"+suffix)
		sessions = append(sessions, graphSession{ID: id, TotalTurns: 2, Turns: []graphTurn{
			{TurnID: turn2, Post: checkpointInfo(id, turn2, base.Add(time.Minute), suffix)},
			{TurnID: turn1, Post: checkpointInfo(id, turn1, base, suffix)},
		}})
	}
	rows := buildGraphTimelineRows(sessions)
	if layout := buildGraphLaneLayout(rows, sessions, 0); layout.LaneCount != 9 || layout.OverflowUsed {
		t.Fatalf("unlimited layout = %#v, want nine lanes without overflow", layout)
	}
	layout := buildGraphLaneLayout(rows, sessions, 8)
	if layout.LaneCount != 8 || !layout.OverflowUsed || layout.OverflowLane != 7 {
		t.Fatalf("bounded layout = %#v, want eighth lane as overflow", layout)
	}
	oneLane := buildGraphLaneLayout(rows, sessions, 1)
	if oneLane.LaneCount != 1 || !oneLane.OverflowUsed || oneLane.OverflowLane != 0 {
		t.Fatalf("one-column layout = %#v, want marker-only overflow", oneLane)
	}
	for sessionIndex := range sessions {
		if oneLane.displayLane(sessionIndex) != 0 {
			t.Fatalf("session %d display lane = %d, want overflow lane 0", sessionIndex, oneLane.displayLane(sessionIndex))
		}
	}

	var out bytes.Buffer
	if err := renderCheckpointGraph(&out, sessions, graphRenderOptions{MaxLanes: 8}); err != nil {
		t.Fatalf("renderCheckpointGraph: %v", err)
	}
	output := stripANSI(out.String())
	if !strings.Contains(output, "checkpoint graph: 9 sessions, 18 turns, 8 lanes, overflow used") {
		t.Fatalf("overflow summary missing:\n%s", output)
	}
	for rowIndex, row := range rows {
		prefix := stripANSI(renderLanePrefix(rowIndex, row.SessionIndex, sessions, layout, "* ", false, false, graphRenderOptions{}))
		overflowOffset := layout.OverflowLane * 2
		if len(prefix) < overflowOffset+2 {
			t.Fatalf("rendered prefix %q does not include overflow lane %d", prefix, layout.OverflowLane)
		}
		want := "  "
		if layout.displayLane(row.SessionIndex) == layout.OverflowLane {
			want = "* "
		}
		if got := prefix[overflowOffset : overflowOffset+2]; got != want {
			t.Fatalf("row %d overflow token = %q, want %q; prefix=%q", rowIndex, got, want, prefix)
		}
	}
}

func TestOverflowKeepsMostRecentlyActiveLanesVisible(t *testing.T) {
	turn1, _ := primitives.NewTurnID(1)
	turn2, _ := primitives.NewTurnID(2)
	base := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	var sessions []graphSession
	for index, suffix := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} {
		id := sessionID(t, "session-"+suffix)
		sessions = append(sessions, graphSession{ID: id, TotalTurns: 2, Turns: []graphTurn{
			{TurnID: turn2, Post: checkpointInfo(id, turn2, base.Add(time.Duration(index+1)*time.Minute), suffix)},
			{TurnID: turn1, Post: checkpointInfo(id, turn1, base, suffix)},
		}})
	}
	layout := buildGraphLaneLayout(buildGraphTimelineRows(sessions), sessions, 8)
	for sessionIndex := range sessions {
		inOverflow := layout.displayLane(sessionIndex) == layout.OverflowLane
		wantOverflow := sessionIndex < 2
		if inOverflow != wantOverflow {
			t.Fatalf("session %s overflow=%t, want %t; layout=%#v", sessions[sessionIndex].ID, inOverflow, wantOverflow, layout)
		}
	}
}

func TestLogGraphCompactionFlagDefaults(t *testing.T) {
	cmd := logCmd()
	maxLanes, err := cmd.Flags().GetInt("max-lanes")
	if err != nil || maxLanes != 8 {
		t.Fatalf("--max-lanes default = %d, %v; want 8", maxLanes, err)
	}
	sessionLimit, err := cmd.Flags().GetInt("session-limit")
	if err != nil || sessionLimit != 0 {
		t.Fatalf("--session-limit default = %d, %v; want 0", sessionLimit, err)
	}
}

func TestSessionLimitKeepsMostRecentlyActiveSessionsAndReportsCompaction(t *testing.T) {
	turnID, _ := primitives.NewTurnID(1)
	base := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	sessionA := sessionID(t, "session-a")
	sessionB := sessionID(t, "session-b")
	sessionC := sessionID(t, "session-c")
	sessions := []graphSession{
		{ID: sessionA, TotalTurns: 1, Turns: []graphTurn{{TurnID: turnID, Post: checkpointInfo(sessionA, turnID, base, "1")}}},
		{ID: sessionB, TotalTurns: 1, Turns: []graphTurn{{TurnID: turnID, Post: checkpointInfo(sessionB, turnID, base.Add(2*time.Minute), "2")}}},
		{ID: sessionC, TotalTurns: 1, Turns: []graphTurn{{TurnID: turnID, Post: checkpointInfo(sessionC, turnID, base.Add(time.Minute), "3")}}},
	}
	limited := limitGraphSessions(sessions, 2)
	if len(limited) != 2 || limited[0].ID != sessionB || limited[1].ID != sessionC {
		t.Fatalf("limited sessions = %#v, want session-b and session-c", limited)
	}

	var out bytes.Buffer
	if err := renderCheckpointGraph(&out, limited, graphRenderOptions{MaxLanes: 8, Totals: &graphHistoryTotals{Sessions: 3, Turns: 3}}); err != nil {
		t.Fatalf("renderCheckpointGraph: %v", err)
	}
	output := stripANSI(out.String())
	if !strings.Contains(output, "checkpoint graph: showing 2 of 3 sessions, 2 of 3 turns, 1 lane") {
		t.Fatalf("session compaction summary missing:\n%s", output)
	}
	if strings.Contains(output, "[session-a]") {
		t.Fatalf("older session survived session limit:\n%s", output)
	}
}

func TestSessionColorDependsOnIDRatherThanLane(t *testing.T) {
	sessionA := sessionID(t, "session-a")
	first := styleSession("* ", sessionA, graphRenderOptions{})
	second := styleSession("[session-a]", sessionA, graphRenderOptions{})
	firstColor := strings.TrimSuffix(first, "* "+ansiReset)
	secondColor := strings.TrimSuffix(second, "[session-a]"+ansiReset)
	if firstColor != secondColor {
		t.Fatalf("session color changed between marker and label: %q != %q", firstColor, secondColor)
	}
	colors := make(map[string]struct{})
	for _, name := range []string{"session-a", "session-b", "session-c", "session-d", "session-e", "session-f", "session-g", "session-h", "session-i"} {
		id := sessionID(t, name)
		styled := styleSession("* ", id, graphRenderOptions{})
		color := strings.TrimSuffix(styled, "* "+ansiReset)
		if _, exists := colors[color]; exists {
			t.Fatalf("displayed sessions %s and an earlier id received the same color %q", id, color)
		}
		colors[color] = struct{}{}
	}
}

func TestWorkspaceEventsUseOneSeparateMarker(t *testing.T) {
	commit := primitives.CommitSHA(strings.Repeat("a", 40))
	var out bytes.Buffer
	renderSaveRow(&out, graphSave{Info: checkpoint.CheckpointRefInfo{Commit: commit}}, graphRenderOptions{})
	if got := stripANSI(out.String()); !strings.HasPrefix(got, "+ ------------ saved ") || strings.HasPrefix(got, "+ + ") {
		t.Fatalf("save marker = %q, want one separate event marker", got)
	}

	out.Reset()
	renderRollbackRow(&out, 0, -1, graphRollback{Manual: true, CommitSHA: commit}, nil, nil, graphLaneLayout{}, graphRenderOptions{})
	if got := stripANSI(out.String()); !strings.HasPrefix(got, "! ------------ reverted to saved ") || strings.HasPrefix(got, "! ! ") {
		t.Fatalf("workspace rollback marker = %q, want one separate event marker", got)
	}
}

func TestRollbackUsesAttachedLaneWhenTargetSessionIsHidden(t *testing.T) {
	visibleID := sessionID(t, "visible")
	hiddenID := sessionID(t, "hidden")
	turnID, _ := primitives.NewTurnID(1)
	target, err := primitives.NewTargetRef(hiddenID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("target ref: %v", err)
	}
	base := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	sessions := []graphSession{{
		ID:         visibleID,
		TotalTurns: 1,
		Turns:      []graphTurn{{TurnID: turnID, Post: checkpointInfo(visibleID, turnID, base, "1")}},
		Rollbacks: []graphRollback{{
			Time:      base.Add(time.Minute),
			Seq:       mustEventSeq(t, 1),
			Target:    target,
			CommitSHA: primitives.CommitSHA(strings.Repeat("2", 40)),
		}},
	}}

	var out bytes.Buffer
	if err := renderCheckpointGraph(&out, sessions, graphRenderOptions{MaxLanes: 8}); err != nil {
		t.Fatalf("renderCheckpointGraph: %v", err)
	}
	output := stripANSI(out.String())
	if !strings.Contains(output, "! ------------ reverted to [hidden] turn 1 pre 222222222222") {
		t.Fatalf("rollback targeting hidden session lost its marker:\n%s", output)
	}
}

func TestRenderCheckpointGraphShowsRollbackRowsAcrossSessions(t *testing.T) {
	sessionA := sessionID(t, "codex-sess_aaaaaaaa")
	sessionB := sessionID(t, "session-b")
	turn1, _ := primitives.NewTurnID(1)
	turn2, _ := primitives.NewTurnID(2)
	turn3, _ := primitives.NewTurnID(3)
	base := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	target, err := primitives.NewTargetRef(sessionA, turn1, primitives.CheckpointPhasePre)
	if err != nil {
		t.Fatalf("target ref: %v", err)
	}

	var out bytes.Buffer
	err = renderCheckpointGraph(&out, []graphSession{
		{
			ID:         sessionA,
			TotalTurns: 3,
			Turns: []graphTurn{
				{TurnID: turn3, Post: checkpointInfo(sessionA, turn3, base.Add(30*time.Minute), "3")},
				{TurnID: turn2, Post: checkpointInfo(sessionA, turn2, base.Add(10*time.Minute), "2")},
				{TurnID: turn1, Post: checkpointInfo(sessionA, turn1, base, "1"), Events: turnEventSummary{Adapter: "codex"}},
			},
			Rollbacks: []graphRollback{
				{
					Time:            base.Add(20 * time.Minute),
					Seq:             mustEventSeq(t, 4),
					Target:          target,
					CheckpointRef:   "refs/agent-vcs/checkpoints/codex-sess_aaaaaaaa/turn/000001/pre",
					CommitSHA:       primitives.CommitSHA(strings.Repeat("9", 40)),
					SafetyRef:       "refs/agent-vcs/rollback-safety/codex-sess_aaaaaaaa/turn/000001/pre/example",
					SafetyCommitSHA: strings.Repeat("8", 40),
					Mode:            primitives.RollbackModeCheckpoint.String(),
					SourceID:        "turnal:rollback:checkpoint:codex-sess_aaaaaaaa:turn:1:pre",
				},
			},
		},
		{
			ID:         sessionB,
			TotalTurns: 2,
			Turns: []graphTurn{
				{TurnID: turn2, Post: checkpointInfo(sessionB, turn2, base.Add(25*time.Minute), "5")},
				{TurnID: turn1, Post: checkpointInfo(sessionB, turn1, base.Add(5*time.Minute), "4")},
			},
		},
	}, graphRenderOptions{Verbose: true})
	if err != nil {
		t.Fatalf("renderCheckpointGraph: %v", err)
	}

	output := stripANSI(out.String())
	for _, want := range []string{
		"\ncheckpoint graph: 2 sessions, 5 turns, 2 lanes, 1 rollback",
		"sessions: [codex 12:00] [session-b]",
		"*   333333333333 - 12:30 [codex 12:00] turn 3",
		"| * 555555555555 - 12:25 [session-b] turn 2",
		"! | ------------ reverted to [codex 12:00] turn 1 pre 999999999999",
		"| | rollback: 2026-07-06 12:20:00 UTC",
		"| | target: codex-sess_aaaaaaaa:turn:1:pre",
		"| | safety:",
		"| |   id:  8888888888888888888888888888888888888888",
		"| |   ref: refs/agent-vcs/rollback-safety/codex-sess_aaaaaaaa/turn/000001/pre/example",
		"* | 222222222222 - 12:10 [codex 12:00] turn 2",
		"| * 444444444444 - 12:05 [session-b] turn 1",
		"*   111111111111 - 12:00 [codex 12:00] turn 1",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("rollback graph output missing %q:\n%s", want, output)
		}
	}
	if !strings.HasPrefix(output, "\n") || !strings.HasSuffix(output, "\n\n") {
		t.Fatalf("graph output should have leading and trailing spacing:\n%q", output)
	}
}

func TestRenderCheckpointGraphOmitsPostRollbackPhase(t *testing.T) {
	sessionID := sessionID(t, "codex")
	turnID, _ := primitives.NewTurnID(1)
	target, err := primitives.NewTargetRef(sessionID, turnID, primitives.CheckpointPhasePost)
	if err != nil {
		t.Fatalf("target ref: %v", err)
	}

	var out bytes.Buffer
	err = renderCheckpointGraph(&out, []graphSession{
		{
			ID:         sessionID,
			TotalTurns: 1,
			Turns: []graphTurn{
				{TurnID: turnID, Post: checkpointInfo(sessionID, turnID, time.Now().UTC(), "1")},
			},
			Rollbacks: []graphRollback{
				{
					Time:      time.Now().UTC().Add(time.Minute),
					Seq:       mustEventSeq(t, 2),
					Target:    target,
					CommitSHA: primitives.CommitSHA(strings.Repeat("2", 40)),
				},
			},
		},
	}, graphRenderOptions{})
	if err != nil {
		t.Fatalf("renderCheckpointGraph: %v", err)
	}

	output := stripANSI(out.String())
	if !strings.Contains(output, "------------ reverted to [codex] turn 1 222222222222") {
		t.Fatalf("post rollback row should omit phase:\n%s", output)
	}
	if strings.Contains(output, "turn 1 post") {
		t.Fatalf("post rollback row should not include post phase:\n%s", output)
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
	if strings.Contains(output, "sessions:") {
		t.Fatalf("default graph output should hide the session legend:\n%s", output)
	}
	for _, want := range []string{
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

func TestStripANSIBytesRemovesColorSequences(t *testing.T) {
	got := string(stripANSIBytes([]byte("plain \x1b[38;5;111mblue\x1b[0m text")))
	if got != "plain blue text" {
		t.Fatalf("stripANSIBytes = %q", got)
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

func mustEventSeq(t *testing.T, value uint64) primitives.EventSeq {
	t.Helper()
	seq, err := primitives.NewEventSeq(value)
	if err != nil {
		t.Fatalf("event seq: %v", err)
	}
	return seq
}

func testTimestamp(t *testing.T, value time.Time) primitives.Timestamp {
	t.Helper()
	timestamp, err := primitives.NewTimestamp(value)
	if err != nil {
		t.Fatalf("NewTimestamp: %v", err)
	}
	return timestamp
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
