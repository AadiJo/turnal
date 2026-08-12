package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/AadiJo/turnal/internal/blame"
	"github.com/AadiJo/turnal/internal/checkpoint"
	eventlog "github.com/AadiJo/turnal/internal/events"
	queryindex "github.com/AadiJo/turnal/internal/index"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/provenance"
	"github.com/AadiJo/turnal/internal/runs"
	"github.com/AadiJo/turnal/internal/turns"
	adaptersdk "github.com/AadiJo/turnal/sdk/adapter"
)

func TestClaudeAndCodexHooksAttributeEditsToStatedIntent(t *testing.T) {
	requireGit(t)
	for _, adapter := range []primitives.AdapterName{primitives.AdapterClaudeCode, primitives.AdapterCodex} {
		t.Run(adapter.String(), func(t *testing.T) {
			root := workspaceRoot(t)
			repo, err := checkpoint.Init(root)
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			t.Chdir(root.String())
			writeFile(t, root, "app.txt", "before\n")

			sessionText := adapter.String() + "-intent"
			send := func(eventName string, fields map[string]any) {
				fields["cwd"] = root.String()
				fields["session_id"] = sessionText
				hookName := eventName
				if adapter == primitives.AdapterCodex {
					fields["hook_event_name"] = eventName
					hookName = "CodexHook"
				}
				handlePayload(t, adapter, hookName, fields)
			}

			send("UserPromptSubmit", map[string]any{"prompt": "fix retry reset", "turn_id": "provider-turn-1"})
			sessionID := sessionID(t, sessionText)
			if _, err := provenance.Record(repo, provenance.RecordInput{
				SessionID: sessionID,
				TurnID:    1,
				Problem:   "retry delay was not reset after success",
				Scope:     []string{"app.txt"},
				Evidence:  []string{"test:TestRetryReset"},
			}); err != nil {
				t.Fatalf("Record intent: %v", err)
			}

			tool := map[string]any{
				"turn_id":     "provider-turn-1",
				"tool_name":   "Write",
				"tool_use_id": "tool-1",
				"tool_input":  map[string]any{"file_path": "app.txt"},
			}
			send("PreToolUse", tool)
			writeFile(t, root, "app.txt", "after\n")
			tool["tool_response"] = map[string]any{"ok": true}
			send("PostToolUse", tool)
			send("Stop", map[string]any{"turn_id": "provider-turn-1", "last_assistant_message": "done"})

			path, _ := primitives.ParseRepoPath("app.txt")
			result, err := blame.New(repo).Compute(blame.Query{Path: path, Line: 1})
			if err != nil {
				t.Fatalf("Blame: %v", err)
			}
			origin := result.Entries[0].Origin
			if origin.Intent == nil || origin.Intent.Problem != "retry delay was not reset after success" || origin.Intent.Status != provenance.IntentStatusCaptured || origin.Intent.Confidence != provenance.IntentConfidenceHigh {
				t.Fatalf("intent attribution = %#v", origin.Intent)
			}
			if origin.ActionTool != "Write" || origin.Prompt != "fix retry reset" || origin.Adapter != adapter.String() {
				t.Fatalf("origin = %#v", origin)
			}
			refs, err := repo.ListPrivateRefs("refs/agent-vcs/actions/" + sessionID.String())
			if err != nil || len(refs) != 2 {
				t.Fatalf("action refs = %#v, err=%v", refs, err)
			}
		})
	}
}

func TestRetriedActionHooksReuseDurableSnapshots(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, "app.txt", "before\n")
	sessionID := sessionID(t, "codex-action-retry")

	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "UserPromptSubmit", "prompt": "fix app",
	})
	if _, err := provenance.Record(repo, provenance.RecordInput{
		SessionID: sessionID,
		TurnID:    1,
		Problem:   "app contains the stale value",
		Scope:     []string{"app.txt"},
	}); err != nil {
		t.Fatalf("Record intent: %v", err)
	}
	tool := map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "PreToolUse",
		"tool_name": "apply_patch", "tool_use_id": "tool-retried",
	}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", tool)
	writeFile(t, root, "app.txt", "after\n")
	tool["hook_event_name"] = "PostToolUse"
	tool["tool_response"] = map[string]any{"ok": true}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", tool)

	eventsBeforeRetry := readEvents(t, repo, sessionID)
	var pre, post provenance.ActionSnapshot
	for _, event := range eventsBeforeRetry {
		switch event.Type {
		case primitives.EventTypeToolCall:
			var payload toolCallPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.PreSnapshot == nil {
				t.Fatalf("decode pre snapshot: payload=%s err=%v", event.Payload, err)
			}
			pre = *payload.PreSnapshot
		case primitives.EventTypeToolResult:
			var payload toolResultPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.PostSnapshot == nil {
				t.Fatalf("decode post snapshot: payload=%s err=%v", event.Payload, err)
			}
			post = *payload.PostSnapshot
		}
	}
	if pre.Ref == "" || post.Ref == "" {
		t.Fatalf("action snapshots = pre=%#v post=%#v", pre, post)
	}

	// Providers can retry a delivered hook with a differently serialized raw
	// payload. The action identity, and therefore its original snapshots, stay
	// the same.
	tool["hook_event_name"] = "PreToolUse"
	tool["retry_metadata"] = map[string]any{"attempt": 2}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", tool)
	tool["hook_event_name"] = "PostToolUse"
	tool["retry_metadata"] = map[string]any{"attempt": 3}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", tool)
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "Stop", "last_assistant_message": "done",
	})
	tool["hook_event_name"] = "PreToolUse"
	tool["retry_metadata"] = map[string]any{"attempt": 4, "delivered_after_stop": true}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", tool)
	tool["hook_event_name"] = "PostToolUse"
	tool["retry_metadata"] = map[string]any{"attempt": 5, "delivered_after_stop": true}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", tool)

	eventsAfterRetry := readEvents(t, repo, sessionID)
	if got := countEvents(eventsAfterRetry, primitives.EventTypeToolCall); got != 1 {
		t.Fatalf("tool calls after retry = %d, want 1", got)
	}
	if got := countEvents(eventsAfterRetry, primitives.EventTypeToolResult); got != 1 {
		t.Fatalf("tool results after retry = %d, want 1", got)
	}
	if active, ok, err := turns.NewManager(repo).Active(sessionID); err != nil || ok {
		t.Fatalf("active turn after stopped-turn retry = %#v ok=%v err=%v", active, ok, err)
	}
	if err := repo.RunHiddenGitGC(); err != nil {
		t.Fatalf("RunHiddenGitGC: %v", err)
	}
	for _, snapshot := range []provenance.ActionSnapshot{pre, post} {
		if got, err := repo.RefCommit(snapshot.Ref); err != nil || got != snapshot.Commit {
			t.Fatalf("snapshot %s after gc = %s, %v; want %s", snapshot.Ref, got, err, snapshot.Commit)
		}
	}

	path, _ := primitives.ParseRepoPath("app.txt")
	result, err := blame.New(repo).Compute(blame.Query{Path: path, Line: 1})
	if err != nil {
		t.Fatalf("Blame after gc: %v", err)
	}
	intent := result.Entries[0].Origin.Intent
	if intent == nil || intent.Problem != "app contains the stale value" {
		t.Fatalf("intent after retry and gc = %#v", intent)
	}
}

func TestRedactedIntentNeverClaimsHighConfidence(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, ".turnal/config.toml", "version = 1\n\n[secrets]\nstore_prompts = false\n")
	writeFile(t, root, "app.txt", "before\n")
	sessionID := sessionID(t, "codex-redacted-intent")
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "UserPromptSubmit", "prompt": "private request",
	})
	if _, err := provenance.Record(repo, provenance.RecordInput{
		SessionID: sessionID,
		TurnID:    1,
		Problem:   "private problem",
		Scope:     []string{"app.txt"},
	}); err != nil {
		t.Fatalf("Record intent: %v", err)
	}
	tool := map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "PreToolUse",
		"tool_name": "apply_patch", "tool_use_id": "redacted-tool",
	}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", tool)
	writeFile(t, root, "app.txt", "after\n")
	tool["hook_event_name"] = "PostToolUse"
	tool["tool_response"] = map[string]any{"ok": true}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", tool)
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "Stop", "last_assistant_message": "private response",
	})

	path, _ := primitives.ParseRepoPath("app.txt")
	result, err := blame.New(repo).Compute(blame.Query{Path: path, Line: 1})
	if err != nil {
		t.Fatalf("Blame: %v", err)
	}
	intent := result.Entries[0].Origin.Intent
	if intent == nil || intent.Status != provenance.IntentStatusRedacted || intent.Confidence != provenance.IntentConfidenceLow || !intent.Redacted {
		t.Fatalf("redacted blame intent = %#v", intent)
	}
	if intent.Problem != primitives.SecretsRedactionText || len(intent.Scope) != 0 || len(intent.Evidence) != 0 {
		t.Fatalf("redacted blame payload = %#v", intent)
	}
}

func TestConcurrentSessionsDoNotBorrowIntent(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, "app.txt", "one\ntwo\n")
	sessionA := sessionID(t, "codex-concurrent-a")
	sessionB := sessionID(t, "codex-concurrent-b")

	for _, session := range []primitives.SessionID{sessionA, sessionB} {
		handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
			"cwd": root.String(), "session_id": session.String(), "hook_event_name": "UserPromptSubmit", "prompt": "change app",
		})
		if _, err := provenance.Record(repo, provenance.RecordInput{
			SessionID: session,
			TurnID:    1,
			Problem:   "problem for " + session.String(),
			Scope:     []string{"app.txt"},
		}); err != nil {
			t.Fatalf("Record intent for %s: %v", session, err)
		}
		handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
			"cwd": root.String(), "session_id": session.String(), "hook_event_name": "PreToolUse",
			"tool_name": "apply_patch", "tool_use_id": "tool-" + session.String(),
		})
	}

	writeFile(t, root, "app.txt", "ONE\ntwo\n")
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionA.String(), "hook_event_name": "PostToolUse",
		"tool_name": "apply_patch", "tool_use_id": "tool-" + sessionA.String(), "tool_response": map[string]any{"ok": true},
	})
	writeFile(t, root, "app.txt", "ONE\nTWO\n")
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionB.String(), "hook_event_name": "PostToolUse",
		"tool_name": "apply_patch", "tool_use_id": "tool-" + sessionB.String(), "tool_response": map[string]any{"ok": true},
	})
	for _, session := range []primitives.SessionID{sessionA, sessionB} {
		handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
			"cwd": root.String(), "session_id": session.String(), "hook_event_name": "Stop", "last_assistant_message": "done",
		})
	}

	path, _ := primitives.ParseRepoPath("app.txt")
	for name, query := range map[string]blame.Query{
		"global":           {Path: path},
		"session-scoped-a": {Path: path, SessionID: sessionA},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := blame.New(repo).Compute(query)
			if err != nil {
				t.Fatalf("Blame: %v", err)
			}
			if len(result.Entries) != 2 {
				t.Fatalf("entries = %#v", result.Entries)
			}
			for _, entry := range result.Entries {
				if entry.Origin.Kind != "concurrent" || entry.Origin.Intent != nil || entry.Origin.SessionID != "" || entry.Origin.ActionTool != "" {
					t.Fatalf("concurrent entry borrowed attribution: %#v", entry)
				}
			}
			if len(result.Warnings) == 0 || !strings.Contains(strings.Join(result.Warnings, "\n"), sessionB.String()) {
				t.Fatalf("warnings = %#v", result.Warnings)
			}
		})
	}
}

