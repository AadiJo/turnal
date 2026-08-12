import assert from "node:assert/strict";
import test from "node:test";
import {
  parseBlameResult,
  parseDiffDocumentsResult,
  parseRollbackPreview,
  parseSessionsResult,
  TurnalCliCompatibilityError,
  turnsForSession,
} from "../turnal/types";

test("parses session turns and accepts the older latest_turn fallback", () => {
  const result = parseSessionsResult({
    schema_version: 1,
    total_sessions: 1,
    sessions: [
      {
        session_id: "demo",
        parent_session_id: "root",
        parent_tool_use_id: "task-1",
        status: "complete",
        turn_count: 1,
        complete_turn_count: 1,
        active_turn_count: 0,
        event_count: 3,
        rollback_count: 0,
        rollbacks: [],
        latest_turn: { turn_id: 1, status: "complete", prompt: "fix it" },
      },
    ],
  });
  assert.deepEqual(turnsForSession(result.sessions[0]).map((turn) => turn.turn_id), [1]);
  assert.deepEqual(result.sessions[0].rollbacks, []);
  assert.equal(result.sessions[0].rollback_count, 0);
  assert.equal(result.sessions[0].parent_session_id, "root");
  assert.equal(result.sessions[0].parent_tool_use_id, "task-1");
});

test("parses first-class rollback activity from sessions", () => {
  const result = parseSessionsResult({
    schema_version: 1,
    total_sessions: 1,
    sessions: [
      {
        session_id: "demo",
        status: "complete",
        turn_count: 1,
        complete_turn_count: 1,
        active_turn_count: 0,
        event_count: 4,
        rollback_count: 1,
        rollbacks: [
          {
            sequence: 4,
            turn_id: 1,
            target: "demo:turn:1:pre",
            phase: "pre",
            mode: "checkpoint",
            time: "2026-07-13T12:00:00Z",
            change_summary: { total: 2, added: 0, modified: 1, deleted: 1, mode_changed: 0 },
          },
        ],
      },
    ],
  });

  assert.equal(result.sessions[0].rollbacks[0].target, "demo:turn:1:pre");
  assert.equal(result.sessions[0].rollbacks[0].change_summary.total, 2);
});

test("rejects a CLI without the supported sessions API", () => {
  assert.throws(
    () => parseSessionsResult({ total_sessions: 0, sessions: [] }),
    (error) => error instanceof TurnalCliCompatibilityError && /Update Turnal/.test(error.message),
  );
  assert.throws(
    () => parseSessionsResult({ schema_version: 2, total_sessions: 0, sessions: [] }),
    (error) => error instanceof TurnalCliCompatibilityError && error.actualSchemaVersion === 2,
  );
});

test("rejects malformed blame json at the boundary", () => {
  assert.throws(
    () =>
      parseBlameResult({
        path: "app.ts",
        latest_ref: "ref",
        latest_commit: "commit",
        latest_time: "2026-07-13T10:00:00Z",
        complete_turns: 1,
        entries: [{ line: "one", text: "code", origin: { kind: "turn" } }],
      }),
    /entries\[0\]\.line must be a finite number/,
  );
});

test("parses intent-first blame provenance", () => {
  const result = parseBlameResult({
    path: "app.ts",
    latest_ref: "refs/agent-vcs/checkpoints/demo/turn/000001/post",
    latest_commit: "abc123",
    latest_time: "2026-07-13T10:00:00Z",
    complete_turns: 1,
    entries: [
      {
        line: 1,
        text: "resetDelay();",
        origin: {
          kind: "turn",
          session_id: "demo",
          turn_id: 1,
          prompt: "fix flaky retries",
          action_tool: "apply_patch",
          action_agent_id: "agent-a",
          action_agent_type: "worker",
          intent: {
            problem: "retry delay was not reset after success",
            scope: ["app.ts"],
            evidence: ["test:TestRetryReset"],
            event_seq: 5,
            status: "captured",
            timing: "before",
            confidence: "high",
            agent_id: "agent-a",
            agent_type: "worker",
            redacted: false,
          },
        },
      },
    ],
  });
  assert.equal(result.entries[0].origin.intent?.problem, "retry delay was not reset after success");
  assert.equal(result.entries[0].origin.action_tool, "apply_patch");
  assert.equal(result.entries[0].origin.action_agent_id, "agent-a");
  assert.equal(result.entries[0].origin.intent?.agent_id, "agent-a");
  assert.equal(result.entries[0].origin.intent?.redacted, false);
  assert.deepEqual(result.entries[0].origin.intent?.evidence, ["test:TestRetryReset"]);
});

test("parses explicit ambiguous blame origins", () => {
  const result = parseBlameResult({
    path: "app.ts",
    latest_ref: "ref",
    latest_commit: "commit",
    latest_time: "2026-07-13T10:00:00Z",
    complete_turns: 2,
    entries: [
      { line: 1, text: "one", origin: { kind: "ambiguous", session_id: "demo", turn_id: 1 } },
      { line: 2, text: "two", origin: { kind: "concurrent" } },
    ],
  });
  assert.deepEqual(result.entries.map((entry) => entry.origin.kind), ["ambiguous", "concurrent"]);
});

test("extracts affected files from rollback dry-run text", () => {
  const preview = parseRollbackPreview(`Dry-run rollback
  target: demo:turn:2:pre
modified:
  src/app.ts
deleted:
  generated.txt
`);
  assert.deepEqual(preview.changes, [
    { action: "modified", path: "src/app.ts" },
    { action: "deleted", path: "generated.txt" },
  ]);
  assert.equal(preview.no_changes, false);
});

test("accepts an explicit empty rollback plan and rejects unknown output", () => {
  assert.equal(parseRollbackPreview("Dry-run rollback\nno changes\n").no_changes, true);
  assert.throws(
    () => parseRollbackPreview("Dry-run workspace-git rollback\n  commits: abc -> def\n"),
    /unrecognized rollback preview/,
  );
});

test("decodes checkpoint documents for VS Code native diff editors", () => {
  const result = parseDiffDocumentsResult({
    kind: "turn",
    session_id: "claude-session",
    turn_id: 2,
    files: [
      {
        status: "M",
        path: "src/app.ts",
        additions: 2,
        deletions: 1,
        before_exists: true,
        after_exists: true,
        before_base64: Buffer.from("const version = 1;\n").toString("base64"),
        after_base64: Buffer.from("const version = 2;\n").toString("base64"),
      },
    ],
  });
  assert.equal(result.files[0].before_text, "const version = 1;\n");
  assert.equal(result.files[0].after_text, "const version = 2;\n");
  assert.equal(result.files[0].additions, 2);
});
