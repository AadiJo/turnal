package primitives

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionID(t *testing.T) {
	got, err := ParseSessionID(" 01HXABC ")
	if err != nil {
		t.Fatalf("ParseSessionID valid: %v", err)
	}
	if got.String() != "01hxabc" {
		t.Fatalf("expected canonical lowercase session id, got %q", got)
	}

	text, err := SessionID("DEMO").MarshalText()
	if err != nil {
		t.Fatalf("MarshalText uppercase session: %v", err)
	}
	if string(text) != "demo" {
		t.Fatalf("expected canonical marshal, got %q", text)
	}

	for _, input := range []string{"", "bad/id", "bad\\id", ".bad", "bad..id", "bad.lock", "bad@{id", "bad:id"} {
		if _, err := ParseSessionID(input); err == nil {
			t.Fatalf("ParseSessionID(%q) succeeded, want error", input)
		}
	}
}

func TestAdapterName(t *testing.T) {
	for _, input := range []string{"claude-code", "codex_cli", "opencode.v1"} {
		if _, err := ParseAdapterName(input); err != nil {
			t.Fatalf("ParseAdapterName(%q): %v", input, err)
		}
	}
	for _, input := range []string{"", "Claude", "-codex", "bad/name"} {
		if _, err := ParseAdapterName(input); err == nil {
			t.Fatalf("ParseAdapterName(%q) succeeded, want error", input)
		}
	}
}

func TestRunAndAttemptIDValidationAndSerialization(t *testing.T) {
	run, err := ParseRunID(" RUN_0123456789ABCDEF0123456789ABCDEF ")
	if err != nil || run.String() != "run_0123456789abcdef0123456789abcdef" {
		t.Fatalf("ParseRunID() = %q, %v", run, err)
	}
	attempt, err := ParseAttemptID("attempt_fedcba9876543210fedcba9876543210")
	if err != nil {
		t.Fatalf("ParseAttemptID: %v", err)
	}

	type record struct {
		Run     RunID     `json:"run_id"`
		Attempt AttemptID `json:"attempt_id"`
	}
	encoded, err := json.Marshal(record{Run: run, Attempt: attempt})
	if err != nil {
		t.Fatalf("marshal ids: %v", err)
	}
	var decoded record
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal ids: %v", err)
	}
	if decoded.Run != run || decoded.Attempt != attempt {
		t.Fatalf("round trip = %+v", decoded)
	}

	invalidIDs := []struct {
		value string
		parse func(string) error
	}{
		{"attempt_0123456789abcdef0123456789abcdef", func(value string) error { _, err := ParseRunID(value); return err }},
		{"run_0123", func(value string) error { _, err := ParseRunID(value); return err }},
		{"attempt_0123456789abcdef0123456789abcdeg", func(value string) error { _, err := ParseAttemptID(value); return err }},
	}
	for _, test := range invalidIDs {
		if err := test.parse(test.value); err == nil {
			t.Fatalf("expected %q to be rejected", test.value)
		}
	}
}

func TestTaskAndCaseIDValidationAndSerialization(t *testing.T) {
	taskID, err := ParseTaskID(" TASK_0123456789ABCDEF0123456789ABCDEF ")
	if err != nil || taskID.String() != "task_0123456789abcdef0123456789abcdef" {
		t.Fatalf("ParseTaskID() = %q, %v", taskID, err)
	}
	caseID, err := ParseCaseID("case_fedcba9876543210fedcba9876543210")
	if err != nil {
		t.Fatalf("ParseCaseID: %v", err)
	}

	type record struct {
		Task TaskID `json:"task_id"`
		Case CaseID `json:"case_id"`
	}
	encoded, err := json.Marshal(record{Task: taskID, Case: caseID})
	if err != nil {
		t.Fatalf("marshal ids: %v", err)
	}
	var decoded record
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal ids: %v", err)
	}
	if decoded.Task != taskID || decoded.Case != caseID {
		t.Fatalf("round trip = %+v", decoded)
	}

	generatedTask, err := NewTaskID()
	if err != nil {
		t.Fatalf("NewTaskID: %v", err)
	}
	if _, err := ParseTaskID(generatedTask.String()); err != nil {
		t.Fatalf("parse generated task id: %v", err)
	}
	generatedCase, err := NewCaseID()
	if err != nil {
		t.Fatalf("NewCaseID: %v", err)
	}
	if _, err := ParseCaseID(generatedCase.String()); err != nil {
		t.Fatalf("parse generated case id: %v", err)
	}

	invalidIDs := []struct {
		value string
		parse func(string) error
	}{
		{"case_0123456789abcdef0123456789abcdef", func(value string) error { _, err := ParseTaskID(value); return err }},
		{"task_0123", func(value string) error { _, err := ParseTaskID(value); return err }},
		{"case_0123456789abcdef0123456789abcdeg", func(value string) error { _, err := ParseCaseID(value); return err }},
	}
	for _, test := range invalidIDs {
		if err := test.parse(test.value); err == nil {
			t.Fatalf("expected %q to be rejected", test.value)
		}
	}
}