func TestSessionScopedBlameIncludesChangesBeforeOverlappingTurnPreSnapshot(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, "app.txt", "one\ntwo\n")
	sessionA := sessionID(t, "codex-scoped-overlap-a")
	sessionB := sessionID(t, "codex-scoped-overlap-b")

	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionA.String(), "hook_event_name": "UserPromptSubmit", "prompt": "change first line",
	})
	actionA := map[string]any{
		"cwd": root.String(), "session_id": sessionA.String(), "hook_event_name": "PreToolUse",
		"tool_name": "apply_patch", "tool_use_id": "edit-a",
	}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", actionA)
	writeFile(t, root, "app.txt", "ONE\ntwo\n")

	// B's pre snapshot already contains A's uncompleted edit.
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionB.String(), "hook_event_name": "UserPromptSubmit", "prompt": "change second line",
	})
	actionB := map[string]any{
		"cwd": root.String(), "session_id": sessionB.String(), "hook_event_name": "PreToolUse",
		"tool_name": "apply_patch", "tool_use_id": "edit-b",
	}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", actionB)
	writeFile(t, root, "app.txt", "ONE\nTWO\n")
	actionB["hook_event_name"] = "PostToolUse"
	actionB["tool_response"] = map[string]any{"ok": true}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", actionB)
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionB.String(), "hook_event_name": "Stop", "last_assistant_message": "done",
	})
	actionA["hook_event_name"] = "PostToolUse"
	actionA["tool_response"] = map[string]any{"ok": true}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", actionA)
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionA.String(), "hook_event_name": "Stop", "last_assistant_message": "done",
	})

	path, _ := primitives.ParseRepoPath("app.txt")
	result, err := blame.New(repo).Compute(blame.Query{Path: path, SessionID: sessionB})
	if err != nil {
		t.Fatalf("Blame: %v", err)
	}
	for _, entry := range result.Entries {
		if entry.Origin.Kind != "concurrent" || entry.Origin.Intent != nil {
			t.Fatalf("scoped overlapping entry = %#v, want concurrent", entry)
		}
	}
	warnings := strings.Join(result.Warnings, "\n")
	if !strings.Contains(warnings, sessionA.String()) || strings.Contains(warnings, "sessions "+sessionB.String()) {
		t.Fatalf("warnings = %#v, want only the other participant", result.Warnings)
	}
}

func TestActiveConcurrentSessionMakesCompletedIntentAmbiguous(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, "app.txt", "one\ntwo\n")
	activeSession := sessionID(t, "codex-active-neighbor")
	completedSession := sessionID(t, "codex-completed-neighbor")

	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": activeSession.String(), "hook_event_name": "UserPromptSubmit", "prompt": "change second line",
	})
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": activeSession.String(), "hook_event_name": "PreToolUse",
		"tool_name": "apply_patch", "tool_use_id": "active-tool",
	})
	// This edit lands before the completed turn's pre snapshot and must not be
	// mistaken for that turn's baseline in session-scoped blame.
	writeFile(t, root, "app.txt", "one\nTWO\n")
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": completedSession.String(), "hook_event_name": "UserPromptSubmit", "prompt": "change first line",
	})
	if _, err := provenance.Record(repo, provenance.RecordInput{
		SessionID: completedSession,
		TurnID:    1,
		Problem:   "first line is stale",
		Scope:     []string{"app.txt"},
	}); err != nil {
		t.Fatalf("Record intent: %v", err)
	}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": completedSession.String(), "hook_event_name": "PreToolUse",
		"tool_name": "apply_patch", "tool_use_id": "completed-tool",
	})

	// The active session never delivers its result. The completed session's
	// post snapshot therefore observes both agents.
	writeFile(t, root, "app.txt", "ONE\nTWO\n")
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": completedSession.String(), "hook_event_name": "PostToolUse",
		"tool_name": "apply_patch", "tool_use_id": "completed-tool", "tool_response": map[string]any{"ok": true},
	})
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": completedSession.String(), "hook_event_name": "Stop", "last_assistant_message": "done",
	})

	path, _ := primitives.ParseRepoPath("app.txt")
	result, err := blame.New(repo).Compute(blame.Query{Path: path, SessionID: completedSession})
	if err != nil {
		t.Fatalf("Blame: %v", err)
	}
	for _, entry := range result.Entries {
		if entry.Origin.Kind != "concurrent" || entry.Origin.Intent != nil {
			t.Fatalf("entry borrowed completed-session intent: %#v", entry)
		}
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), activeSession.String()) {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
}

func TestIntentScopeFollowsActionRename(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, "old.txt", "one\ntwo\nthree\nfour\nfive\n")
	sessionID := sessionID(t, "codex-rename-scope")
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "UserPromptSubmit", "prompt": "rename and fix the file",
	})
	if _, err := provenance.Record(repo, provenance.RecordInput{
		SessionID: sessionID,
		TurnID:    1,
		Problem:   "the old file has a stale second line",
		Scope:     []string{"old.txt"},
	}); err != nil {
		t.Fatalf("Record intent: %v", err)
	}
	tool := map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "PreToolUse",
		"tool_name": "apply_patch", "tool_use_id": "rename-tool",
	}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", tool)
	if err := os.Rename(filepath.Join(root.String(), "old.txt"), filepath.Join(root.String(), "new.txt")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	writeFile(t, root, "new.txt", "one\nTWO\nthree\nfour\nfive\n")
	tool["hook_event_name"] = "PostToolUse"
	tool["tool_response"] = map[string]any{"ok": true}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", tool)
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "Stop", "last_assistant_message": "done",
	})

	path, _ := primitives.ParseRepoPath("new.txt")
	result, err := blame.New(repo).Compute(blame.Query{Path: path, Line: 2})
	if err != nil {
		t.Fatalf("Blame: %v", err)
	}
	intent := result.Entries[0].Origin.Intent
	if intent == nil || intent.Status != provenance.IntentStatusCaptured || intent.Confidence != provenance.IntentConfidenceHigh {
		t.Fatalf("renamed-path intent = %#v", intent)
	}
}

func TestIntentRecordedAfterAnEditIsLabeledLate(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, "app.txt", "before\n")
	sessionID := sessionID(t, "codex-late-intent")

	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "UserPromptSubmit", "prompt": "fix retry reset",
	})
	tool := map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "turn_id": "provider-turn-1",
		"hook_event_name": "PreToolUse", "tool_name": "apply_patch", "tool_use_id": "tool-1",
	}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", tool)
	writeFile(t, root, "app.txt", "after\n")
	tool["hook_event_name"] = "PostToolUse"
	tool["tool_response"] = map[string]any{"ok": true}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", tool)
	if _, err := provenance.Record(repo, provenance.RecordInput{
		SessionID: sessionID,
		TurnID:    1,
		Problem:   "retry delay was not reset after success",
		Scope:     []string{"app.txt"},
	}); err != nil {
		t.Fatalf("Record late intent: %v", err)
	}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "Stop", "last_assistant_message": "done",
	})

	path, _ := primitives.ParseRepoPath("app.txt")
	result, err := blame.New(repo).Compute(blame.Query{Path: path, Line: 1})
	if err != nil {
		t.Fatalf("Blame: %v", err)
	}
	intent := result.Entries[0].Origin.Intent
	if intent == nil || intent.Status != provenance.IntentStatusLate || intent.Timing != provenance.IntentTimingAfter || intent.Confidence != provenance.IntentConfidenceLow {
		t.Fatalf("late intent = %#v", intent)
	}
}

func TestLaterAgentsActionDoesNotConsumeEarlierAgentsLateIntent(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, "app.txt", "one\ntwo\n")
	sessionID := sessionID(t, "codex-interleaved-actors")
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "UserPromptSubmit", "prompt": "delegate two edits",
	})

	editA := map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "PreToolUse",
		"tool_name": "apply_patch", "tool_use_id": "edit-a", "agent_id": "agent-a", "agent_type": "worker",
	}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", editA)
	writeFile(t, root, "app.txt", "ONE\ntwo\n")
	editA["hook_event_name"] = "PostToolUse"
	editA["tool_response"] = map[string]any{"ok": true}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", editA)

	intentA := map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "PreToolUse",
		"tool_name": "Bash", "tool_use_id": "intent-a", "agent_id": "agent-a", "agent_type": "worker",
		"tool_input": map[string]any{"command": "turnal intent --session " + sessionID.String() + " --turn 1 --problem stale-first-line"},
	}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", intentA)
	if _, err := provenance.Record(repo, provenance.RecordInput{
		SessionID: sessionID,
		TurnID:    1,
		Problem:   "agent A found the stale first line",
		Scope:     []string{"app.txt"},
	}); err != nil {
		t.Fatalf("Record late agent A intent: %v", err)
	}
	intentA["hook_event_name"] = "PostToolUse"
	intentA["tool_response"] = map[string]any{"ok": true}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", intentA)

	// B sees A's stamp as the latest turn intent, but actor mismatch means B
	// must not claim it and hide its valid late association with A's edit.
	editB := map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "PreToolUse",
		"tool_name": "apply_patch", "tool_use_id": "edit-b", "agent_id": "agent-b", "agent_type": "worker",
	}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", editB)
	writeFile(t, root, "app.txt", "ONE\nTWO\n")
	editB["hook_event_name"] = "PostToolUse"
	editB["tool_response"] = map[string]any{"ok": true}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", editB)
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "Stop", "last_assistant_message": "done",
	})

	path, _ := primitives.ParseRepoPath("app.txt")
	result, err := blame.New(repo).Compute(blame.Query{Path: path, Line: 1})
	if err != nil {
		t.Fatalf("Blame: %v", err)
	}
	intent := result.Entries[0].Origin.Intent
	if intent == nil || intent.Problem != "agent A found the stale first line" || intent.AgentID != "agent-a" || intent.Status != provenance.IntentStatusLate {
		t.Fatalf("agent A late intent = %#v", intent)
	}
}

