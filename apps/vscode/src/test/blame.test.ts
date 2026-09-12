import assert from "node:assert/strict";
import test from "node:test";
import { BlameEntry } from "../turnal/types";
import { blameTitle, intentConfidence, recordedTextMatches, RecordedTextMatcher } from "../utils/blame";

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
    kind: "turn" as const,
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
  assert.equal(intentConfidence({
    ...origin,
    intent: { ...origin.intent, status: "late_out_of_scope", timing: "after", confidence: "low" },
  }), "low · stated after edit · outside stated scope");
  assert.equal(intentConfidence({
    ...origin,
    intent: { ...origin.intent, status: "redacted", confidence: "low", redacted: true },
  }), "low · intent redacted");
  assert.equal(blameTitle({
    ...origin,
    intent: { ...origin.intent, status: "redacted", confidence: "low", redacted: true },
  }), "agent intent redacted");
  assert.equal(intentConfidence({ ...origin, intent: undefined }), "Unavailable · no agent intent recorded");
  assert.equal(blameTitle({ ...origin, intent: undefined }), "no agent intent recorded");
  assert.equal(blameTitle({ kind: "ambiguous" }), "intent attribution unavailable");
  assert.equal(intentConfidence({ kind: "ambiguous" }), "Unavailable · no recorded intent could be safely tied to this change");
  assert.equal(blameTitle({ kind: "concurrent" }), "concurrent agent changes");
  assert.equal(intentConfidence({ kind: "concurrent" }), "Unavailable · concurrent agent turns overlapped");
});


test("reuses cursor-only comparisons and invalidates edits and refreshed blame", () => {
  const matcher = new RecordedTextMatcher();
  let reads = 0;
  let text = "const pageSize = 25;\nreturn pageSize;\n";
  const document = { version: 1, getText: () => { reads++; return text; } };
  assert.equal(matcher.matchesDocument(document, entries), true);
  assert.equal(matcher.matchesDocument(document, entries), true);
  assert.equal(reads, 1);

  document.version++;
  text = "edited";
  assert.equal(matcher.matchesDocument(document, entries), false);
  assert.equal(matcher.matchesDocument(document, entries), false);
  assert.equal(reads, 2);

  const refreshed: BlameEntry[] = [{ line: 1, text, origin: { kind: "turn" } }];
  assert.equal(matcher.matchesDocument(document, refreshed), true);
  assert.equal(reads, 3);
  const reopened = { version: 1, getText: () => "different text" };
  assert.equal(matcher.matchesDocument(reopened, refreshed), false);
});