func TestTurnIDAndEventSeq(t *testing.T) {
	turn, err := ParseTurnID("000007")
	if err != nil {
		t.Fatalf("ParseTurnID: %v", err)
	}
	if turn.String() != "7" || turn.RefSegment() != "000007" {
		t.Fatalf("unexpected turn formatting: string=%q ref=%q", turn.String(), turn.RefSegment())
	}

	if _, err := NewTurnID(0); err == nil {
		t.Fatal("NewTurnID(0) succeeded, want error")
	}
	if _, err := NewEventSeq(0); err == nil {
		t.Fatal("NewEventSeq(0) succeeded, want error")
	}

	if _, err := json.Marshal(TurnID(0)); err == nil {
		t.Fatal("json.Marshal(TurnID(0)) succeeded, want error")
	}
	if _, err := json.Marshal(EventSeq(0)); err == nil {
		t.Fatal("json.Marshal(EventSeq(0)) succeeded, want error")
	}

	var decodedTurn TurnID
	if err := json.Unmarshal([]byte("3"), &decodedTurn); err != nil {
		t.Fatalf("Unmarshal TurnID: %v", err)
	}
	if decodedTurn != 3 {
		t.Fatalf("decoded turn = %v, want 3", decodedTurn)
	}
	if err := json.Unmarshal([]byte("0"), &decodedTurn); err == nil {
		t.Fatal("Unmarshal TurnID zero succeeded, want error")
	}

	var decodedSeq EventSeq
	if err := json.Unmarshal([]byte("null"), &decodedSeq); err == nil {
		t.Fatal("Unmarshal EventSeq null succeeded, want error")
	}
}

func TestEventHash(t *testing.T) {
	input := "sha256:" + strings.Repeat("A", 64)
	got, err := ParseEventHash(input)
	if err != nil {
		t.Fatalf("ParseEventHash: %v", err)
	}
	if got.String() != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("expected lowercase canonical hash, got %q", got)
	}

	text, err := EventHash(input).MarshalText()
	if err != nil {
		t.Fatalf("MarshalText EventHash: %v", err)
	}
	if string(text) != got.String() {
		t.Fatalf("marshal = %q, want %q", text, got)
	}

	for _, input := range []string{"", strings.Repeat("a", 64), "sha1:" + strings.Repeat("a", 40), "sha256:abc", "sha256:" + strings.Repeat("g", 64)} {
		if _, err := ParseEventHash(input); err == nil {
			t.Fatalf("ParseEventHash(%q) succeeded, want error", input)
		}
	}
}

func TestEventType(t *testing.T) {
	for _, eventType := range []EventType{
		EventTypeSessionStart,
		EventTypeTurnStart,
		EventTypePromptUser,
		EventTypeAgentIntent,
		EventTypeAssistantMessage,
		EventTypeToolCall,
		EventTypeToolResult,
		EventTypeTurnFinish,
		EventTypeCheckpoint,
		EventTypeRollback,
		EventTypeError,
		EventTypeAdapterRaw,
		EventTypeRunStart,
		EventTypeRunCaptureLink,
		EventTypeRunAttemptLink,
		EventTypeRunFinish,
		EventTypeTaskCreate,
		EventTypeTaskRevision,
		EventTypeCaseCreate,
		EventTypeCaseDelete,
		EventTypeCaseAttemptLink,
		EventTypeCaseAttemptResult,
		EventTypeCaseAttemptSelect,
		EventTypeCaseAttemptApply,
	} {
		if _, err := ParseEventType(eventType.String()); err != nil {
			t.Fatalf("ParseEventType(%q): %v", eventType, err)
		}
	}
	if _, err := ParseEventType("tool.finish"); err == nil {
		t.Fatal("ParseEventType unknown succeeded, want error")
	}
}