func TestIntentForNextActionIsNotBackAttributedToEarlierEdit(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, "app.txt", "one\ntwo\n")
	sessionID := sessionID(t, "codex-future-intent")
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "UserPromptSubmit", "prompt": "fix both values",
	})

	first := map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "PreToolUse",
		"tool_name": "apply_patch", "tool_use_id": "tool-1",
	}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", first)
	writeFile(t, root, "app.txt", "ONE\ntwo\n")
	first["hook_event_name"] = "PostToolUse"
	first["tool_response"] = map[string]any{"ok": true}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", first)

	if _, err := provenance.Record(repo, provenance.RecordInput{
		SessionID: sessionID,
		TurnID:    1,
		Problem:   "second value is not normalized",
		Scope:     []string{"app.txt"},
	}); err != nil {
		t.Fatalf("Record intent: %v", err)
	}
	second := map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "PreToolUse",
		"tool_name": "apply_patch", "tool_use_id": "tool-2",
	}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", second)
	writeFile(t, root, "app.txt", "ONE\nTWO\n")
	second["hook_event_name"] = "PostToolUse"
	second["tool_response"] = map[string]any{"ok": true}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", second)
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "Stop", "last_assistant_message": "done",
	})

	path, _ := primitives.ParseRepoPath("app.txt")
	result, err := blame.New(repo).Compute(blame.Query{Path: path})
	if err != nil {
		t.Fatalf("Blame: %v", err)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("entries = %#v", result.Entries)
	}
	if result.Entries[0].Origin.Intent != nil {
		t.Fatalf("earlier line inherited future intent: %#v", result.Entries[0].Origin.Intent)
	}
	if intent := result.Entries[1].Origin.Intent; intent == nil || intent.Problem != "second value is not normalized" || intent.Status != provenance.IntentStatusCaptured {
		t.Fatalf("next action intent = %#v", intent)
	}
}

func TestPostOnlyActionWithRecordedIntentIsAmbiguous(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, "app.txt", "before\n")
	sessionID := sessionID(t, "codex-post-only-intent")
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "UserPromptSubmit", "prompt": "change app",
	})
	if _, err := provenance.Record(repo, provenance.RecordInput{
		SessionID: sessionID,
		TurnID:    1,
		Problem:   "the value is stale",
		Scope:     []string{"app.txt"},
	}); err != nil {
		t.Fatalf("Record intent: %v", err)
	}

	// The pre hook is missing, so Turnal observes only the post boundary.
	writeFile(t, root, "app.txt", "after\n")
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "PostToolUse",
		"tool_name": "apply_patch", "tool_use_id": "post-only", "tool_response": map[string]any{"ok": true},
	})
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "Stop", "last_assistant_message": "done",
	})

	path, _ := primitives.ParseRepoPath("app.txt")
	result, err := blame.New(repo).Compute(blame.Query{Path: path, Line: 1})
	if err != nil {
		t.Fatalf("Blame: %v", err)
	}
	origin := result.Entries[0].Origin
	if origin.Kind != "ambiguous" || origin.Intent != nil {
		t.Fatalf("post-only intent attribution = %#v, want explicit ambiguity", origin)
	}
}

func TestPostOnlyNextActionDoesNotBackAttributeItsIntent(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, "app.txt", "one\ntwo\n")
	sessionID := sessionID(t, "codex-post-only-future-intent")
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "UserPromptSubmit", "prompt": "change both values",
	})

	first := map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "PreToolUse",
		"tool_name": "apply_patch", "tool_use_id": "first-edit",
	}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", first)
	writeFile(t, root, "app.txt", "ONE\ntwo\n")
	first["hook_event_name"] = "PostToolUse"
	first["tool_response"] = map[string]any{"ok": true}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", first)

	if _, err := provenance.Record(repo, provenance.RecordInput{
		SessionID: sessionID,
		TurnID:    1,
		Problem:   "the second value is stale",
		Scope:     []string{"app.txt"},
	}); err != nil {
		t.Fatalf("Record second-action intent: %v", err)
	}
	writeFile(t, root, "app.txt", "ONE\nTWO\n")
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "PostToolUse",
		"tool_name": "apply_patch", "tool_use_id": "post-only-second", "tool_response": map[string]any{"ok": true},
	})
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "Stop", "last_assistant_message": "done",
	})

	path, _ := primitives.ParseRepoPath("app.txt")
	result, err := blame.New(repo).Compute(blame.Query{Path: path})
	if err != nil {
		t.Fatalf("Blame: %v", err)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("entries = %#v", result.Entries)
	}
	for _, entry := range result.Entries {
		if entry.Origin.Intent != nil || entry.Origin.Kind != "ambiguous" {
			t.Fatalf("future post-only intent leaked backward: %#v", entry.Origin)
		}
	}
}

func TestOverlappingIntentCommandsDoNotLeakUnresolvedActorIntent(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, "app.txt", "before\n")
	sessionID := sessionID(t, "codex-unresolved-intent-actor")
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "UserPromptSubmit", "prompt": "delegate work",
	})

	intentTool := func(toolUseID, agentID string) map[string]any {
		return map[string]any{
			"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "PreToolUse",
			"tool_name": "Bash", "tool_use_id": toolUseID, "agent_id": agentID, "agent_type": "worker",
			"tool_input": map[string]any{"command": "turnal intent --session " + sessionID.String() + " --turn 1 --problem delegated"},
		}
	}
	intentA := intentTool("intent-a", "agent-a")
	intentB := intentTool("intent-b", "agent-b")
	handlePayload(t, primitives.AdapterCodex, "CodexHook", intentA)
	handlePayload(t, primitives.AdapterCodex, "CodexHook", intentB)
	if _, err := provenance.Record(repo, provenance.RecordInput{
		SessionID: sessionID,
		TurnID:    1,
		Problem:   "one worker found a stale value",
		Scope:     []string{"app.txt"},
	}); err != nil {
		t.Fatalf("Record unresolved intent: %v", err)
	}
	for _, tool := range []map[string]any{intentA, intentB} {
		tool["hook_event_name"] = "PostToolUse"
		tool["tool_response"] = map[string]any{"ok": true}
		handlePayload(t, primitives.AdapterCodex, "CodexHook", tool)
	}

	mainEdit := map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "PreToolUse",
		"tool_name": "apply_patch", "tool_use_id": "main-edit",
	}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", mainEdit)
	writeFile(t, root, "app.txt", "after\n")
	mainEdit["hook_event_name"] = "PostToolUse"
	mainEdit["tool_response"] = map[string]any{"ok": true}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", mainEdit)
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "Stop", "last_assistant_message": "done",
	})

	path, _ := primitives.ParseRepoPath("app.txt")
	result, err := blame.New(repo).Compute(blame.Query{Path: path, Line: 1})
	if err != nil {
		t.Fatalf("Blame: %v", err)
	}
	origin := result.Entries[0].Origin
	if origin.Kind != "ambiguous" || origin.Intent != nil || origin.ActionTool != "apply_patch" {
		t.Fatalf("unresolved actor intent leaked to main action: %#v", origin)
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "overlapping intent commands") {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
}

func TestOverlappingActionsFallBackToTurnAttribution(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, "app.txt", "one\ntwo\n")
	sessionID := sessionID(t, "codex-overlapping-actions")
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "UserPromptSubmit", "prompt": "fix both values",
	})
	if _, err := provenance.Record(repo, provenance.RecordInput{SessionID: sessionID, TurnID: 1, Problem: "first value is wrong", Scope: []string{"app.txt"}}); err != nil {
		t.Fatalf("Record first intent: %v", err)
	}
	first := map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "PreToolUse",
		"tool_name": "apply_patch", "tool_use_id": "tool-1",
	}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", first)
	if _, err := provenance.Record(repo, provenance.RecordInput{SessionID: sessionID, TurnID: 1, Problem: "second value is wrong", Scope: []string{"app.txt"}}); err != nil {
		t.Fatalf("Record second intent: %v", err)
	}
	second := map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "PreToolUse",
		"tool_name": "apply_patch", "tool_use_id": "tool-2",
	}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", second)

	writeFile(t, root, "app.txt", "ONE\ntwo\n")
	first["hook_event_name"] = "PostToolUse"
	first["tool_response"] = map[string]any{"ok": true}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", first)
	writeFile(t, root, "app.txt", "ONE\nTWO\n")
	second["hook_event_name"] = "PostToolUse"
	second["tool_response"] = map[string]any{"ok": true}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", second)
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "Stop", "last_assistant_message": "done",
	})

	path, _ := primitives.ParseRepoPath("app.txt")
	result, err := blame.New(repo).Compute(blame.Query{Path: path})
	if err != nil {
		t.Fatalf("Blame: %v", err)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("entries = %#v", result.Entries)
	}
	for _, entry := range result.Entries {
		if entry.Origin.Kind != "ambiguous" || entry.Origin.Intent != nil || entry.Origin.ActionTool != "" {
			t.Fatalf("overlapping action received individual attribution: %#v", entry.Origin)
		}
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "overlapping actions") {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
}

func TestIncompleteOverlappingActionFallsBackToTurnAttribution(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, "app.txt", "before\n")
	sessionID := sessionID(t, "codex-incomplete-overlap")
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "UserPromptSubmit", "prompt": "change value",
	})
	if _, err := provenance.Record(repo, provenance.RecordInput{SessionID: sessionID, TurnID: 1, Problem: "first action intent", Scope: []string{"app.txt"}}); err != nil {
		t.Fatalf("Record intent: %v", err)
	}
	first := map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "PreToolUse",
		"tool_name": "apply_patch", "tool_use_id": "tool-1",
	}
	second := map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "PreToolUse",
		"tool_name": "apply_patch", "tool_use_id": "tool-2",
	}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", first)
	handlePayload(t, primitives.AdapterCodex, "CodexHook", second)
	writeFile(t, root, "app.txt", "after\n")
	first["hook_event_name"] = "PostToolUse"
	first["tool_response"] = map[string]any{"ok": true}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", first)
	// tool-2 never reports a result, so its interval is still open.
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "Stop", "last_assistant_message": "done",
	})

	path, _ := primitives.ParseRepoPath("app.txt")
	result, err := blame.New(repo).Compute(blame.Query{Path: path, Line: 1})
	if err != nil {
		t.Fatalf("Blame: %v", err)
	}
	origin := result.Entries[0].Origin
	if origin.Kind != "ambiguous" || origin.Intent != nil || origin.ActionTool != "" {
		t.Fatalf("incomplete overlap received individual attribution: %#v", origin)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "incomplete or overlapping actions") {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
}

