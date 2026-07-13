export function truncate(value: string, maxLength: number): string {
  const collapsed = value.replace(/\s+/g, " ").trim();
  if (collapsed.length <= maxLength) {
    return collapsed;
  }
  return `${collapsed.slice(0, Math.max(0, maxLength - 1)).trimEnd()}…`;
}

export function relativeTime(value: string | undefined, now = Date.now()): string {
  if (!value) {
    return "unknown time";
  }
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp)) {
    return "unknown time";
  }
  const seconds = Math.round((timestamp - now) / 1000);
  const absolute = Math.abs(seconds);
  const formatter = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
  if (absolute < 60) {
    return formatter.format(seconds, "second");
  }
  const minutes = Math.round(seconds / 60);
  if (absolute < 60 * 60) {
    return formatter.format(minutes, "minute");
  }
  const hours = Math.round(seconds / (60 * 60));
  if (absolute < 60 * 60 * 24) {
    return formatter.format(hours, "hour");
  }
  const days = Math.round(seconds / (60 * 60 * 24));
  return formatter.format(days, "day");
}

export function displayAgent(adapter?: string): string {
  switch (adapter?.toLowerCase()) {
    case "claude-code":
    case "claude":
      return "Claude";
    case "codex":
      return "Codex";
    default:
      return adapter ? adapter : "Agent";
  }
}

export function turnTitle(prompt: string | undefined, turnId: number, maxLength = 64): string {
  return prompt?.trim() ? truncate(prompt, maxLength) : `Turn ${turnId}`;
}

export function formatTimestamp(value: string | undefined): string {
  if (!value) {
    return "Unknown time";
  }
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) {
    return "Unknown time";
  }
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(date);
}