func TestTimestamp(t *testing.T) {
	ts, err := ParseTimestamp("2026-07-04T12:34:56.123456789-05:00")
	if err != nil {
		t.Fatalf("ParseTimestamp: %v", err)
	}
	if ts.Time.Location() != time.UTC {
		t.Fatalf("timestamp location = %v, want UTC", ts.Time.Location())
	}

	data, err := json.Marshal(ts)
	if err != nil {
		t.Fatalf("Marshal Timestamp: %v", err)
	}
	var decoded Timestamp
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal Timestamp: %v", err)
	}
	if !decoded.Time.Equal(ts.Time) {
		t.Fatalf("decoded timestamp = %s, want %s", decoded, ts)
	}

	if _, err := json.Marshal(Timestamp{}); err == nil {
		t.Fatal("Marshal zero Timestamp succeeded, want error")
	}
	if err := json.Unmarshal([]byte("null"), &decoded); err == nil {
		t.Fatal("Unmarshal null Timestamp succeeded, want error")
	}
}

func TestCommitSHA(t *testing.T) {
	sha1, err := ParseCommitSHA(strings.Repeat("A", 40))
	if err != nil {
		t.Fatalf("ParseCommitSHA sha1: %v", err)
	}
	if sha1.String() != strings.Repeat("a", 40) {
		t.Fatalf("expected lowercase sha1, got %q", sha1)
	}

	if _, err := ParseCommitSHA(strings.Repeat("b", 64)); err != nil {
		t.Fatalf("ParseCommitSHA sha256: %v", err)
	}
	for _, input := range []string{"", "abc123", strings.Repeat("z", 40)} {
		if _, err := ParseCommitSHA(input); err == nil {
			t.Fatalf("ParseCommitSHA(%q) succeeded, want error", input)
		}
	}
}

func TestCheckpointRef(t *testing.T) {
	sessionID, _ := ParseSessionID("Demo")
	turnID, _ := NewTurnID(7)

	ref, err := NewCheckpointRef(sessionID, turnID, CheckpointPhasePre)
	if err != nil {
		t.Fatalf("NewCheckpointRef: %v", err)
	}
	want := "refs/agent-vcs/checkpoints/demo/turn/000007/pre"
	if ref.String() != want {
		t.Fatalf("ref = %q, want %q", ref, want)
	}
	prefix, err := CheckpointSessionRefPrefix(sessionID)
	if err != nil {
		t.Fatalf("CheckpointSessionRefPrefix: %v", err)
	}
	if prefix != "refs/agent-vcs/checkpoints/demo/turn" {
		t.Fatalf("checkpoint session prefix = %q", prefix)
	}

	ref, err = NewCheckpointRef(SessionID("DIRECT"), turnID, CheckpointPhasePre)
	if err != nil {
		t.Fatalf("NewCheckpointRef direct typed session: %v", err)
	}
	if ref.String() != "refs/agent-vcs/checkpoints/direct/turn/000007/pre" {
		t.Fatalf("direct typed ref = %q", ref)
	}

	parsed, err := ParseCheckpointRef(want)
	if err != nil {
		t.Fatalf("ParseCheckpointRef: %v", err)
	}
	parts, err := parsed.Parts()
	if err != nil {
		t.Fatalf("CheckpointRef.Parts: %v", err)
	}
	if parts.SessionID != "demo" || parts.TurnID != 7 || parts.Phase != CheckpointPhasePre || !parts.HasPhase {
		t.Fatalf("unexpected checkpoint ref parts: %+v", parts)
	}

	noPhase := "refs/agent-vcs/checkpoints/demo/turn/000007"
	if _, err := ParseCheckpointRef(noPhase); err != nil {
		t.Fatalf("ParseCheckpointRef no phase: %v", err)
	}
	legacyManualSession := "refs/agent-vcs/checkpoints/manual/turn/000001"
	legacyManualRef, err := ParseCheckpointRef(legacyManualSession)
	if err != nil {
		t.Fatalf("ParseCheckpointRef legacy manual session: %v", err)
	}
	legacyManualParts, err := legacyManualRef.Parts()
	if err != nil {
		t.Fatalf("legacy manual session Parts: %v", err)
	}
	if legacyManualParts.SessionID != "manual" || legacyManualParts.TurnID != 1 || legacyManualParts.Manual || legacyManualParts.HasPhase {
		t.Fatalf("legacy manual session parts = %+v", legacyManualParts)
	}

	for _, input := range []string{
		"refs/agent-vcs/checkpoints/Demo/turn/000007/pre",
		"refs/agent-vcs/checkpoints/demo/turn/0007/pre",
		"refs/agent-vcs/checkpoints/demo/turn/0000007/pre",
		"refs/agent-vcs/checkpoints/demo/turn/000007/mid",
		"refs/heads/demo",
		"refs/agent-vcs/checkpoints/de..mo/turn/000007/pre",
	} {
		if _, err := ParseCheckpointRef(input); err == nil {
			t.Fatalf("ParseCheckpointRef(%q) succeeded, want error", input)
		}
	}

	if _, err := CheckpointRef("refs/agent-vcs/checkpoints/demo/turn/0000007/pre").Parts(); err == nil {
		t.Fatal("CheckpointRef.Parts accepted non-canonical ref")
	}
}