func TestClaudeFailedToolUseStillCapturesActionIntent(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, "app.txt", "before\n")
	sessionID := sessionID(t, "claude-failed-tool")
	handlePayload(t, primitives.AdapterClaudeCode, "UserPromptSubmit", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "prompt": "fix generated output",
	})
	if _, err := provenance.Record(repo, provenance.RecordInput{SessionID: sessionID, TurnID: 1, Problem: "generated output is stale", Scope: []string{"app.txt"}}); err != nil {
		t.Fatalf("Record intent: %v", err)
	}
	tool := map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "PreToolUse",
		"tool_name": "Bash", "tool_use_id": "tool-1", "tool_input": map[string]any{"command": "generate app.txt"},
	}
	handlePayload(t, primitives.AdapterClaudeCode, "PreToolUse", tool)
	writeFile(t, root, "app.txt", "partial output\n")
	tool["hook_event_name"] = "PostToolUseFailure"
	tool["error"] = "command exited with status 1"
	tool["duration_ms"] = 25
	handlePayload(t, primitives.AdapterClaudeCode, "PostToolUseFailure", tool)
	handlePayload(t, primitives.AdapterClaudeCode, "Stop", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "last_assistant_message": "generation failed",
	})

	path, _ := primitives.ParseRepoPath("app.txt")
	result, err := blame.New(repo).Compute(blame.Query{Path: path, Line: 1})
	if err != nil {
		t.Fatalf("Blame: %v", err)
	}
	origin := result.Entries[0].Origin
	if origin.ActionTool != "Bash" || origin.Intent == nil || origin.Intent.Problem != "generated output is stale" {
		t.Fatalf("failed action attribution = %#v", origin)
	}
	refs, err := repo.ListPrivateRefs("refs/agent-vcs/actions/" + sessionID.String())
	if err != nil || len(refs) != 2 {
		t.Fatalf("action refs = %#v, err=%v", refs, err)
	}
}

func TestBlameKeepsDistinctIntentsForMultipleEditsInOneTurn(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, "app.txt", "one\ntwo\n")
	sessionID := sessionID(t, "codex-two-intents")
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "UserPromptSubmit", "prompt": "fix both regressions",
	})

	edit := func(toolUseID, problem, contents string) {
		t.Helper()
		if _, err := provenance.Record(repo, provenance.RecordInput{SessionID: sessionID, TurnID: 1, Problem: problem, Scope: []string{"app.txt"}}); err != nil {
			t.Fatalf("Record intent: %v", err)
		}
		tool := map[string]any{
			"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "PreToolUse",
			"tool_name": "apply_patch", "tool_use_id": toolUseID,
		}
		handlePayload(t, primitives.AdapterCodex, "CodexHook", tool)
		writeFile(t, root, "app.txt", contents)
		tool["hook_event_name"] = "PostToolUse"
		tool["tool_response"] = map[string]any{"ok": true}
		handlePayload(t, primitives.AdapterCodex, "CodexHook", tool)
	}
	edit("tool-1", "first value is not normalized", "ONE\ntwo\n")
	edit("tool-2", "second value is not normalized", "ONE\nTWO\n")
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "Stop", "last_assistant_message": "done",
	})

	path, _ := primitives.ParseRepoPath("app.txt")
	result, err := blame.New(repo).Compute(blame.Query{Path: path})
	if err != nil {
		t.Fatalf("Blame: %v", err)
	}
	if len(result.Entries) != 2 || result.Entries[0].Origin.Intent == nil || result.Entries[1].Origin.Intent == nil {
		t.Fatalf("entries = %#v", result.Entries)
	}
	if result.Entries[0].Origin.Intent.Problem != "first value is not normalized" || result.Entries[1].Origin.Intent.Problem != "second value is not normalized" {
		t.Fatalf("line intents = %#v, %#v", result.Entries[0].Origin.Intent, result.Entries[1].Origin.Intent)
	}
}

func TestCodexSubagentActionUsesThatAgentsIntent(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, "app.txt", "before\n")
	sessionID := sessionID(t, "codex-subagent-intent")
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "UserPromptSubmit", "prompt": "delegate two investigations",
	})

	recordForAgent := func(agentID, problem string) {
		t.Helper()
		toolUseID := "intent-" + agentID
		tool := map[string]any{
			"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "PreToolUse",
			"tool_name": "Bash", "tool_use_id": toolUseID, "agent_id": agentID, "agent_type": "worker",
			"tool_input": map[string]any{"command": "turnal intent --session " + sessionID.String() + " --turn 1 --problem " + problem},
		}
		handlePayload(t, primitives.AdapterCodex, "CodexHook", tool)
		if _, err := provenance.Record(repo, provenance.RecordInput{SessionID: sessionID, TurnID: 1, Problem: problem, Scope: []string{"app.txt"}}); err != nil {
			t.Fatalf("Record intent: %v", err)
		}
		tool["hook_event_name"] = "PostToolUse"
		tool["tool_response"] = map[string]any{"ok": true}
		handlePayload(t, primitives.AdapterCodex, "CodexHook", tool)
	}
	recordForAgent("agent-a", "agent A found the stale value")
	recordForAgent("agent-b", "agent B found an unrelated issue")

	action := map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "PreToolUse",
		"tool_name": "apply_patch", "tool_use_id": "edit-a", "agent_id": "agent-a", "agent_type": "worker",
	}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", action)
	writeFile(t, root, "app.txt", "after\n")
	action["hook_event_name"] = "PostToolUse"
	action["tool_response"] = map[string]any{"ok": true}
	handlePayload(t, primitives.AdapterCodex, "CodexHook", action)
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd": root.String(), "session_id": sessionID.String(), "hook_event_name": "Stop", "last_assistant_message": "done",
	})
	if _, err := queryindex.Rebuild(repo); err != nil {
		t.Fatalf("Rebuild index: %v", err)
	}

	path, _ := primitives.ParseRepoPath("app.txt")
	result, err := blame.New(repo).Compute(blame.Query{Path: path, Line: 1})
	if err != nil {
		t.Fatalf("Blame: %v", err)
	}
	origin := result.Entries[0].Origin
	if origin.Intent == nil || origin.Intent.Problem != "agent A found the stale value" || origin.Intent.AgentID != "agent-a" {
		t.Fatalf("subagent intent = %#v", origin.Intent)
	}
	if origin.ActionAgentID != "agent-a" || origin.ActionAgentType != "worker" {
		t.Fatalf("subagent action origin = %#v", origin)
	}
	cached, err := blame.New(repo).Compute(blame.Query{Path: path, Line: 1})
	if err != nil {
		t.Fatalf("Cached blame: %v", err)
	}
	cachedOrigin := cached.Entries[0].Origin
	if cachedOrigin.ActionAgentID != "agent-a" || cachedOrigin.ActionAgentType != "worker" || cachedOrigin.Intent == nil || cachedOrigin.Intent.AgentID != "agent-a" {
		t.Fatalf("cached subagent origin = %#v", cachedOrigin)
	}
}

func TestCodexHookReconcilesProviderTurnsWithWrapperRun(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root.String())
	runID, _ := primitives.NewRunID()
	wrapper := sessionID(t, "wrapper-capture")
	releaseRun, err := runs.Begin(repo, runID, wrapper, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(releaseRun)
	if err := runs.LinkCapture(repo, runID, runs.CaptureWrapper, wrapper, primitives.AdapterCodex); err != nil {
		t.Fatal(err)
	}

	for turn := 1; turn <= 2; turn++ {
		raw, _ := json.Marshal(map[string]any{"cwd": root.String(), "session_id": "provider-capture", "hook_event_name": "UserPromptSubmit", "turn_id": strconv.Itoa(turn), "prompt": "prompt"})
		if err := HandleHookPayloadWithRunID(primitives.AdapterCodex, "UserPromptSubmit", raw, runID.String()); err != nil {
			t.Fatal(err)
		}
		// Exact duplicate delivery must not create another attempt or capture link.
		if err := HandleHookPayloadWithRunID(primitives.AdapterCodex, "UserPromptSubmit", raw, runID.String()); err != nil {
			t.Fatal(err)
		}
		if turn == 1 {
			oneShot, err := runs.Read(repo, runID)
			if err != nil || oneShot.Shape != "single-attempt" || len(oneShot.Attempts) != 1 {
				t.Fatalf("one-shot projection = %+v, %v", oneShot, err)
			}
		}
	}
	projection, err := runs.Read(repo, runID)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Shape != "multi-attempt" || len(projection.Captures) != 2 || len(projection.Attempts) != 2 {
		t.Fatalf("projection = %+v", projection)
	}
	if projection.Captures[0].SessionID == projection.Captures[1].SessionID {
		t.Fatal("wrapper and provider captures were merged")
	}
	for _, attempt := range projection.Attempts {
		for _, source := range attempt.Fields {
			if source.SessionID != sessionID(t, "provider-capture") {
				t.Fatalf("wrapper checkpoint attributed to provider attempt: %+v", source)
			}
		}
	}
}

func TestHookRunCorrelationFailureIsDiagnosticAndDoesNotLink(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root.String())
	raw, _ := json.Marshal(map[string]any{"cwd": root.String(), "session_id": "direct-provider", "hook_event_name": "UserPromptSubmit", "prompt": "hello"})
	if err := HandleHookPayloadWithRunID(primitives.AdapterCodex, "UserPromptSubmit", raw, "run_fabricated"); err != nil {
		t.Fatal(err)
	}
	events := readEvents(t, repo, sessionID(t, "direct-provider"))
	if countEvents(events, primitives.EventTypePromptUser) != 1 || countEvents(events, primitives.EventTypeError) != 1 || countEvents(events, primitives.EventTypeRunCaptureLink) != 0 {
		t.Fatalf("events after invalid correlation = %#v", eventTypes(events))
	}
}

type checkpointEventPayload struct {
	Turn          uint64 `json:"turn"`
	Phase         string `json:"phase"`
	CommitSHA     string `json:"commit_sha"`
	Ref           string `json:"ref"`
	EventSeqStart uint64 `json:"event_seq_start"`
	EventSeqEnd   uint64 `json:"event_seq_end"`
}

