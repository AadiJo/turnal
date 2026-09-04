/** Shared display helpers. Kept separate from components so the formatting
 * rules for time, ids, and adapter names have one home. */

import type { UsageSummary } from "./types";

export function cx(...values: Array<string | false | null | undefined>) {
  return values.filter(Boolean).join(" ");
}

export function shortID(value?: string, length = 8) {
  if (!value) return "unavailable";
  return value.length <= length ? value : value.slice(0, length);
}

/** Go marshals an unset time.Time as year one, so an absent timestamp arrives as
 * a real-looking string. Treat anything before 1971 as unset. */
export function isRealTime(value?: string): boolean {
  if (!value) return false;
  const parsed = new Date(value).getTime();
  return Number.isFinite(parsed) && parsed > 31536000000;
}

export function displayTime(value?: string) {
  if (!isRealTime(value)) return "No timestamp";
  const date = new Date(value!);
  const sameDay = date.toDateString() === new Date().toDateString();
  return new Intl.DateTimeFormat(undefined, {
    ...(sameDay ? {} : { month: "short", day: "numeric" }),
    hour: "numeric",
    minute: "2-digit",
  }).format(date);
}

/** Compact relative time for dense rows: 4m, 3h, 2d. A project with no recorded
 * activity has a zero timestamp, which must read as absent rather than as an age
 * measured from year one. */
export function shortAge(value?: string) {
  if (!isRealTime(value)) return "";
  const seconds = Math.round((Date.now() - new Date(value!).getTime()) / 1000);
  if (seconds < 60) return "now";
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h`;
  const days = Math.round(hours / 24);
  if (days < 30) return `${days}d`;
  return `${Math.round(days / 30)}mo`;
}

export function duration(start?: string, end?: string) {
  if (!start || !end) return "In progress";
  const seconds = Math.max(0, Math.round((new Date(end).getTime() - new Date(start).getTime()) / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const remainder = seconds % 60;
  return remainder ? `${minutes}m ${remainder}s` : `${minutes}m`;
}

export function cleanAdapter(value?: string) {
  if (!value) return "agent";
  const lower = value.toLowerCase();
  if (lower.includes("claude")) return "claude";
  if (lower.includes("codex")) return "codex";
  if (lower.includes("manual")) return "manual";
  return value;
}

export function initials(value?: string) {
  const label = value?.trim() || "?";
  return label.slice(0, 2).toLowerCase();
}

export function totalTokens(usage: UsageSummary) {
  return (usage.input_tokens ?? 0) + (usage.cache_read_tokens ?? 0) +
    (usage.cache_write_tokens ?? 0) + (usage.output_tokens ?? 0);
}

export function tokenCount(value = 0) {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}k`;
  return String(value);
}

export function estimatedCost(micros = 0, pricedTokens = 0, totalTokens = 0) {
  return new Intl.NumberFormat(undefined, {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: 2,
    maximumFractionDigits: micros < 1_000_000 ? 3 : 2,
  }).format(micros / 1_000_000) + (pricedTokens < totalTokens ? "+" : "");
}

/** True when an agent may still be working, which is the one state that earns
 * the recording color. */
export function isActive(status?: string) {
  return status === "active";
}