func TestManualCheckpointRef(t *testing.T) {
	worktreeID, err := ParseWorktreeID("wt_0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("ParseWorktreeID: %v", err)
	}
	checkpointID, err := ParseCheckpointID("chk_0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("ParseCheckpointID: %v", err)
	}
	ref, err := NewManualCheckpointRef(worktreeID, checkpointID)
	if err != nil {
		t.Fatalf("NewManualCheckpointRef: %v", err)
	}
	want := "refs/agent-vcs/checkpoints/manual/wt_0123456789abcdef0123456789abcdef/chk_0123456789abcdef0123456789abcdef"
	if ref.String() != want {
		t.Fatalf("manual ref = %q, want %q", ref, want)
	}
	parsed, err := ParseCheckpointRef(want)
	if err != nil {
		t.Fatalf("ParseCheckpointRef: %v", err)
	}
	parts, err := parsed.Parts()
	if err != nil {
		t.Fatalf("Parts: %v", err)
	}
	if !parts.Manual || parts.Canonical || parts.Scoped || parts.WorktreeID != worktreeID || parts.CheckpointID != checkpointID || parts.SessionID != "" || parts.TurnID != 0 || parts.HasPhase {
		t.Fatalf("manual ref parts = %+v", parts)
	}
	for _, invalid := range []string{
		"refs/agent-vcs/checkpoints/manual/demo/" + checkpointID.String(),
		"refs/agent-vcs/checkpoints/manual/" + worktreeID.String() + "/demo",
		want + "/extra",
	} {
		if _, err := ParseCheckpointRef(invalid); err == nil {
			t.Fatalf("ParseCheckpointRef(%q) succeeded", invalid)
		}
	}
}

func TestGitSyncRefAndRollbackMode(t *testing.T) {
	sessionID, _ := ParseSessionID("Demo")
	turnID, _ := NewTurnID(7)

	ref, err := NewGitSyncRef(sessionID, turnID, CheckpointPhasePre)
	if err != nil {
		t.Fatalf("NewGitSyncRef: %v", err)
	}
	want := "refs/agent-vcs/git-sync/demo/turn/000007/pre"
	if ref.String() != want {
		t.Fatalf("git-sync ref = %q, want %q", ref, want)
	}
	parsed, err := ParseGitSyncRef(want)
	if err != nil {
		t.Fatalf("ParseGitSyncRef: %v", err)
	}
	parts, err := parsed.Parts()
	if err != nil {
		t.Fatalf("GitSyncRef.Parts: %v", err)
	}
	if parts.SessionID != "demo" || parts.TurnID != 7 || parts.Phase != CheckpointPhasePre {
		t.Fatalf("unexpected git-sync ref parts: %+v", parts)
	}

	for _, input := range []string{
		"refs/agent-vcs/git-sync/Demo/turn/000007/pre",
		"refs/agent-vcs/git-sync/demo/turn/0007/pre",
		"refs/agent-vcs/git-sync/demo/turn/000007/mid",
		"refs/agent-vcs/checkpoints/demo/turn/000007/pre",
	} {
		if _, err := ParseGitSyncRef(input); err == nil {
			t.Fatalf("ParseGitSyncRef(%q) succeeded, want error", input)
		}
	}

	if mode, err := ParseRollbackMode("workspace-git"); err != nil || mode != RollbackModeWorkspaceGit {
		t.Fatalf("ParseRollbackMode workspace-git = %q, %v", mode, err)
	}
	if mode, err := ParseRollbackMode(""); err != nil || mode != RollbackModeCheckpoint {
		t.Fatalf("ParseRollbackMode empty = %q, %v", mode, err)
	}
	if _, err := ParseRollbackMode("hard-reset"); err == nil {
		t.Fatal("ParseRollbackMode accepted invalid mode")
	}
}