func TestHandleNormalizedEventsKeepsDurabilityInCore(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, "app.txt", "before\n")

	rawPrompt := rawPayload(t, map[string]any{
		"sessionId": "external-session", "cwd": root.String(), "prompt": "change app.txt",
	})
	err = HandleNormalizedEvents("gemini-cli", "BeforeAgent", rawPrompt, []adaptersdk.Event{{
		Type: adaptersdk.EventPromptUser, SessionID: "external-session", CWD: root.String(), Text: "change app.txt",
	}})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	writeFile(t, root, "app.txt", "after\n")
	rawFinish := rawPayload(t, map[string]any{
		"session_id": "external-session", "cwd": root.String(), "prompt_response": "done",
	})
	err = HandleNormalizedEvents("gemini-cli", "AfterAgent", rawFinish, []adaptersdk.Event{{
		Type: adaptersdk.EventAssistantMessage, SessionID: "external-session", CWD: root.String(), Text: "done",
	}})
	if err != nil {
		t.Fatalf("assistant: %v", err)
	}

	sessionID := sessionID(t, "external-session")
	events := readEvents(t, repo, sessionID)
	if countEvents(events, primitives.EventTypePromptUser) != 1 || countEvents(events, primitives.EventTypeAssistantMessage) != 1 || countEvents(events, primitives.EventTypeCheckpoint) != 2 {
		t.Fatalf("unexpected events: %#v", eventTypes(events))
	}
	for _, event := range events {
		if event.Adapter != "gemini-cli" || event.RawRef == "" {
			t.Fatalf("event did not retain external adapter provenance: %#v", event)
		}
	}
	turnID, _ := primitives.NewTurnID(1)
	diff, err := repo.DiffTurn(sessionID, turnID)
	if err != nil || !containsAll(string(diff), "-before", "+after") {
		t.Fatalf("DiffTurn = %q, err=%v", diff, err)
	}
}

func TestHandleNormalizedEventsRedactsPromptBearingRawPayloads(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, ".turnal/config.toml", `
version = 1

[secrets]
store_prompts = false
	store_tool_io = true
`)
	const session = "external-prompt-privacy"
	sessionRaw := rawPayload(t, map[string]any{
		"sessionId": session, "cwd": root.String(), "question": "session-prompt-secret",
	})
	if err := HandleNormalizedEvents("custom-adapter", "session", sessionRaw, []adaptersdk.Event{{
		Type: adaptersdk.EventSessionStart, SessionID: session, CWD: root.String(), Text: "session-prompt-secret",
	}}); err != nil {
		t.Fatalf("session: %v", err)
	}
	promptRaw := rawPayload(t, map[string]any{
		"directory": root.String(), "event": map[string]any{
			"properties": map[string]any{"info": map[string]any{"text": "opencode-prompt-secret"}},
		},
	})
	if err := HandleNormalizedEvents("opencode", "event", promptRaw, []adaptersdk.Event{{
		Type: adaptersdk.EventPromptUser, SessionID: session, CWD: root.String(), Text: "opencode-prompt-secret",
	}}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	toolRaw := rawPayload(t, map[string]any{
		"sessionId": session, "cwd": root.String(), "toolPayload": map[string]any{"value": "tool-raw-kept"},
	})
	if err := HandleNormalizedEvents("custom-adapter", "tool", toolRaw, []adaptersdk.Event{{
		Type: adaptersdk.EventToolCall, SessionID: session, CWD: root.String(), ToolName: "read", ToolUseID: "tool-1", Input: json.RawMessage(`{"value":"tool-normalized-kept"}`),
	}}); err != nil {
		t.Fatalf("tool: %v", err)
	}
	assistantRaw := rawPayload(t, map[string]any{
		"sessionId": session, "cwd": root.String(), "unknown": map[string]any{"message": "assistant-raw-secret"},
	})
	if err := HandleNormalizedEvents("custom-adapter", "assistant", assistantRaw, []adaptersdk.Event{{
		Type: adaptersdk.EventAssistantMessage, SessionID: session, CWD: root.String(), Text: "assistant-raw-secret",
	}}); err != nil {
		t.Fatalf("assistant: %v", err)
	}

	events := readEvents(t, repo, sessionID(t, session))
	for _, event := range events {
		if event.Type != primitives.EventTypeSessionStart && event.Type != primitives.EventTypePromptUser && event.Type != primitives.EventTypeAssistantMessage && event.Type != primitives.EventTypeToolCall {
			continue
		}
		record, err := ReadRawHookRecord(repo.MetadataDir, event.RawRef)
		if err != nil {
			t.Fatal(err)
		}
		stored := string(record.Payload)
		if event.Type == primitives.EventTypeToolCall {
			if !strings.Contains(stored, "tool-raw-kept") {
				t.Fatalf("tool-only raw payload was redacted: %s", stored)
			}
			continue
		}
		if strings.Contains(stored, "session-prompt-secret") || strings.Contains(stored, "opencode-prompt-secret") || strings.Contains(stored, "assistant-raw-secret") || !strings.Contains(stored, `"content":"prompt"`) {
			t.Fatalf("prompt-bearing raw payload was not redacted: %s", stored)
		}
		if event.Type != primitives.EventTypeSessionStart && (strings.Contains(string(event.Payload), "opencode-prompt-secret") || strings.Contains(string(event.Payload), "assistant-raw-secret") || !strings.Contains(string(event.Payload), "redacted")) {
			t.Fatalf("normalized prompt payload was not redacted: %s", event.Payload)
		}
	}
}

func TestExternalIntentPrivacyUsesRawPayloadWhenNormalizedInputIsPartial(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, ".turnal/config.toml", `
version = 1

[hooks]
command = "workspace-turnal"

[secrets]
store_prompts = false
store_tool_io = true
`)
	promptRaw := rawPayload(t, map[string]any{
		"sessionId": "external-intent-result", "cwd": root.String(), "prompt": "private request",
	})
	if err := HandleNormalizedEvents("custom-adapter", "prompt", promptRaw, []adaptersdk.Event{{
		Type: adaptersdk.EventPromptUser, SessionID: "external-intent-result", CWD: root.String(), Text: "private request",
	}}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	toolRaw := rawPayload(t, map[string]any{
		"sessionId": "external-intent-result", "cwd": root.String(),
		"invocation": "workspace-turnal intent --problem customer-secret", "result": "result-secret",
	})
	if err := HandleNormalizedEvents("custom-adapter", "tool", toolRaw, []adaptersdk.Event{
		{Type: adaptersdk.EventToolCall, SessionID: "external-intent-result", CWD: root.String(), ToolName: "shell", ToolUseID: "intent-1", Input: json.RawMessage(`{"problem":"customer-secret"}`)},
		{Type: adaptersdk.EventToolResult, SessionID: "external-intent-result", CWD: root.String(), ToolName: "shell", ToolUseID: "intent-1", Input: json.RawMessage(`{"normalized":"partial"}`), Output: json.RawMessage(`{"message":"result-secret"}`)},
	}); err != nil {
		t.Fatalf("tool: %v", err)
	}

	events := readEvents(t, repo, sessionID(t, "external-intent-result"))
	for _, event := range events {
		if event.Type != primitives.EventTypeToolCall && event.Type != primitives.EventTypeToolResult {
			continue
		}
		if strings.Contains(string(event.Payload), "customer-secret") || strings.Contains(string(event.Payload), "result-secret") || !strings.Contains(string(event.Payload), "agent.intent") {
			t.Fatalf("external intent event was not redacted: %s", event.Payload)
		}
		rawRecord, err := ReadRawHookRecord(repo.MetadataDir, event.RawRef)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(rawRecord.Payload), "customer-secret") || strings.Contains(string(rawRecord.Payload), "result-secret") || !strings.Contains(string(rawRecord.Payload), "agent.intent") {
			t.Fatalf("external raw intent was not redacted: %s", rawRecord.Payload)
		}
	}
}

func TestExternalDelayedIntentResultOnlyBatchUsesPriorCallForRedaction(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, ".turnal/config.toml", `
version = 1

[hooks]
command = "workspace-turnal"

[secrets]
store_prompts = false
store_tool_io = true
`)
	const session = "external-intent-separate-result"
	promptRaw := rawPayload(t, map[string]any{
		"sessionId": session, "cwd": root.String(), "prompt": "private request",
	})
	if err := HandleNormalizedEvents("custom-adapter", "prompt", promptRaw, []adaptersdk.Event{{
		Type: adaptersdk.EventPromptUser, SessionID: session, CWD: root.String(), Text: "private request",
	}}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	callRaw := rawPayload(t, map[string]any{
		"sessionId": session, "cwd": root.String(),
		"invocation": "workspace-turnal intent --problem customer-secret",
	})
	if err := HandleNormalizedEvents("custom-adapter", "tool-call", callRaw, []adaptersdk.Event{{
		Type: adaptersdk.EventToolCall, SessionID: session, CWD: root.String(), ToolName: "shell", ToolUseID: "intent-1",
		ProviderTurnID: "provider-turn-1", Input: json.RawMessage(`{"command":"workspace-turnal intent --problem customer-secret"}`),
	}}); err != nil {
		t.Fatalf("tool call: %v", err)
	}
	finishRaw := rawPayload(t, map[string]any{
		"sessionId": session, "cwd": root.String(), "finished": true,
	})
	if err := HandleNormalizedEvents("custom-adapter", "finish", finishRaw, []adaptersdk.Event{{
		Type: adaptersdk.EventTurnFinish, SessionID: session, CWD: root.String(), ProviderTurnID: "provider-turn-1",
	}}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	nextPromptRaw := rawPayload(t, map[string]any{
		"sessionId": session, "cwd": root.String(), "prompt": "next private request",
	})
	if err := HandleNormalizedEvents("custom-adapter", "next-prompt", nextPromptRaw, []adaptersdk.Event{{
		Type: adaptersdk.EventPromptUser, SessionID: session, CWD: root.String(), ProviderTurnID: "provider-turn-2", Text: "next private request",
	}}); err != nil {
		t.Fatalf("next prompt: %v", err)
	}
	resultRaw := rawPayload(t, map[string]any{
		"sessionId": session, "cwd": root.String(), "result": "result-secret",
	})
	if err := HandleNormalizedEvents("custom-adapter", "tool-result", resultRaw, []adaptersdk.Event{{
		Type: adaptersdk.EventToolResult, SessionID: session, CWD: root.String(), ToolName: "shell", ToolUseID: "intent-1",
		ProviderTurnID: "provider-turn-1", Output: json.RawMessage(`{"message":"result-secret"}`),
	}}); err != nil {
		t.Fatalf("tool result: %v", err)
	}

	events := readEvents(t, repo, sessionID(t, session))
	var resultEvent *eventlog.Event
	for index := range events {
		if events[index].Type == primitives.EventTypeToolResult {
			resultEvent = &events[index]
			break
		}
	}
	if resultEvent == nil {
		t.Fatal("tool result event not found")
	}
	if strings.Contains(string(resultEvent.Payload), "result-secret") || !strings.Contains(string(resultEvent.Payload), "agent.intent") {
		t.Fatalf("result-only normalized event was not redacted: %s", resultEvent.Payload)
	}
	rawRecord, err := ReadRawHookRecord(repo.MetadataDir, resultEvent.RawRef)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawRecord.Payload), "result-secret") || !strings.Contains(string(rawRecord.Payload), "agent.intent") {
		t.Fatalf("result-only raw record was not redacted: %s", rawRecord.Payload)
	}
}

