import assert from "node:assert/strict";
import test from "node:test";
import { SessionSummary } from "../turnal/types";
import { recentTurns } from "../utils/recentTurns";

test("flattens turns across sessions in newest-first activity order", () => {
  const result = recentTurns([
    session("claude", "2026-07-13T10:00:00Z", [
      { turn_id: 1, status: "complete", last_activity: "2026-07-13T10:05:00Z" },
      { turn_id: 2, status: "complete", last_activity: "2026-07-13T10:20:00Z" },
    ]),
    session("codex", "2026-07-13T10:15:00Z", [
      { turn_id: 1, status: "complete", last_activity: "2026-07-13T10:15:00Z" },
    ]),
  ]);

  assert.deepEqual(
    result.map(({ session, turn }) => `${session.session_id}:${turn.turn_id}`),
    ["claude:2", "codex:1", "claude:1"],
  );
});

test("uses session activity when a turn has no timestamp", () => {
  const result = recentTurns([
    session("older", "2026-07-13T09:00:00Z", [{ turn_id: 1, status: "events-only" }]),
    session("newer", "2026-07-13T11:00:00Z", [{ turn_id: 1, status: "events-only" }]),
  ]);

  assert.deepEqual(result.map(({ session }) => session.session_id), ["newer", "older"]);
});

function session(
  sessionId: string,
  lastActivity: string,
  turns: SessionSummary["turns"],
): SessionSummary {
  return {
    session_id: sessionId,
    status: "complete",
    turn_count: turns?.length ?? 0,
    complete_turn_count: turns?.length ?? 0,
    active_turn_count: 0,
    event_count: 0,
    last_activity: lastActivity,
    turns,
  };
}