func TestWorkspaceRoot(t *testing.T) {
	root, err := ParseWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatalf("ParseWorkspaceRoot temp dir: %v", err)
	}
	if !filepath.IsAbs(root.String()) {
		t.Fatalf("workspace root is not absolute: %q", root)
	}

	for _, input := range []string{"", "relative/root", "bad\x00root"} {
		if _, err := ParseWorkspaceRoot(input); err == nil {
			t.Fatalf("ParseWorkspaceRoot(%q) succeeded, want error", input)
		}
	}
}

func TestRepoPath(t *testing.T) {
	valid := map[string]string{
		"src/login.tsx":     "src/login.tsx",
		"src//login.tsx":    "src/login.tsx",
		"src/../login.tsx":  "login.tsx",
		"dir with spaces/x": "dir with spaces/x",
	}
	for input, want := range valid {
		got, err := ParseRepoPath(input)
		if err != nil {
			t.Fatalf("ParseRepoPath(%q): %v", input, err)
		}
		if got.String() != want {
			t.Fatalf("ParseRepoPath(%q) = %q, want %q", input, got, want)
		}
	}

	for _, input := range []string{
		"",
		"/absolute",
		"../outside",
		"a/../../outside",
		".git/config",
		".GIT/config",
		"src/.turnal/state",
		"src/.TURNAL/state",
		`src\file.go`,
		`C:\repo\file.go`,
		"bad\x00path",
	} {
		if _, err := ParseRepoPath(input); err == nil {
			t.Fatalf("ParseRepoPath(%q) succeeded, want error", input)
		}
	}
}

func TestTargetRef(t *testing.T) {
	constructed, err := NewTargetRef(SessionID("DIRECT"), TurnID(7), CheckpointPhasePost)
	if err != nil {
		t.Fatalf("NewTargetRef direct typed session: %v", err)
	}
	if constructed.String() != "direct:turn:7:post" {
		t.Fatalf("constructed target = %q", constructed)
	}

	target, err := ParseTargetRef("Demo:turn:007:pre")
	if err != nil {
		t.Fatalf("ParseTargetRef: %v", err)
	}
	if target.String() != "demo:turn:7:pre" {
		t.Fatalf("target = %q, want canonical demo:turn:7:pre", target)
	}

	phase, ok := target.Phase()
	if !ok || phase != CheckpointPhasePre {
		t.Fatalf("phase = %q, %v; want pre,true", phase, ok)
	}

	ref, err := target.CheckpointRef()
	if err != nil {
		t.Fatalf("TargetRef.CheckpointRef: %v", err)
	}
	if ref.String() != "refs/agent-vcs/checkpoints/demo/turn/000007/pre" {
		t.Fatalf("checkpoint ref = %q", ref)
	}

	noPhase, err := ParseTargetRef("demo:turn:7")
	if err != nil {
		t.Fatalf("ParseTargetRef no phase: %v", err)
	}
	if _, ok := noPhase.Phase(); ok {
		t.Fatal("no-phase target reported a phase")
	}

	for _, input := range []string{"", "demo:checkpoint:7", "demo:turn:0", "demo:turn:7:mid", "bad/id:turn:7"} {
		if _, err := ParseTargetRef(input); err == nil {
			t.Fatalf("ParseTargetRef(%q) succeeded, want error", input)
		}
	}
}