func TestBuiltInIntentResultWithoutRepeatedInputIsRedacted(t *testing.T) {
	for _, test := range []struct {
		name         string
		adapter      primitives.AdapterName
		promptHook   string
		toolHook     string
		callTurnID   string
		resultTurnID string
	}{
		{
			name: "claude-code-call-without-provider-turn", adapter: primitives.AdapterClaudeCode,
			promptHook: "UserPromptSubmit", resultTurnID: "provider-turn-1",
		},
		{
			name: "codex-result-without-provider-turn", adapter: primitives.AdapterCodex,
			promptHook: "CodexHook", toolHook: "CodexHook", callTurnID: "provider-turn-1",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireGit(t)
			root := workspaceRoot(t)
			repo, err := checkpoint.Init(root)
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			t.Chdir(root.String())
			writeFile(t, root, ".turnal/config.toml", "version = 1\n\n[secrets]\nstore_prompts = false\nstore_tool_io = true\n")
			session := sessionID(t, "intent-result-"+test.name)
			handlePayload(t, test.adapter, test.promptHook, map[string]any{
				"cwd": root.String(), "session_id": session.String(), "hook_event_name": "UserPromptSubmit", "prompt": "private request",
			})
			preHook := test.toolHook
			if preHook == "" {
				preHook = "PreToolUse"
			}
			handlePayload(t, test.adapter, preHook, map[string]any{
				"cwd": root.String(), "session_id": session.String(), "hook_event_name": "PreToolUse",
				"turn_id": test.callTurnID, "tool_name": "Bash", "tool_use_id": "intent-1",
				"tool_input": map[string]any{"command": "turnal intent --problem customer-secret"},
			})
			postHook := test.toolHook
			if postHook == "" {
				postHook = "PostToolUse"
			}
			handlePayload(t, test.adapter, postHook, map[string]any{
				"cwd": root.String(), "session_id": session.String(), "hook_event_name": "PostToolUse",
				"turn_id": test.resultTurnID, "tool_name": "Bash", "tool_use_id": "intent-1",
				"tool_response": map[string]any{"message": "result-secret"},
			})

			events := readEvents(t, repo, session)
			var resultEvent *eventlog.Event
			for index := range events {
				if events[index].Type == primitives.EventTypeToolResult {
					resultEvent = &events[index]
					break
				}
			}
			if resultEvent == nil {
				t.Fatal("tool result event not found")
			}
			if strings.Contains(string(resultEvent.Payload), "result-secret") || !strings.Contains(string(resultEvent.Payload), "agent.intent") {
				t.Fatalf("normalized result was not redacted: %s", resultEvent.Payload)
			}
			rawRecord, err := ReadRawHookRecord(repo.MetadataDir, resultEvent.RawRef)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(rawRecord.Payload), "result-secret") || !strings.Contains(string(rawRecord.Payload), "agent.intent") {
				t.Fatalf("raw result was not redacted: %s", rawRecord.Payload)
			}
			seenCall, err := sessionHasToolEvent(repo.EventLog(), test.adapter, session, hookPayload{
				ToolUseID: "intent-1",
				TurnID:    test.resultTurnID,
			}, primitives.EventTypeToolCall)
			if err != nil || !seenCall {
				t.Fatalf("partial provider-turn metadata did not match the stored call: seen=%v err=%v", seenCall, err)
			}
		})
	}
}

func TestBuiltInDelayedIntentResultAfterNextTurnIsRedacted(t *testing.T) {
	for _, test := range []struct {
		name        string
		adapter     primitives.AdapterName
		resultEvent string
	}{
		{name: "claude-code-failure", adapter: primitives.AdapterClaudeCode, resultEvent: "PostToolUseFailure"},
		{name: "codex-success", adapter: primitives.AdapterCodex, resultEvent: "PostToolUse"},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireGit(t)
			root := workspaceRoot(t)
			repo, err := checkpoint.Init(root)
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			t.Chdir(root.String())
			writeFile(t, root, ".turnal/config.toml", "version = 1\n\n[secrets]\nstore_prompts = false\nstore_tool_io = true\n")
			session := sessionID(t, "delayed-intent-result-"+test.name)
			hookName := func(event string) string {
				if test.adapter == primitives.AdapterCodex {
					return "CodexHook"
				}
				return event
			}
			handlePayload(t, test.adapter, hookName("UserPromptSubmit"), map[string]any{
				"cwd": root.String(), "session_id": session.String(), "turn_id": "provider-turn-1",
				"hook_event_name": "UserPromptSubmit", "prompt": "private request",
			})
			handlePayload(t, test.adapter, hookName("PreToolUse"), map[string]any{
				"cwd": root.String(), "session_id": session.String(), "turn_id": "provider-turn-1",
				"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_use_id": "intent-1",
				"tool_input": map[string]any{"command": "turnal intent --problem customer-secret"},
			})
			handlePayload(t, test.adapter, hookName("Stop"), map[string]any{
				"cwd": root.String(), "session_id": session.String(), "turn_id": "provider-turn-1",
				"hook_event_name": "Stop", "last_assistant_message": "done",
			})
			handlePayload(t, test.adapter, hookName("UserPromptSubmit"), map[string]any{
				"cwd": root.String(), "session_id": session.String(), "turn_id": "provider-turn-2",
				"hook_event_name": "UserPromptSubmit", "prompt": "next private request",
			})
			resultPayload := map[string]any{
				"cwd": root.String(), "session_id": session.String(), "turn_id": "provider-turn-1",
				"hook_event_name": test.resultEvent, "tool_name": "Bash", "tool_use_id": "intent-1",
			}
			if test.resultEvent == "PostToolUseFailure" {
				resultPayload["error"] = "result-secret"
			} else {
				resultPayload["tool_response"] = map[string]any{"message": "result-secret"}
			}
			handlePayload(t, test.adapter, hookName(test.resultEvent), resultPayload)

			events := readEvents(t, repo, session)
			var resultEvent *eventlog.Event
			for index := range events {
				if events[index].Type == primitives.EventTypeToolResult {
					resultEvent = &events[index]
					break
				}
			}
			if resultEvent == nil {
				t.Fatal("delayed tool result event not found")
			}
			if strings.Contains(string(resultEvent.Payload), "result-secret") || !strings.Contains(string(resultEvent.Payload), "agent.intent") {
				t.Fatalf("delayed normalized result was not redacted: %s", resultEvent.Payload)
			}
			rawRecord, err := ReadRawHookRecord(repo.MetadataDir, resultEvent.RawRef)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(rawRecord.Payload), "result-secret") || !strings.Contains(string(rawRecord.Payload), "agent.intent") {
				t.Fatalf("delayed raw result was not redacted: %s", rawRecord.Payload)
			}
		})
	}
}

func TestHandleClaudeHookPayloadCreatesAutomaticTurn(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	writeFile(t, root, "app.txt", "before\n")
	handlePayload(t, primitives.AdapterClaudeCode, "UserPromptSubmit", map[string]any{
		"cwd":        root.String(),
		"session_id": "Claude-Session",
		"prompt":     "change app.txt",
	})
	writeFile(t, root, "app.txt", "after\n")
	handlePayload(t, primitives.AdapterClaudeCode, "PostToolUse", map[string]any{
		"cwd":           root.String(),
		"session_id":    "Claude-Session",
		"tool_name":     "Write",
		"tool_use_id":   "tool-1",
		"tool_input":    map[string]any{"file_path": "app.txt", "content": "after\n"},
		"tool_response": map[string]any{"ok": true},
	})
	handlePayload(t, primitives.AdapterClaudeCode, "Stop", map[string]any{
		"cwd":                    root.String(),
		"session_id":             "Claude-Session",
		"last_assistant_message": "done",
	})

	sessionID := sessionID(t, "claude-session")
	turnID, _ := primitives.NewTurnID(1)
	diff, err := repo.DiffTurn(sessionID, turnID)
	if err != nil {
		t.Fatalf("DiffTurn: %v", err)
	}
	diffText := string(diff)
	if !containsAll(diffText, "diff --git a/app.txt b/app.txt", "-before", "+after") {
		t.Fatalf("unexpected diff:\n%s", diffText)
	}

	events := readEvents(t, repo, sessionID)
	wantTypes := []primitives.EventType{
		primitives.EventTypeSessionStart,
		primitives.EventTypeTurnStart,
		primitives.EventTypeCheckpoint,
		primitives.EventTypePromptUser,
		primitives.EventTypeToolCall,
		primitives.EventTypeToolResult,
		primitives.EventTypeAssistantMessage,
		primitives.EventTypeTurnFinish,
		primitives.EventTypeCheckpoint,
	}
	if got := eventTypes(events); !sameEventTypes(got, wantTypes) {
		t.Fatalf("event types = %#v, want %#v", got, wantTypes)
	}
	preCheckpoint := checkpointPayloadForPhase(t, events, primitives.CheckpointPhasePre)
	if preCheckpoint.EventSeqStart != 1 || preCheckpoint.EventSeqEnd != 3 {
		t.Fatalf("pre checkpoint event range = %d-%d, want 1-3", preCheckpoint.EventSeqStart, preCheckpoint.EventSeqEnd)
	}
	postCheckpoint := checkpointPayloadForPhase(t, events, primitives.CheckpointPhasePost)
	if postCheckpoint.EventSeqStart != 4 || postCheckpoint.EventSeqEnd != 9 {
		t.Fatalf("post checkpoint event range = %d-%d, want 4-9", postCheckpoint.EventSeqStart, postCheckpoint.EventSeqEnd)
	}
	for _, event := range events {
		if event.RawRef == "" {
			t.Fatalf("event missing raw ref: %#v", event)
		}
	}

	if _, ok, err := turns.NewManager(repo).Active(sessionID); err != nil {
		t.Fatalf("Active: %v", err)
	} else if ok {
		t.Fatal("turn still active after Stop")
	}
}

