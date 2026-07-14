import assert from "node:assert/strict";
import test from "node:test";
import { displayAgent, relativeTime, truncate, turnTitle } from "../utils/format";

test("formats compact native labels", () => {
  assert.equal(displayAgent("claude-code"), "Claude");
  assert.equal(truncate("  fix   the pagination edge case  ", 18), "fix the paginatio…");
  assert.equal(turnTitle(undefined, 4), "Turn 4");
});

test("formats relative activity times", () => {
  const now = Date.parse("2026-07-13T12:00:00Z");
  assert.equal(relativeTime("2026-07-13T11:45:00Z", now), "15 minutes ago");
});
