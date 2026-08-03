import { BlameEntry, BlameOrigin } from "../turnal/types";
import { truncate, turnTitle } from "./format";

export function recordedTextMatches(currentText: string, entries: readonly BlameEntry[]): boolean {
  const recorded = entries.map((entry) => entry.text).join("\n");
  const current = currentText.replace(/\r\n/g, "\n").replace(/\n$/, "");
  return current === recorded;
}

export function blameTitle(origin: BlameOrigin, maxLength = 64): string {
  if (origin.intent?.problem.trim()) {
    return truncate(origin.intent.problem, maxLength);
  }
  return turnTitle(origin.prompt, origin.turn_id ?? 0, maxLength);
}

export function intentConfidence(origin: BlameOrigin): string {
  const intent = origin.intent;
  if (!intent) {
    return "Unavailable · no agent intent recorded";
  }
  switch (intent.status) {
    case "late":
      return `${intent.confidence} · stated after edit`;
    case "out_of_scope":
      return `${intent.confidence} · outside stated scope`;
    default:
      return `${intent.confidence} · stated before edit`;
  }
}