func TestClaudePromptInheritsSessionModel(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	handlePayload(t, primitives.AdapterClaudeCode, "SessionStart", map[string]any{
		"cwd":             root.String(),
		"session_id":      "claude-model-session",
		"hook_event_name": "SessionStart",
		"model":           "claude-sonnet-4-6",
		"source":          "startup",
	})
	handlePayload(t, primitives.AdapterClaudeCode, "UserPromptSubmit", map[string]any{
		"cwd":        root.String(),
		"session_id": "claude-model-session",
		"prompt":     "inspect the model",
	})

	prompt := promptEventPayload(t, readEvents(t, repo, sessionID(t, "claude-model-session")))
	if prompt.Model != "claude-sonnet-4-6" {
		t.Fatalf("prompt model = %q, want inherited Claude session model", prompt.Model)
	}
}

func TestClaudeStopDerivesModelFromMatchingTranscript(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	const session = "claude-transcript-model"
	const response = "captured with transcript model"
	transcriptPath := filepath.Join(root.String(), session+".jsonl")
	matchingEntry, err := json.Marshal(map[string]any{
		"type":      "assistant",
		"sessionId": session,
		"cwd":       root.String(),
		"message": map[string]any{
			"role":    "assistant",
			"model":   "claude-opus-5",
			"content": []map[string]string{{"type": "text", "text": response}},
		},
	})
	if err != nil {
		t.Fatalf("marshal transcript entry: %v", err)
	}
	writeFile(t, root, session+".jsonl", "malformed\n"+string(matchingEntry)+"\n")

	handlePayload(t, primitives.AdapterClaudeCode, "UserPromptSubmit", map[string]any{
		"cwd":             root.String(),
		"session_id":      session,
		"transcript_path": transcriptPath,
		"prompt":          "inspect the completed model",
	})
	handlePayload(t, primitives.AdapterClaudeCode, "Stop", map[string]any{
		"cwd":                    root.String(),
		"session_id":             session,
		"transcript_path":        transcriptPath,
		"last_assistant_message": response,
	})

	assistant := assistantEventPayload(t, readEvents(t, repo, sessionID(t, session)))
	if assistant.Model != "claude-opus-5" {
		t.Fatalf("assistant model = %q, want transcript model", assistant.Model)
	}
}

func TestClaudeTranscriptModelRejectsTraversalPath(t *testing.T) {
	root := t.TempDir()
	const session = "claude-traversal-model"
	entry, err := json.Marshal(map[string]any{
		"type":      "assistant",
		"sessionId": session,
		"cwd":       root,
		"message": map[string]any{
			"role":    "assistant",
			"model":   "claude-sonnet-5",
			"content": []map[string]string{{"type": "text", "text": "done"}},
		},
	})
	if err != nil {
		t.Fatalf("marshal transcript entry: %v", err)
	}
	cleanPath := filepath.Join(root, session+".jsonl")
	if err := os.WriteFile(cleanPath, append(entry, '\n'), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	uncleanPath := filepath.Join(root, "nested") + string(os.PathSeparator) + ".." + string(os.PathSeparator) + session + ".jsonl"

	model := claudeCompletedTurnModel(hookPayload{
		SessionID:            session,
		TranscriptPath:       uncleanPath,
		CWD:                  root,
		LastAssistantMessage: "done",
	})
	if model != "" {
		t.Fatalf("model = %q for traversal-bearing transcript path", model)
	}
}

func TestHandleHookPayloadIsIdempotentForDuplicatePrompt(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	payload := map[string]any{
		"cwd":        root.String(),
		"session_id": "codex-session",
		"prompt":     "make change",
	}
	handlePayload(t, primitives.AdapterCodex, "UserPromptSubmit", payload)
	handlePayload(t, primitives.AdapterCodex, "UserPromptSubmit", payload)

	sessionID := sessionID(t, "codex-session")
	events := readEvents(t, repo, sessionID)
	if countEvents(events, primitives.EventTypePromptUser) != 1 {
		t.Fatalf("prompt events = %d, want 1; events=%#v", countEvents(events, primitives.EventTypePromptUser), events)
	}
	if countEvents(events, primitives.EventTypeTurnStart) != 1 {
		t.Fatalf("turn start events = %d, want 1", countEvents(events, primitives.EventTypeTurnStart))
	}
	active, ok, err := turns.NewManager(repo).Active(sessionID)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if !ok || active.TurnID.Uint64() != 1 {
		t.Fatalf("active = %#v ok=%v, want turn 1 active", active, ok)
	}
}

func TestHandleHookPayloadAppliesSecretsRedactionPolicy(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, ".turnal/config.toml", `
version = 1

[secrets]
store_prompts = false
store_tool_io = false
`)

	handlePayload(t, primitives.AdapterClaudeCode, "UserPromptSubmit", map[string]any{
		"cwd":        root.String(),
		"session_id": "secret-session",
		"prompt":     "token=secret",
	})
	handlePayload(t, primitives.AdapterClaudeCode, "PostToolUse", map[string]any{
		"cwd":           root.String(),
		"session_id":    "secret-session",
		"tool_name":     "Write",
		"tool_use_id":   "tool-1",
		"tool_input":    map[string]any{"content": "secret"},
		"tool_response": map[string]any{"output": "secret"},
	})
	handlePayload(t, primitives.AdapterClaudeCode, "PostToolUseFailure", map[string]any{
		"cwd":         root.String(),
		"session_id":  "secret-session",
		"tool_name":   "Write",
		"tool_use_id": "tool-2",
		"tool_input":  map[string]any{"content": "failed-input-secret"},
		"error":       "failed-output-secret",
	})

	sessionID := sessionID(t, "secret-session")
	events := readEvents(t, repo, sessionID)
	for _, event := range events {
		switch event.Type {
		case primitives.EventTypePromptUser:
			var payload promptPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatalf("unmarshal prompt payload: %v", err)
			}
			if strings.Contains(payload.Text, "token=secret") || !strings.Contains(payload.Text, "redacted") {
				t.Fatalf("prompt text not redacted: %#v", payload)
			}
			rawRecord, err := ReadRawHookRecord(repo.MetadataDir, event.RawRef)
			if err != nil {
				t.Fatalf("ReadRawHookRecord: %v", err)
			}
			if strings.Contains(string(rawRecord.Payload), "token=secret") {
				t.Fatalf("raw prompt payload was not redacted: %s", rawRecord.Payload)
			}
		case primitives.EventTypeToolCall:
			var payload toolCallPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatalf("unmarshal tool call payload: %v", err)
			}
			if strings.Contains(string(payload.Input), `"content":"secret"`) || strings.Contains(string(payload.Input), "failed-input-secret") || !strings.Contains(string(payload.Input), "redacted") {
				t.Fatalf("tool input not redacted: %s", payload.Input)
			}
		case primitives.EventTypeToolResult:
			var payload toolResultPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatalf("unmarshal tool result payload: %v", err)
			}
			if strings.Contains(string(payload.Output), `"output":"secret"`) || strings.Contains(string(payload.Output), "failed-output-secret") || !strings.Contains(string(payload.Output), "redacted") {
				t.Fatalf("tool output not redacted: %s", payload.Output)
			}
		}
		if event.Type == primitives.EventTypeToolCall || event.Type == primitives.EventTypeToolResult {
			rawRecord, err := ReadRawHookRecord(repo.MetadataDir, event.RawRef)
			if err != nil {
				t.Fatalf("ReadRawHookRecord: %v", err)
			}
			storedRaw := string(rawRecord.Payload)
			if strings.Contains(storedRaw, "failed-input-secret") || strings.Contains(storedRaw, "failed-output-secret") || strings.Contains(storedRaw, `"content":"secret"`) || strings.Contains(storedRaw, `"output":"secret"`) || !strings.Contains(storedRaw, "redacted") {
				t.Fatalf("raw tool payload not redacted: %s", rawRecord.Payload)
			}
		}
	}
}

func TestPromptPrivacyAlsoRedactsIntentCommandArguments(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())
	writeFile(t, root, ".turnal/config.toml", `
version = 1

[hooks]
command = "workspace-turnal"

[secrets]
store_prompts = false
store_tool_io = true
`)
	handlePayload(t, primitives.AdapterClaudeCode, "UserPromptSubmit", map[string]any{
		"cwd": root.String(), "session_id": "private-intent", "prompt": "private request",
	})
	tool := map[string]any{
		"cwd": root.String(), "session_id": "private-intent", "tool_name": "Bash", "tool_use_id": "intent-command",
		"tool_input": map[string]any{"command": `workspace-turnal intent --session private-intent --problem "customer secret"`},
	}
	handlePayload(t, primitives.AdapterClaudeCode, "PreToolUse", tool)
	tool["error"] = `command failed: workspace-turnal intent --problem "customer secret"`
	handlePayload(t, primitives.AdapterClaudeCode, "PostToolUseFailure", tool)

	events := readEvents(t, repo, sessionID(t, "private-intent"))
	foundCall := false
	foundResult := false
	for _, event := range events {
		if event.Type != primitives.EventTypeToolCall && event.Type != primitives.EventTypeToolResult {
			continue
		}
		foundCall = foundCall || event.Type == primitives.EventTypeToolCall
		foundResult = foundResult || event.Type == primitives.EventTypeToolResult
		if strings.Contains(string(event.Payload), "customer secret") || !strings.Contains(string(event.Payload), "agent.intent") {
			t.Fatalf("tool event did not redact intent command data: %s", event.Payload)
		}
		rawRecord, err := ReadRawHookRecord(repo.MetadataDir, event.RawRef)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(rawRecord.Payload), "customer secret") || !strings.Contains(string(rawRecord.Payload), "agent.intent") {
			t.Fatalf("raw hook did not redact intent command data: %s", rawRecord.Payload)
		}
	}
	if !foundCall || !foundResult {
		t.Fatalf("intent command events: call=%v result=%v", foundCall, foundResult)
	}
}

func TestIntentCommandDetectionHandlesProviderCommandShapes(t *testing.T) {
	for _, input := range []json.RawMessage{
		json.RawMessage(`{"command":"/workspace/bin/turnal intent --problem secret"}`),
		json.RawMessage(`{"command":"C:\\Program Files\\Turnal\\turnal.exe intent --problem secret"}`),
		json.RawMessage(`{"command":["turnal","intent","--problem","secret"]}`),
	} {
		if !rawContainsIntentCommand(input, "turnal") {
			t.Fatalf("intent command was not detected in %s", input)
		}
	}
	if !rawContainsIntentCommand(json.RawMessage(`{"command":"workspace-turnal intent --problem secret"}`), "workspace-turnal") {
		t.Fatal("configured intent command was not detected")
	}
	if rawContainsIntentCommand(json.RawMessage(`{"command":"workspace-turnal intent --problem secret"}`), "other-wrapper") {
		t.Fatal("unconfigured wrapper was detected as an intent command")
	}
	if rawContainsIntentCommand(json.RawMessage(`{"command":"turnal status"}`), "turnal") {
		t.Fatal("non-intent Turnal command was detected as intent")
	}
}

