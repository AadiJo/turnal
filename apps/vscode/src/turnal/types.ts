export interface SessionsResult {
  total_sessions: number;
  sessions: SessionSummary[];
}

export interface SessionSummary {
  session_id: string;
  status: string;
  adapter?: string;
  model?: string;
  permission_mode?: string;
  turn_count: number;
  complete_turn_count: number;
  active_turn_count: number;
  event_count: number;
  first_activity?: string;
  last_activity?: string;
  head?: SessionHead;
  latest_turn?: SessionTurn;
  turns?: SessionTurn[];
  warnings?: string[];
}

export interface SessionHead {
  turn_id: number;
  phase: string;
  commit_sha: string;
  ref: string;
  time?: string;
}

export interface SessionTurn {
  turn_id: number;
  status: string;
  prompt?: string;
  assistant?: string;
  tool_names?: string[];
  first_activity?: string;
  last_activity?: string;
}

export interface BlameResult {
  path: string;
  latest_ref: string;
  latest_commit: string;
  latest_time: string;
  complete_turns: number;
  entries: BlameEntry[];
  warnings?: string[];
}

export interface BlameEntry {
  line: number;
  text: string;
  origin: BlameOrigin;
}

export interface BlameOrigin {
  kind: string;
  session_id?: string;
  turn_id?: number;
  checkpoint_ref?: string;
  commit?: string;
  time?: string;
  adapter?: string;
  prompt?: string;
  tool_names?: string[];
}

export interface RollbackChange {
  action: "added" | "modified" | "deleted" | "mode-changed";
  path: string;
}

export interface RollbackPreview {
  raw: string;
  changes: RollbackChange[];
}

export function parseSessionsResult(value: unknown): SessionsResult {
  const result = record(value, "sessions result");
  const sessions = array(result.sessions, "sessions").map((item, index) => parseSession(item, index));
  return {
    total_sessions: number(result.total_sessions, "total_sessions"),
    sessions,
  };
}

export function parseBlameResult(value: unknown): BlameResult {
  const result = record(value, "blame result");
  return {
    path: string(result.path, "path"),
    latest_ref: string(result.latest_ref, "latest_ref"),
    latest_commit: string(result.latest_commit, "latest_commit"),
    latest_time: string(result.latest_time, "latest_time"),
    complete_turns: number(result.complete_turns, "complete_turns"),
    entries: array(result.entries, "entries").map((item, index) => parseBlameEntry(item, index)),
    warnings: optionalStrings(result.warnings, "warnings"),
  };
}

export function parseRollbackPreview(raw: string): RollbackPreview {
  const changes: RollbackChange[] = [];
  let action: RollbackChange["action"] | undefined;
  for (const line of raw.split(/\r?\n/)) {
    const heading = /^(added|modified|deleted|mode-changed):$/.exec(line);
    if (heading) {
      action = heading[1] as RollbackChange["action"];
      continue;
    }
    if (action && line.startsWith("  ") && line.trim()) {
      changes.push({ action, path: line.trim() });
      continue;
    }
    if (line && !line.startsWith("  ")) {
      action = undefined;
    }
  }
  return { raw, changes };
}

export function turnsForSession(session: SessionSummary): SessionTurn[] {
  if (session.turns) {
    return session.turns;
  }
  return session.latest_turn ? [session.latest_turn] : [];
}

