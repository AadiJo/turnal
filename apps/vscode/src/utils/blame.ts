import { BlameEntry, BlameOrigin } from "../turnal/types";
import { truncate, turnTitle } from "./format";

export function recordedTextMatches(currentText: string, entries: readonly BlameEntry[]): boolean {
  const recorded = entries.map((entry) => entry.text).join("\n");
  const current = currentText.replace(/\r\n/g, "\n").replace(/\n$/, "");
  return current === recorded;
}

interface VersionedDocument {
  readonly version: number;
  getText(): string;
}

/** Reuses a whole-file comparison until the document or recorded entries change.
 * Weak keys allow closed editor documents to be collected. */
export class RecordedTextMatcher {
  private readonly matches = new WeakMap<VersionedDocument, {
    version: number;
    entries: readonly BlameEntry[];
    matches: boolean;
  }>();

  matchesDocument(document: VersionedDocument, entries: readonly BlameEntry[]): boolean {
    const cached = this.matches.get(document);
    if (cached?.version === document.version && cached.entries === entries) {
      return cached.matches;
    }
    const matches = recordedTextMatches(document.getText(), entries);
    this.matches.set(document, { version: document.version, entries, matches });
    return matches;
  }
}

export function blameTitle(origin: BlameOrigin, maxLength = 64): string {
  if (origin.intent?.redacted) {
    return truncate("agent intent redacted", maxLength);
  }
  if (origin.intent?.problem.trim()) {
    return truncate(origin.intent.problem, maxLength);
  }
  if (origin.kind === "turn") {
    return truncate("no agent intent recorded", maxLength);
  }
  if (origin.kind === "ambiguous") {
    return truncate("intent attribution unavailable", maxLength);
  }
  if (origin.kind === "concurrent") {
    return truncate("concurrent agent changes", maxLength);
  }
  return turnTitle(origin.prompt, origin.turn_id ?? 0, maxLength);
}

export function intentConfidence(origin: BlameOrigin): string {
  const intent = origin.intent;
  if (!intent) {
    if (origin.kind === "ambiguous") {
      return "Unavailable · no recorded intent could be safely tied to this change";
    }
    if (origin.kind === "concurrent") {
      return "Unavailable · concurrent agent turns overlapped";
    }
    return "Unavailable · no agent intent recorded";
  }
  switch (intent.status) {
    case "captured":
      return `${intent.confidence} · stated before edit`;
    case "late":
      return `${intent.confidence} · stated after edit`;
    case "out_of_scope":
      return `${intent.confidence} · outside stated scope`;
    case "late_out_of_scope":
      return `${intent.confidence} · stated after edit · outside stated scope`;
    case "redacted":
      return intent.timing === "after"
        ? `${intent.confidence} · intent redacted · stated after edit`
        : `${intent.confidence} · intent redacted`;
    default:
      return assertNever(intent.status);
  }
}

function assertNever(value: never): never {
  throw new Error(`Unsupported intent status: ${value}`);
}
