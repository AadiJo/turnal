export interface SessionsResult {
  schema_version: number;
  total_sessions: number;
  sessions: SessionSummary[];
}

export const SUPPORTED_SESSIONS_SCHEMA_VERSION = 1;

export class TurnalCliCompatibilityError extends Error {
  constructor(readonly actualSchemaVersion?: number) {
    const detail = actualSchemaVersion === undefined
      ? "does not expose the versioned sessions API"
      : `uses unsupported sessions API version ${actualSchemaVersion}`;
    super(`The installed Turnal CLI ${detail}. Update Turnal, then refresh the extension.`);
    this.name = "TurnalCliCompatibilityError";
  }
}

export interface SessionSummary {
  session_id: string;
  parent_session_id?: string;
  parent_tool_use_id?: string;
  status: string;
  adapter?: string;
  model?: string;
  permission_mode?: string;
  turn_count: number;
  complete_turn_count: number;
  active_turn_count: number;
  event_count: number;
  rollback_count: number;
  first_activity?: string;
  last_activity?: string;
  head?: SessionHead;
  latest_turn?: SessionTurn;
  turns?: SessionTurn[];
  rollbacks: SessionRollback[];
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

export interface SessionRollback {
  sequence: number;
  stream_id?: string;
  turn_id?: number;
  target: string;
  phase?: string;
  mode: string;
  time: string;
  change_summary: RollbackChangeSummary;
}

export interface RollbackChangeSummary {
  total: number;
  added: number;
  modified: number;
  deleted: number;
  mode_changed: number;
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

export type BlameOriginKind = "baseline" | "turn" | "ambiguous" | "concurrent";

export interface BlameOrigin {
  kind: BlameOriginKind;
  session_id?: string;
  turn_id?: number;
  checkpoint_ref?: string;
  commit?: string;
  time?: string;
  adapter?: string;
  prompt?: string;
  tool_names?: string[];
  action_tool?: string;
  action_agent_id?: string;
  action_agent_type?: string;
  intent?: BlameIntent;
}

export interface BlameIntent {
  problem: string;
  scope?: string[];
  evidence?: string[];
  event_seq: number;
  status: "captured" | "late" | "out_of_scope" | "late_out_of_scope" | "redacted";
  timing: "before" | "after";
  confidence: "high" | "low";
  agent_id?: string;
  agent_type?: string;
  redacted?: boolean;
}

export interface RollbackChange {
  action: "added" | "modified" | "deleted" | "mode-changed";
  path: string;
}

export interface RollbackPreview {
  raw: string;
  changes: RollbackChange[];
  no_changes: boolean;
}

export interface DiffDocumentsResult {
  kind: "turn" | "rollback";
  session_id: string;
  turn_id: number;
  workspace_tree?: string;
  files: DiffDocument[];
}

export interface DiffDocument {
  status: string;
  path: string;
  old_path?: string;
  additions: number;
  deletions: number;
  binary: boolean;
  truncated: boolean;
  before_exists: boolean;
  after_exists: boolean;
  before_text?: string;
  after_text?: string;
}

export function parseSessionsResult(value: unknown): SessionsResult {
  const result = record(value, "sessions result");
  const schemaVersion = optionalNumber(result.schema_version, "schema_version");
  if (schemaVersion !== SUPPORTED_SESSIONS_SCHEMA_VERSION) {
    throw new TurnalCliCompatibilityError(schemaVersion);
  }
  const sessions = array(result.sessions, "sessions").map((item, index) => parseSession(item, index));
  return {
    schema_version: schemaVersion,
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
  let noChanges = false;
  let action: RollbackChange["action"] | undefined;
  for (const line of raw.split(/\r?\n/)) {
    if (line.trim() === "no changes") {
      noChanges = true;
    }
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
  if (changes.length === 0 && !noChanges) {
    throw new TypeError("Turnal returned an unrecognized rollback preview. No changes were made.");
  }
  return { raw, changes, no_changes: noChanges };
}

export function parseDiffDocumentsResult(value: unknown): DiffDocumentsResult {
  const result = record(value, "diff documents result");
  const kind = string(result.kind, "kind");
  if (kind !== "turn" && kind !== "rollback") {
    throw new TypeError("kind must be turn or rollback");
  }
  return {
    kind,
    session_id: string(result.session_id, "session_id"),
    turn_id: number(result.turn_id, "turn_id"),
    workspace_tree: optionalString(result.workspace_tree, "workspace_tree"),
    files: array(result.files, "files").map((item, index) => parseDiffDocument(item, index)),
  };
}

export function turnsForSession(session: SessionSummary): SessionTurn[] {
  if (session.turns) {
    return session.turns;
  }
  return session.latest_turn ? [session.latest_turn] : [];
}

function parseSession(value: unknown, index: number): SessionSummary {
  const item = record(value, `sessions[${index}]`);
  const rollbacks = array(item.rollbacks, `sessions[${index}].rollbacks`).map((rollback, rollbackIndex) =>
    parseSessionRollback(rollback, `sessions[${index}].rollbacks[${rollbackIndex}]`),
  );
  return {
    session_id: string(item.session_id, `sessions[${index}].session_id`),
    parent_session_id: optionalString(item.parent_session_id, `sessions[${index}].parent_session_id`),
    parent_tool_use_id: optionalString(item.parent_tool_use_id, `sessions[${index}].parent_tool_use_id`),
    status: string(item.status, `sessions[${index}].status`),
    adapter: optionalString(item.adapter, `sessions[${index}].adapter`),
    model: optionalString(item.model, `sessions[${index}].model`),
    permission_mode: optionalString(item.permission_mode, `sessions[${index}].permission_mode`),
    turn_count: number(item.turn_count, `sessions[${index}].turn_count`),
    complete_turn_count: number(item.complete_turn_count, `sessions[${index}].complete_turn_count`),
    active_turn_count: number(item.active_turn_count, `sessions[${index}].active_turn_count`),
    event_count: number(item.event_count, `sessions[${index}].event_count`),
    rollback_count: number(item.rollback_count, `sessions[${index}].rollback_count`),
    first_activity: optionalString(item.first_activity, `sessions[${index}].first_activity`),
    last_activity: optionalString(item.last_activity, `sessions[${index}].last_activity`),
    head: item.head === undefined ? undefined : parseHead(item.head, index),
    latest_turn: item.latest_turn === undefined ? undefined : parseTurn(item.latest_turn, `sessions[${index}].latest_turn`),
    turns: item.turns === undefined
      ? undefined
      : array(item.turns, `sessions[${index}].turns`).map((turn, turnIndex) =>
          parseTurn(turn, `sessions[${index}].turns[${turnIndex}]`),
        ),
    rollbacks,
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

function parseSessionRollback(value: unknown, name: string): SessionRollback {
  const item = record(value, name);
  const summary = record(item.change_summary, `${name}.change_summary`);
  return {
    sequence: number(item.sequence, `${name}.sequence`),
    stream_id: optionalString(item.stream_id, `${name}.stream_id`),
    turn_id: optionalNumber(item.turn_id, `${name}.turn_id`),
    target: string(item.target, `${name}.target`),
    phase: optionalString(item.phase, `${name}.phase`),
    mode: string(item.mode, `${name}.mode`),
    time: string(item.time, `${name}.time`),
    change_summary: {
      total: number(summary.total, `${name}.change_summary.total`),
      added: number(summary.added, `${name}.change_summary.added`),
      modified: number(summary.modified, `${name}.change_summary.modified`),
      deleted: number(summary.deleted, `${name}.change_summary.deleted`),
      mode_changed: number(summary.mode_changed, `${name}.change_summary.mode_changed`),
    },
  };
}

function parseBlameEntry(value: unknown, index: number): BlameEntry {
  const item = record(value, `entries[${index}]`);
  const origin = record(item.origin, `entries[${index}].origin`);
  const kind = string(origin.kind, `entries[${index}].origin.kind`);
  if (kind !== "baseline" && kind !== "turn" && kind !== "ambiguous" && kind !== "concurrent") {
    throw new TypeError(`entries[${index}].origin.kind is not supported`);
  }
  return {
    line: number(item.line, `entries[${index}].line`),
    text: string(item.text, `entries[${index}].text`),
    origin: {
      kind,
      session_id: optionalString(origin.session_id, "origin.session_id"),
      turn_id: optionalNumber(origin.turn_id, "origin.turn_id"),
      checkpoint_ref: optionalString(origin.checkpoint_ref, "origin.checkpoint_ref"),
      commit: optionalString(origin.commit, "origin.commit"),
      time: optionalString(origin.time, "origin.time"),
      adapter: optionalString(origin.adapter, "origin.adapter"),
      prompt: optionalString(origin.prompt, "origin.prompt"),
      tool_names: optionalStrings(origin.tool_names, "origin.tool_names"),
      action_tool: optionalString(origin.action_tool, "origin.action_tool"),
      action_agent_id: optionalString(origin.action_agent_id, "origin.action_agent_id"),
      action_agent_type: optionalString(origin.action_agent_type, "origin.action_agent_type"),
      intent: origin.intent === undefined ? undefined : parseBlameIntent(origin.intent),
    },
  };
}

function parseBlameIntent(value: unknown): BlameIntent {
  const intent = record(value, "origin.intent");
  const status = string(intent.status, "origin.intent.status");
  if (status !== "captured" && status !== "late" && status !== "out_of_scope" && status !== "late_out_of_scope" && status !== "redacted") {
    throw new TypeError("origin.intent.status is not supported");
  }
  const timing = string(intent.timing, "origin.intent.timing");
  if (timing !== "before" && timing !== "after") {
    throw new TypeError("origin.intent.timing must be before or after");
  }
  const confidence = string(intent.confidence, "origin.intent.confidence");
  if (confidence !== "high" && confidence !== "low") {
    throw new TypeError("origin.intent.confidence must be high or low");
  }
  return {
    problem: string(intent.problem, "origin.intent.problem"),
    scope: optionalStrings(intent.scope, "origin.intent.scope"),
    evidence: optionalStrings(intent.evidence, "origin.intent.evidence"),
    event_seq: number(intent.event_seq, "origin.intent.event_seq"),
    status,
    timing,
    confidence,
    agent_id: optionalString(intent.agent_id, "origin.intent.agent_id"),
    agent_type: optionalString(intent.agent_type, "origin.intent.agent_type"),
    redacted: optionalBoolean(intent.redacted, "origin.intent.redacted"),
  };
}

function parseDiffDocument(value: unknown, index: number): DiffDocument {
  const name = `files[${index}]`;
  const item = record(value, name);
  const status = string(item.status, `${name}.status`);
  if (!/^[ACDMRTUXB]$/.test(status)) {
    throw new TypeError(`${name}.status is not a supported Git status`);
  }
  return {
    status,
    path: string(item.path, `${name}.path`),
    old_path: optionalString(item.old_path, `${name}.old_path`),
    additions: optionalNumber(item.additions, `${name}.additions`) ?? 0,
    deletions: optionalNumber(item.deletions, `${name}.deletions`) ?? 0,
    binary: optionalBoolean(item.binary, `${name}.binary`) ?? false,
    truncated: optionalBoolean(item.truncated, `${name}.truncated`) ?? false,
    before_exists: boolean(item.before_exists, `${name}.before_exists`),
    after_exists: boolean(item.after_exists, `${name}.after_exists`),
    before_text: optionalBase64Text(item.before_base64, `${name}.before_base64`),
    after_text: optionalBase64Text(item.after_base64, `${name}.after_base64`),
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

function boolean(value: unknown, name: string): boolean {
  if (typeof value !== "boolean") {
    throw new TypeError(`${name} must be a boolean`);
  }
  return value;
}

function optionalBoolean(value: unknown, name: string): boolean | undefined {
  return value === undefined ? undefined : boolean(value, name);
}

function optionalBase64Text(value: unknown, name: string): string | undefined {
  const encoded = optionalString(value, name);
  if (encoded === undefined) {
    return undefined;
  }
  const normalized = encoded.replace(/=+$/, "");
  const decoded = Buffer.from(encoded, "base64");
  if (decoded.toString("base64").replace(/=+$/, "") !== normalized) {
    throw new TypeError(`${name} must be valid base64`);
  }
  return decoded.toString("utf8");
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