function parseSession(value: unknown, index: number): SessionSummary {
  const item = record(value, `sessions[${index}]`);
  return {
    session_id: string(item.session_id, `sessions[${index}].session_id`),
    status: string(item.status, `sessions[${index}].status`),
    adapter: optionalString(item.adapter, `sessions[${index}].adapter`),
    model: optionalString(item.model, `sessions[${index}].model`),
    permission_mode: optionalString(item.permission_mode, `sessions[${index}].permission_mode`),
    turn_count: number(item.turn_count, `sessions[${index}].turn_count`),
    complete_turn_count: number(item.complete_turn_count, `sessions[${index}].complete_turn_count`),
    active_turn_count: number(item.active_turn_count, `sessions[${index}].active_turn_count`),
    event_count: number(item.event_count, `sessions[${index}].event_count`),
    first_activity: optionalString(item.first_activity, `sessions[${index}].first_activity`),
    last_activity: optionalString(item.last_activity, `sessions[${index}].last_activity`),
    head: item.head === undefined ? undefined : parseHead(item.head, index),
    latest_turn: item.latest_turn === undefined ? undefined : parseTurn(item.latest_turn, `sessions[${index}].latest_turn`),
    turns: item.turns === undefined
      ? undefined
      : array(item.turns, `sessions[${index}].turns`).map((turn, turnIndex) =>
          parseTurn(turn, `sessions[${index}].turns[${turnIndex}]`),
        ),
    warnings: optionalStrings(item.warnings, `sessions[${index}].warnings`),
  };
}

function parseHead(value: unknown, sessionIndex: number): SessionHead {
  const item = record(value, `sessions[${sessionIndex}].head`);
  return {
    turn_id: number(item.turn_id, "head.turn_id"),
    phase: string(item.phase, "head.phase"),
    commit_sha: string(item.commit_sha, "head.commit_sha"),
    ref: string(item.ref, "head.ref"),
    time: optionalString(item.time, "head.time"),
  };
}

function parseTurn(value: unknown, name: string): SessionTurn {
  const item = record(value, name);
  return {
    turn_id: number(item.turn_id, `${name}.turn_id`),
    status: string(item.status, `${name}.status`),
    prompt: optionalString(item.prompt, `${name}.prompt`),
    assistant: optionalString(item.assistant, `${name}.assistant`),
    tool_names: optionalStrings(item.tool_names, `${name}.tool_names`),
    first_activity: optionalString(item.first_activity, `${name}.first_activity`),
    last_activity: optionalString(item.last_activity, `${name}.last_activity`),
  };
}

function parseBlameEntry(value: unknown, index: number): BlameEntry {
  const item = record(value, `entries[${index}]`);
  const origin = record(item.origin, `entries[${index}].origin`);
  return {
    line: number(item.line, `entries[${index}].line`),
    text: string(item.text, `entries[${index}].text`),
    origin: {
      kind: string(origin.kind, `entries[${index}].origin.kind`),
      session_id: optionalString(origin.session_id, "origin.session_id"),
      turn_id: optionalNumber(origin.turn_id, "origin.turn_id"),
      checkpoint_ref: optionalString(origin.checkpoint_ref, "origin.checkpoint_ref"),
      commit: optionalString(origin.commit, "origin.commit"),
      time: optionalString(origin.time, "origin.time"),
      adapter: optionalString(origin.adapter, "origin.adapter"),
      prompt: optionalString(origin.prompt, "origin.prompt"),
      tool_names: optionalStrings(origin.tool_names, "origin.tool_names"),
    },
  };
}

function record(value: unknown, name: string): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new TypeError(`${name} must be an object`);
  }
  return value as Record<string, unknown>;
}

function array(value: unknown, name: string): unknown[] {
  if (!Array.isArray(value)) {
    throw new TypeError(`${name} must be an array`);
  }
  return value;
}

function string(value: unknown, name: string): string {
  if (typeof value !== "string") {
    throw new TypeError(`${name} must be a string`);
  }
  return value;
}

function optionalString(value: unknown, name: string): string | undefined {
  return value === undefined ? undefined : string(value, name);
}

function optionalStrings(value: unknown, name: string): string[] | undefined {
  return value === undefined ? undefined : array(value, name).map((item, index) => string(item, `${name}[${index}]`));
}

function number(value: unknown, name: string): number {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    throw new TypeError(`${name} must be a finite number`);
  }
  return value;
}

function optionalNumber(value: unknown, name: string): number | undefined {
  return value === undefined ? undefined : number(value, name);
}
