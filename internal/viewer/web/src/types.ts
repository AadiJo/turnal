export type Project = {
  store_id: string;
  repo_id?: string;
  name: string;
  root: string;
  branch?: string;
  /** False when the store directory is gone. The project stays listed because
   * its recorded history outlives the working tree. */
  present: boolean;
  index_state?: "healthy" | "stale" | "missing" | "unavailable";
  history_state?: "ready" | "attention";
  session_count: number;
  turn_count: number;
  additions: number;
  deletions: number;
  last_activity?: string;
  last_prompt?: string;
  last_adapter?: string;
  added_at?: string;
  worktrees?: Array<{ root: string; git_dir?: string; last_seen_at?: string }>;
};

export type ActivityItem = {
  store_id: string;
  project_name: string;
  session_key: string;
  session_id: string;
  title?: string;
  adapter?: string;
  model?: string;
  branch?: string;
  status?: string;
  turn_count: number;
  file_count: number;
  additions: number;
  deletions: number;
  started_at?: string;
  finished_at?: string;
};

export type ActivityPage = {
  items: ActivityItem[];
  truncated: boolean;
};

export type ViewerIndex = {
  projects: Project[];
  read_only: boolean;
  network_silent: boolean;
  viewer_started_at: string;
  current_store_id?: string;
};

export type AddProjectRequest = {
  directory: string;
  agent?: string;
  update_gitignore: boolean;
  git_sync: boolean;
};

export type AddProjectResult = {
  store_id: string;
  root: string;
  attached: boolean;
  gitignore_updated: boolean;
  hooks?: string[];
  warning?: string;
};

export type Workspace = {
  name: string;
  root: string;
  repo_id: string;
  store_id: string;
  worktree_id: string;
  session_count: number;
  turn_count: number;
  index_state: "healthy" | "stale" | "missing" | "unavailable";
  history_state: "ready" | "attention";
  problems?: string[];
  last_activity?: string;
  read_only: boolean;
  network_silent: boolean;
  viewer_started_at: string;
};

export type FileChange = {
  path: string;
  additions: number;
  deletions: number;
  binary: boolean;
};

export type SessionSummary = {
  key: string;
  id: string;
  stream_id: string;
  worktree_id?: string;
  adapter?: string;
  model?: string;
  branch?: string;
  started_at?: string;
  finished_at?: string;
  event_count: number;
  turn_count: number;
  complete_turns: number;
  error_count: number;
  file_count: number;
  additions: number;
  deletions: number;
  status: "complete" | "active" | "attention";
  prompt_preview?: string;
};

export type ManualSave = {
  id: string;
  message?: string;
  time?: string;
  warnings?: string[];
};

export type TurnSummary = {
  key: string;
  id: number;
  status: "complete" | "active" | "attention";
  started_at?: string;
  finished_at?: string;
  adapter?: string;
  prompt?: string;
  assistant?: string;
  tool_names?: string[];
  event_count: number;
  error_count: number;
  files?: FileChange[];
  additions: number;
  deletions: number;
  pre_commit?: string;
  post_commit?: string;
  checkpointed: boolean;
};

export type SessionTurns = {
  session: SessionSummary;
  turns: TurnSummary[];
};

export type TurnEvent = {
  sequence: number;
  type: string;
  kind:
    | "prompt"
    | "assistant"
    | "tool"
    | "result"
    | "checkpoint"
    | "error"
    | "system";
  title: string;
  body?: string;
  tool_name?: string;
  time: string;
  sensitive: boolean;
};

export type TurnDetail = TurnSummary & {
  session_id: string;
  stream_id: string;
  events: TurnEvent[];
  warnings?: string[];
  truncated: boolean;
  identity: Record<string, string>;
};

export type DiffSummary = {
  turn_key: string;
  files: FileChange[];
  additions: number;
  deletions: number;
  binary_files: number;
  pre_commit: string;
  post_commit: string;
  truth_source: string;
};

export type FilePatch = {
  path: string;
  patch: string;
  truncated: boolean;
  byte_count: number;
  line_count: number;
  limit_bytes: number;
  limit_lines: number;
};

export type BlameLine = {
  line: number;
  text: string;
  kind: string;
  turn_key?: string;
  turn_id?: number;
  session_id?: string;
  adapter?: string;
  prompt?: string;
  tool_names?: string[];
  time?: string;
};

export type Blame = {
  path: string;
  latest_commit: string;
  latest_time: string;
  complete_turns: number;
  lines: BlameLine[];
  warnings?: string[];
  truncated: boolean;
  truth_source: string;
};

/** The file list of one checkpoint. This is a snapshot, not the working tree:
 * files the snapshot policy excludes never appear. */
export type Tree = {
  checkpoint_id: string;
  checkpoint_time: string;
  commit: string;
  files: TreeEntry[];
  truncated: boolean;
  limit_entries: number;
  truth_source: string;
};

export type TreeEntry = {
  path: string;
  mode: string;
  /** The last recorded turn that changed this file, absent when no turn has. */
  turn_key?: string;
  turn_id?: number;
  prompt?: string;
  adapter?: string;
  changed_at?: string;
};

export type FileContent = {
  path: string;
  checkpoint_id: string;
  checkpoint_time: string;
  commit: string;
  content: string;
  binary: boolean;
  byte_count: number;
  line_count: number;
  truncated: boolean;
  limit_bytes: number;
  limit_lines: number;
  truth_source: string;
};

export type ViewerError = {
  error: { code: string; message: string };
};
