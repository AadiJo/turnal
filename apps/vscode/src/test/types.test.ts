import assert from "node:assert/strict";
import test from "node:test";
import { parseBlameResult, parseRollbackPreview, parseSessionsResult, turnsForSession } from "../turnal/types";

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
});
