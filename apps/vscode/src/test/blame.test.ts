import assert from "node:assert/strict";
import test from "node:test";
import { BlameEntry } from "../turnal/types";
import { recordedTextMatches } from "../utils/blame";

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