func TestHandleHookPayloadSerializesConcurrentDuplicatePrompt(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	raw, err := json.Marshal(map[string]any{
		"cwd":             root.String(),
		"session_id":      "codex-session",
		"hook_event_name": "UserPromptSubmit",
		"turn_id":         "turn-1",
		"prompt":          "make change",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- HandleHookPayload(primitives.AdapterCodex, "CodexHook", raw)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("HandleHookPayload: %v", err)
		}
	}

	sessionID := sessionID(t, "codex-session")
	events := readEvents(t, repo, sessionID)
	for _, event := range events {
		if event.Type == primitives.EventTypeError {
			t.Fatalf("unexpected error event: %#v", event)
		}
	}
	if countEvents(events, primitives.EventTypeTurnStart) != 1 {
		t.Fatalf("turn starts = %d, want 1; events=%#v", countEvents(events, primitives.EventTypeTurnStart), events)
	}
	if countEvents(events, primitives.EventTypePromptUser) != 1 {
		t.Fatalf("prompt events = %d, want 1; events=%#v", countEvents(events, primitives.EventTypePromptUser), events)
	}
	if countEvents(events, primitives.EventTypeCheckpoint) != 1 {
		t.Fatalf("checkpoint events = %d, want 1; events=%#v", countEvents(events, primitives.EventTypeCheckpoint), events)
	}
}

func TestHandleCodexDocumentedHookPayloads(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	writeFile(t, root, "app.txt", "before\n")
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd":             root.String(),
		"session_id":      "codex-session",
		"hook_event_name": "SessionStart",
		"transcript_path": nil,
		"model":           "gpt-5.5",
		"permission_mode": "default",
		"source":          "startup",
	})
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd":             root.String(),
		"session_id":      "codex-session",
		"hook_event_name": "UserPromptSubmit",
		"turn_id":         "turn-1",
		"model":           "gpt-5.6-sol",
		"prompt":          "change app.txt",
		"permission_mode": "default",
	})
	writeFile(t, root, "app.txt", "after\n")
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd":             root.String(),
		"session_id":      "codex-session",
		"hook_event_name": "PostToolUse",
		"turn_id":         "turn-1",
		"tool_name":       "Bash",
		"tool_use_id":     "call-1",
		"tool_input":      map[string]any{"command": "printf after > app.txt"},
		"tool_response":   map[string]any{"exit_code": 0, "stdout": ""},
		"permission_mode": "default",
	})
	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd":                    root.String(),
		"session_id":             "codex-session",
		"hook_event_name":        "Stop",
		"turn_id":                "turn-1",
		"stop_hook_active":       false,
		"last_assistant_message": "done",
		"permission_mode":        "default",
	})

	sessionID := sessionID(t, "codex-session")
	events := readEvents(t, repo, sessionID)
	wantTypes := []primitives.EventType{
		primitives.EventTypeSessionStart,
		primitives.EventTypeTurnStart,
		primitives.EventTypeCheckpoint,
		primitives.EventTypePromptUser,
		primitives.EventTypeToolCall,
		primitives.EventTypeToolResult,
		primitives.EventTypeAssistantMessage,
		primitives.EventTypeTurnFinish,
		primitives.EventTypeCheckpoint,
	}
	if got := eventTypes(events); !sameEventTypes(got, wantTypes) {
		t.Fatalf("event types = %#v, want %#v", got, wantTypes)
	}
	if countEvents(events, primitives.EventTypeSessionStart) != 1 {
		t.Fatalf("session starts = %d, want 1", countEvents(events, primitives.EventTypeSessionStart))
	}
	if prompt := promptEventPayload(t, events); prompt.Model != "gpt-5.6-sol" {
		t.Fatalf("prompt model = %q, want turn-scoped Codex model", prompt.Model)
	}
}

func TestHandleCodexUnsupportedDocumentedHookFallsBackToRawEvent(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	handlePayload(t, primitives.AdapterCodex, "CodexHook", map[string]any{
		"cwd":             root.String(),
		"session_id":      "codex-session",
		"hook_event_name": "PreCompact",
		"turn_id":         "turn-1",
		"trigger":         "manual",
	})

	sessionID := sessionID(t, "codex-session")
	events := readEvents(t, repo, sessionID)
	if len(events) != 1 || events[0].Type != primitives.EventTypeAdapterRaw {
		t.Fatalf("events = %#v, want one adapter.raw event", events)
	}
}

func TestNextPromptFinishesStaleActiveTurn(t *testing.T) {
	requireGit(t)

	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Chdir(root.String())

	writeFile(t, root, "app.txt", "first\n")
	handlePayload(t, primitives.AdapterCodex, "UserPromptSubmit", map[string]any{
		"cwd":        root.String(),
		"session_id": "codex-session",
		"prompt":     "first prompt",
	})
	writeFile(t, root, "app.txt", "second\n")
	handlePayload(t, primitives.AdapterCodex, "UserPromptSubmit", map[string]any{
		"cwd":        root.String(),
		"session_id": "codex-session",
		"prompt":     "second prompt",
	})

	sessionID := sessionID(t, "codex-session")
	events := readEvents(t, repo, sessionID)
	if countEvents(events, primitives.EventTypeTurnStart) != 2 {
		t.Fatalf("turn starts = %d, want 2; events=%#v", countEvents(events, primitives.EventTypeTurnStart), events)
	}
	if countEvents(events, primitives.EventTypeTurnFinish) != 1 {
		t.Fatalf("turn finishes = %d, want 1; events=%#v", countEvents(events, primitives.EventTypeTurnFinish), events)
	}

	turn1, _ := primitives.NewTurnID(1)
	if _, err := repo.DiffTurn(sessionID, turn1); err != nil {
		t.Fatalf("turn 1 should have pre/post checkpoints: %v", err)
	}
	turn2, _ := primitives.NewTurnID(2)
	if preTurn1 := checkpointPayloadForTurnPhase(t, events, turn1, primitives.CheckpointPhasePre); preTurn1.EventSeqStart != 1 || preTurn1.EventSeqEnd != 3 {
		t.Fatalf("turn 1 pre checkpoint event range = %d-%d, want 1-3", preTurn1.EventSeqStart, preTurn1.EventSeqEnd)
	}
	if postTurn1 := checkpointPayloadForTurnPhase(t, events, turn1, primitives.CheckpointPhasePost); postTurn1.EventSeqStart != 4 || postTurn1.EventSeqEnd != 6 {
		t.Fatalf("turn 1 post checkpoint event range = %d-%d, want 4-6", postTurn1.EventSeqStart, postTurn1.EventSeqEnd)
	}
	if preTurn2 := checkpointPayloadForTurnPhase(t, events, turn2, primitives.CheckpointPhasePre); preTurn2.EventSeqStart != 7 || preTurn2.EventSeqEnd != 8 {
		t.Fatalf("turn 2 pre checkpoint event range = %d-%d, want 7-8", preTurn2.EventSeqStart, preTurn2.EventSeqEnd)
	}
	active, ok, err := turns.NewManager(repo).Active(sessionID)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if !ok || active.TurnID.Uint64() != 2 {
		t.Fatalf("active = %#v ok=%v, want turn 2 active", active, ok)
	}
}

func handlePayload(t *testing.T, adapter primitives.AdapterName, hookName string, payload map[string]any) {
	t.Helper()
	raw := rawPayload(t, payload)
	if err := HandleHookPayload(adapter, hookName, raw); err != nil {
		t.Fatalf("HandleHookPayload %s: %v", hookName, err)
	}
}

func rawPayload(t *testing.T, payload map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return raw
}

func readEvents(t *testing.T, repo *checkpoint.Repo, sessionID primitives.SessionID) []eventlog.Event {
	t.Helper()
	events, err := eventlog.Open(repo.MetadataDir).Read(sessionID)
	if err != nil {
		t.Fatalf("Read events: %v", err)
	}
	return events
}

func eventTypes(events []eventlog.Event) []primitives.EventType {
	types := make([]primitives.EventType, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}

func sameEventTypes(left, right []primitives.EventType) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func countEvents(events []eventlog.Event, eventType primitives.EventType) int {
	var count int
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func promptEventPayload(t *testing.T, events []eventlog.Event) promptPayload {
	t.Helper()
	for _, event := range events {
		if event.Type != primitives.EventTypePromptUser {
			continue
		}
		var payload promptPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("unmarshal prompt payload: %v", err)
		}
		return payload
	}
	t.Fatal("missing prompt event")
	return promptPayload{}
}

func assistantEventPayload(t *testing.T, events []eventlog.Event) assistantPayload {
	t.Helper()
	for _, event := range events {
		if event.Type != primitives.EventTypeAssistantMessage {
			continue
		}
		var payload assistantPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("unmarshal assistant payload: %v", err)
		}
		return payload
	}
	t.Fatal("missing assistant event")
	return assistantPayload{}
}

func checkpointPayloadForPhase(t *testing.T, events []eventlog.Event, phase primitives.CheckpointPhase) checkpointEventPayload {
	t.Helper()
	for _, event := range events {
		if event.Type != primitives.EventTypeCheckpoint {
			continue
		}
		var payload checkpointEventPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("unmarshal checkpoint payload: %v", err)
		}
		if payload.Phase == phase.String() {
			return payload
		}
	}
	t.Fatalf("missing %s checkpoint event", phase)
	return checkpointEventPayload{}
}

func checkpointPayloadForTurnPhase(t *testing.T, events []eventlog.Event, turnID primitives.TurnID, phase primitives.CheckpointPhase) checkpointEventPayload {
	t.Helper()
	for _, event := range events {
		if event.Type != primitives.EventTypeCheckpoint {
			continue
		}
		var payload checkpointEventPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("unmarshal checkpoint payload: %v", err)
		}
		if payload.Turn == turnID.Uint64() && payload.Phase == phase.String() {
			return payload
		}
	}
	t.Fatalf("missing turn %s %s checkpoint event", turnID, phase)
	return checkpointEventPayload{}
}

func containsAll(value string, wants ...string) bool {
	for _, want := range wants {
		if !strings.Contains(value, want) {
			return false
		}
	}
	return true
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
