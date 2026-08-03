import assert from "node:assert/strict";
import test from "node:test";
import { BlameEntry } from "../turnal/types";
import { blameTitle, intentConfidence, recordedTextMatches } from "../utils/blame";

const entries: BlameEntry[] = [
  { line: 1, text: "const pageSize = 25;", origin: { kind: "turn" } },
  { line: 2, text: "return pageSize;", origin: { kind: "turn" } },
];

test("matches checkpointed text with either line ending", () => {
  assert.equal(recordedTextMatches("const pageSize = 25;\nreturn pageSize;\n", entries), true);
  assert.equal(recordedTextMatches("const pageSize = 25;\r\nreturn pageSize;\r\n", entries), true);
});

test("suppresses attribution when the live file has drifted", () => {
  assert.equal(recordedTextMatches("// inserted\nconst pageSize = 25;\nreturn pageSize;\n", entries), false);
  assert.equal(recordedTextMatches("const pageSize = 50;\nreturn pageSize;\n", entries), false);
});

test("puts the stated problem first and labels recorded limitations", () => {
  const origin = {
    kind: "turn",
    turn_id: 3,
    prompt: "fix flaky retries",
    intent: {
      problem: "retry delay was not reset after success",
      event_seq: 5,
      status: "captured" as const,
      timing: "before" as const,
      confidence: "high" as const,
    },
  };
  assert.equal(blameTitle(origin), "retry delay was not reset after success");
  assert.equal(intentConfidence(origin), "high · stated before edit");
  assert.equal(intentConfidence({
    ...origin,
    intent: { ...origin.intent, status: "late", timing: "after", confidence: "low" },
  }), "low · stated after edit");
  assert.equal(intentConfidence({ ...origin, intent: undefined }), "Unavailable · no agent intent recorded");
});
