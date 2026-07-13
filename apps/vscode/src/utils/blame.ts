import { BlameEntry } from "../turnal/types";

export function recordedTextMatches(currentText: string, entries: readonly BlameEntry[]): boolean {
  const recorded = entries.map((entry) => entry.text).join("\n");
  const current = currentText.replace(/\r\n/g, "\n").replace(/\n$/, "");
  return current === recorded;
}
