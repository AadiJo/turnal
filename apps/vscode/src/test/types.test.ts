import assert from "node:assert/strict";
import test from "node:test";
import {
  parseBlameResult,
  parseDiffDocumentsResult,
  parseRollbackPreview,
  parseSessionsResult,
  turnsForSession,
} from "../turnal/types";

test("parses session turns and accepts the older latest_turn fallback", () => {
  const result = parseSessionsResult({
    total_sessions: 1,
    sessions: [
      {
        session_id: "demo",
        status: "complete",
        turn_count: 1,
        complete_turn_count: 1,
        active_turn_count: 0,
        event_count: 3,
        latest_turn: { turn_id: 1, status: "complete", prompt: "fix it" },
      },
    ],
  });
  assert.deepEqual(turnsForSession(result.sessions[0]).map((turn) => turn.turn_id), [1]);
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
