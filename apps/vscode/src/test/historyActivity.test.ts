import assert from "node:assert/strict";
import test from "node:test";
import { SessionRollback, SessionSummary } from "../turnal/types";
import { activitiesForSession, recentActivities } from "../utils/historyActivity";

test("interleaves turns and rollbacks in newest-first activity order", () => {
  const claude = session("claude", "2026-07-13T10:25:00Z", [
    { turn_id: 1, status: "complete", last_activity: "2026-07-13T10:05:00Z" },
    { turn_id: 2, status: "complete", last_activity: "2026-07-13T10:20:00Z" },
  ], [rollback(4, "2026-07-13T10:25:00Z")]);
  const codex = session("codex", "2026-07-13T10:15:00Z", [
    { turn_id: 1, status: "complete", last_activity: "2026-07-13T10:15:00Z" },
  ]);

  assert.deepEqual(
    recentActivities([claude, codex]).map((item) =>
      item.kind === "rollback"
        ? `${item.session.session_id}:rollback:${item.rollback.sequence}`
        : `${item.session.session_id}:turn:${item.turn.turn_id}`,
    ),
    ["claude:rollback:4", "claude:turn:2", "codex:turn:1", "claude:turn:1"],
  );
});

test("uses session activity when a turn has no timestamp", () => {
  const older = session("older", "2026-07-13T09:00:00Z", [{ turn_id: 1, status: "events-only" }]);
  const newer = session("newer", "2026-07-13T11:00:00Z", [{ turn_id: 1, status: "events-only" }]);

  assert.deepEqual(
    recentActivities([older, newer]).map(({ session }) => session.session_id),
    ["newer", "older"],
  );
});

test("orders a rollback independently inside its session", () => {
  const value = session("demo", "2026-07-13T12:00:00Z", [
    { turn_id: 2, status: "complete", last_activity: "2026-07-13T11:00:00Z" },
  ], [rollback(7, "2026-07-13T12:00:00Z")]);

  assert.deepEqual(activitiesForSession(value).map((item) => item.kind), ["rollback", "turn"]);
});

function session(
  sessionId: string,
  lastActivity: string,
  turns: SessionSummary["turns"],
  rollbacks: SessionRollback[] = [],
): SessionSummary {
  return {
    session_id: sessionId,
    status: "complete",
    turn_count: turns?.length ?? 0,
    complete_turn_count: turns?.length ?? 0,
    active_turn_count: 0,
    event_count: 0,
    rollback_count: rollbacks.length,
    last_activity: lastActivity,
    turns,
    rollbacks,
  };
}

function rollback(sequence: number, time: string): SessionRollback {
  return {
    sequence,
    turn_id: 1,
    target: "demo:turn:1:pre",
    phase: "pre",
    mode: "checkpoint",
    time,
    change_summary: { total: 2, added: 0, modified: 1, deleted: 1, mode_changed: 0 },
  };
}
