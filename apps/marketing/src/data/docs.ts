// Keep this reference aligned with internal/cli/*.go and internal/config/config.go.
// It intentionally documents public Cobra commands only; hook/checkpoint plumbing
// commands are hidden implementation details.

export interface DocsNavItem {
  id: string;
  label: string;
}

export interface DocsNavGroup {
  label: string;
  items: DocsNavItem[];
}

export interface CommandFlag {
  name: string;
  description: string;
}

export interface CommandReference {
  id: string;
  command: string;
  summary: string;
  usage: string;
  flags?: CommandFlag[];
  notes?: string[];
}

export interface ConfigReference {
  key: string;
  type: string;
  defaultValue: string;
  description: string;
}

export interface EnvironmentReference {
  name: string;
  description: string;
}

export const docsNav: DocsNavGroup[] = [
  {
    label: 'Start',
    items: [
      { id: 'overview', label: 'Overview' },
      { id: 'installation', label: 'Installation' },
      { id: 'quickstart', label: 'Quickstart' },
    ],
  },
  {
    label: 'Understand',
    items: [
      { id: 'mental-model', label: 'Mental model' },
      { id: 'capture-boundaries', label: 'Capture boundaries' },
      { id: 'agents', label: 'Agent integrations' },
    ],
  },
  {
    label: 'Inspect',
    items: [
      { id: 'history', label: 'History and turns' },
      { id: 'search', label: 'Search' },
      { id: 'blame', label: 'Prompt-aware blame' },
    ],
  },
  {
    label: 'Recover',
    items: [
      { id: 'rollback', label: 'Rollback' },
      { id: 'replay', label: 'Replay worktrees' },
    ],
  },
  {
    label: 'Operate',
    items: [
      { id: 'worktrees', label: 'Git worktrees' },
      { id: 'merge', label: 'Merge stores' },
      { id: 'configuration', label: 'Configuration' },
      { id: 'privacy', label: 'Privacy and storage' },
      { id: 'retention', label: 'Retention' },
    ],
  },
  {
    label: 'Reference',
    items: [
      { id: 'commands', label: 'Command reference' },
      { id: 'troubleshooting', label: 'Troubleshooting' },
    ],
  },
];

export const commandReference: CommandReference[] = [
  {
    id: 'cmd-init',
    command: 'turnal init',
    summary: 'Create or attach the Turnal store for the current directory and configure agent hooks.',
    usage: 'turnal init [--agent auto|claude|codex|all|none] [--skip-hooks]\n            [--git-sync] [--store PATH]',
    flags: [
      { name: '--agent VALUE', description: 'Select auto, claude, codex, all, or none. Default: auto.' },
      { name: '--skip-hooks', description: 'Initialize storage without changing agent hook configuration.' },
      { name: '--git-sync', description: 'Capture workspace Git state for future workspace-git rollbacks.' },
      { name: '--store PATH', description: 'Use or create an explicit physical .turnal store.' },
    ],
    notes: [
      'Run this from the directory that should become the workspace root.',
      'By default Turnal ensures workspace Git exists, adds .turnal/ to .gitignore, and installs detected hooks.',
    ],
  },
  {
    id: 'cmd-status',
    command: 'turnal status',
    summary: 'Inspect storage, identities, hidden Git, hook health, integrity, and pending journals.',
    usage: 'turnal status',
    notes: ['Returns a non-zero status when the workspace needs attention.'],
  },
  {
    id: 'cmd-sessions',
    command: 'turnal sessions',
    summary: 'List recorded sessions, adapters, turn counts, activity, latest prompt, and head checkpoint.',
    usage: 'turnal sessions [--json]',
    flags: [{ name: '--json', description: 'Emit structured JSON instead of formatted text.' }],
  },
  {
    id: 'cmd-log',
    command: 'turnal log',
    summary: 'Render checkpoint history across one or more agent sessions.',
    usage: 'turnal log [--session ID] [--limit N] [--transcript] [--verbose]\n           [--worktree ID | --all-worktrees] [--stream ID]\n           [--index | --durable] [--no-pager]',
    flags: [
      { name: '--session ID', description: 'Restrict output to one provider session.' },
      { name: '-n, --limit N', description: 'Maximum turns per session; 0 shows all. Default: 0.' },
      { name: '--transcript', description: 'Show normalized human, agent, and tool-call text.' },
      { name: '--verbose', description: 'Show full refs, checkpoint IDs, event counts, and per-file statistics.' },
      { name: '--worktree ID', description: 'Select one attached worktree; current is the default.' },
      { name: '--all-worktrees', description: 'Show every attached worktree in the store.' },
      { name: '--stream ID', description: 'Select one durable event stream.' },
      { name: '--index', description: 'Read the disposable index when available; falls back to durable data.' },
      { name: '--durable', description: 'Explicitly read event logs and checkpoint refs, which is the default path.' },
      { name: '--no-pager', description: 'Write directly even when the output is taller than the terminal.' },
    ],
    notes: ['The alias turnal graph is equivalent to turnal log.'],
  },
  {
    id: 'cmd-show',
    command: 'turnal show',
    summary: 'Show normalized events and checkpoint metadata for one turn.',
    usage: 'turnal show [latest|TURN|SESSION:TURN|SESSION:latest]\n            [--raw] [--transcript] [--full] [--json]',
    flags: [
      { name: '--raw', description: 'Include referenced raw adapter hook records.' },
      { name: '--transcript', description: 'Read matching text from the provider transcript path.' },
      { name: '--full', description: 'Enable both --raw and --transcript.' },
      { name: '--json', description: 'Emit the complete structured turn object.' },
    ],
    notes: [
      'No target means latest.',
      'A bare turn number works only when it is unambiguous across sessions in the current worktree.',
    ],
  },
  {
    id: 'cmd-diff',
    command: 'turnal diff',
    summary: 'Print the patch between the pre and post checkpoints of one turn.',
    usage: 'turnal diff SESSION:TURN',
    notes: ['Diffs come from hidden Git snapshots, not provider-reported file changes.'],
  },
  {
    id: 'cmd-blame',
    command: 'turnal blame',
    summary: 'Attribute lines to the completed turn and prompt that last changed them.',
    usage: 'turnal blame PATH[:LINE] [--session ID] [--verbose] [--json]',
    flags: [
      { name: '--session ID', description: 'Restrict replay history and the latest checkpoint to one session.' },
      { name: '--verbose', description: 'Include prompt, tools, checkpoint ID, and origin metadata.' },
      { name: '--json', description: 'Emit structured blame results.' },
    ],
    notes: [
      'Only completed pre/post turn pairs participate.',
      'Uncheckpointed workspace edits are not considered; binary files are not supported.',
    ],
  },
  {
    id: 'cmd-reindex',
    command: 'turnal reindex',
    summary: 'Rebuild the disposable SQLite index from event logs and checkpoint refs.',
    usage: 'turnal reindex [--quiet]',
    flags: [{ name: '--quiet', description: 'Suppress the rebuild summary.' }],
  },
  {
    id: 'cmd-search',
    command: 'turnal search',
    summary: 'Search indexed prompts, replies, tools, paths, and normalized event text.',
    usage: 'turnal search QUERY [--session ID] [--all-worktrees]\n              [-n N] [--json]',
    flags: [
      { name: '--session ID', description: 'Restrict results to one session.' },
      { name: '--all-worktrees', description: 'Search attached and imported worktrees instead of only the current one.' },
      { name: '-n, --limit N', description: 'Maximum results; 0 shows all. Default: 20.' },
      { name: '--json', description: 'Emit structured ranked results.' },
    ],
    notes: ['Run turnal reindex after new activity. Every whitespace-separated query term must match.'],
  },
  {
    id: 'cmd-rollback',
    command: 'turnal rollback',
    summary: 'Restore the source workspace to a selected checkpoint with a safety snapshot.',
    usage: 'turnal rollback --to SESSION:TURN[:pre|post] [--dry-run]\n                [--workspace-git] [--from-worktree ID]',
    flags: [
      { name: '--to TARGET', description: 'Select a turn checkpoint, chk_ checkpoint ID, or Git SHA prefix.' },
      { name: '--dry-run', description: 'Resolve the target and print planned changes without writing.' },
      { name: '--workspace-git', description: 'Restore captured HEAD, index, tracked changes, and untracked files.' },
      { name: '--from-worktree ID', description: 'Select a cross-worktree checkpoint when the target is elsewhere or ambiguous.' },
    ],
    notes: [
      'Omitting the phase selects post. Use :pre to return to the state before that turn.',
      'Workspace-git mode requires git_sync.enabled during the target capture.',
    ],
  },
  {
    id: 'cmd-replay',
    command: 'turnal replay',
    summary: 'Materialize checkpoints in an isolated directory without changing the source workspace.',
    usage: 'turnal replay checkout SESSION[:TURN[:pre|post]] [--path PATH]\nturnal replay checkout SESSION:START..END\nturnal replay next | prev | goto TARGET | diff | show | keep | stop | list',
    flags: [
      { name: '--path PATH', description: 'Choose the replay directory instead of the managed default.' },
      { name: '--worktree PATH', description: 'Alias for --path; the two flags cannot be combined.' },
      { name: 'replay diff --next', description: 'Compare the current checkpoint with the next checkpoint.' },
      { name: 'replay diff --workspace', description: 'Compare the current checkpoint with the live source workspace.' },
      { name: 'replay keep [PATH]', description: 'Keep the managed replay directory or copy the current state to an empty path.' },
      { name: 'replay remove [ID|PATH]', description: 'Remove a selected replay session and its directory.' },
    ],
    notes: ['Stopping removes the managed directory unless turnal replay keep marked it to be preserved.'],
  },
  {
    id: 'cmd-run',
    command: 'turnal run',
    summary: 'Wrap a Codex process with independent pre/post safety checkpoints.',
    usage: 'turnal run [--quiet] [--skip-hook-install]\n           [--bypass-hook-trust] -- codex [CODEX_ARGS...]',
    flags: [
      { name: '--quiet', description: 'Suppress wrapper status messages.' },
      { name: '--skip-hook-install', description: 'Do not update .codex/config.toml before launch.' },
      { name: '--bypass-hook-trust', description: 'Pass --dangerously-bypass-hook-trust to Codex for this invocation.' },
    ],
    notes: [
      'The wrapper currently supports Codex only.',
      'Wrapper checkpoints still exist if hooks emit no prompt/tool/assistant payloads.',
    ],
  },
  {
    id: 'cmd-worktree',
    command: 'turnal worktree',
    summary: 'Inspect and repair the Git worktrees attached to one physical Turnal store.',
    usage: 'turnal worktree list\nturnal worktree attach --store PATH_TO_DOT_TURNAL\nturnal worktree repair',
    notes: [
      'list marks the current worktree and identifies the primary worktree.',
      'repair refreshes the worktree binding and user-state registry.',
    ],
  },
  {
    id: 'cmd-merge',
    command: 'turnal merge',
    summary: 'Import immutable event streams and private refs from another Turnal store.',
    usage: 'turnal merge PATH [--dry-run] [--adopt-source-as-current-repo]\nturnal merge --recover\nturnal merge --abort',
    flags: [
      { name: '--dry-run', description: 'Validate and report the import without changing the destination.' },
      { name: '--adopt-source-as-current-repo', description: 'Assert that a differing repo ID represents the same logical project.' },
      { name: '--recover', description: 'Resume the single pending import journal.' },
      { name: '--abort', description: 'Remove staging data for the single pending import journal.' },
    ],
    notes: ['A successful merge rebuilds the destination index; durable history remains imported if that rebuild fails.'],
  },
  {
    id: 'cmd-session-drop',
    command: 'turnal session drop',
    summary: 'Delete one session event log, stream metadata, temporary state, and related private refs.',
    usage: 'turnal session drop SESSION [--dry-run]',
    flags: [{ name: '--dry-run', description: 'Report the refs and files that would be deleted.' }],
    notes: ['Run turnal reindex afterward; object bytes remain until hidden Git GC.'],
  },
  {
    id: 'cmd-retention',
    command: 'turnal retention prune',
    summary: 'Delete private refs that no durable event, journal, active turn, or import manifest references.',
    usage: 'turnal retention prune [--dry-run]',
    flags: [{ name: '--dry-run', description: 'List the count without deleting refs.' }],
  },
  {
    id: 'cmd-maintenance',
    command: 'turnal maintenance gc',
    summary: 'Expire hidden-Git reflogs and immediately prune unreachable objects.',
    usage: 'turnal maintenance gc [--dry-run]',
    flags: [{ name: '--dry-run', description: 'Print the GC policy without invoking Git.' }],
    notes: ['Run only after reviewing session-drop and retention-prune results.'],
  },
  {
    id: 'cmd-store-rekey',
    command: 'turnal store rekey',
    summary: 'Give a copied .turnal store new store, worktree, and producer identities.',
    usage: 'turnal store rekey --confirm',
    flags: [{ name: '--confirm', description: 'Required acknowledgement that this is a copied store.' }],
    notes: ['Existing events and checkpoint commits are not rewritten.'],
  },
  {
    id: 'cmd-destroy',
    command: 'turnal destroy',
    summary: 'Remove Turnal metadata and optionally uninstall Turnal-owned hook commands.',
    usage: 'turnal destroy [--dry-run] [--remove-hooks]\n               [--agent auto|claude|codex|all|none]',
    flags: [
      { name: '--dry-run', description: 'Show metadata and hook changes without deleting them.' },
      { name: '--remove-hooks', description: 'Remove Turnal commands from supported agent hook configs.' },
      { name: '--agent VALUE', description: 'Limit hook removal to selected adapters. Default: auto.' },
    ],
    notes: ['Workspace source files are not deleted. Start with --dry-run.'],
  },
  {
    id: 'cmd-version',
    command: 'turnal version',
    summary: 'Print version, release channel, build commit, and install source.',
    usage: 'turnal version [--json]',
    flags: [{ name: '--json', description: 'Emit structured build metadata.' }],
  },
  {
    id: 'cmd-upgrade',
    command: 'turnal upgrade',
    summary: 'Check or install the newest release while preserving the current channel by default.',
    usage: 'turnal upgrade [--check [--exit-code]] [--dry-run] [--json]\n               [--stable | --nightly] [--yes]',
    flags: [
      { name: '--check', description: 'Check availability without installing.' },
      { name: '--exit-code', description: 'With --check, exit 3 when an update is available.' },
      { name: '--dry-run', description: 'Show the selected target and action without installing.' },
      { name: '--stable / --nightly', description: 'Switch release channel.' },
      { name: '--yes', description: 'Confirm a channel switch or downgrade non-interactively.' },
      { name: '--json', description: 'Emit the upgrade plan as JSON.' },
    ],
    notes: ['The alias turnal update is equivalent to turnal upgrade.'],
  },
  {
    id: 'cmd-completion',
    command: 'turnal completion',
    summary: 'Generate shell completion scripts.',
    usage: 'turnal completion bash|zsh|fish|powershell',
  },
];

export const configReference: ConfigReference[] = [
  { key: 'version', type: 'integer', defaultValue: '1', description: 'Configuration schema version. Only version 1 is accepted.' },
  { key: 'init.agent', type: 'enum', defaultValue: '"auto"', description: 'Hook target: auto, claude, codex, all, or none.' },
  { key: 'init.install_hooks', type: 'boolean', defaultValue: 'true', description: 'Install or refresh agent hooks during turnal init.' },
  { key: 'run.install_hooks', type: 'boolean', defaultValue: 'true', description: 'Refresh Codex hooks before turnal run launches Codex.' },
  { key: 'run.quiet', type: 'boolean', defaultValue: 'false', description: 'Suppress Turnal wrapper status messages.' },
  { key: 'run.bypass_hook_trust', type: 'boolean', defaultValue: 'false', description: 'Pass Codex the dangerous hook-trust bypass flag.' },
  { key: 'hooks.command', type: 'string', defaultValue: '"turnal"', description: 'Executable prefix written into Claude Code and Codex hooks.' },
  { key: 'bootstrap.init_workspace_git', type: 'boolean', defaultValue: 'true', description: 'Run git init when the workspace is not already inside Git.' },
  { key: 'bootstrap.update_gitignore', type: 'boolean', defaultValue: 'true', description: 'Ensure .turnal/ appears in the workspace .gitignore.' },
  { key: 'git_sync.enabled', type: 'boolean', defaultValue: 'false', description: 'Capture HEAD, index, tracked patches, and untracked files alongside future checkpoints.' },
  { key: 'rollback.mode', type: 'enum', defaultValue: '"checkpoint"', description: 'Default rollback engine: checkpoint or workspace-git.' },
  { key: 'secrets.store_prompts', type: 'boolean', defaultValue: 'true', description: 'Store normalized prompt and assistant text in Turnal logs.' },
  { key: 'secrets.store_tool_io', type: 'boolean', defaultValue: 'true', description: 'Store tool inputs and outputs in Turnal logs.' },
  { key: 'secrets.snapshot_deny_globs', type: 'string[]', defaultValue: '[".env", ".env.*", "**/.env", "**/.env.*", "**/credentials.*"]', description: 'Paths excluded from new snapshots and protected during restore.' },
];

export const environmentReference: EnvironmentReference[] = [
  { name: 'TURNAL_CONFIG', description: 'Absolute or relative path to use instead of the OS user config path.' },
  { name: 'TURNAL_HOOK_COMMAND', description: 'Override hooks.command after file configuration is loaded.' },
  { name: 'TURNAL_STATE_DIR', description: 'Absolute directory for the cross-worktree store registry.' },
  { name: 'XDG_STATE_HOME', description: 'Base state directory when TURNAL_STATE_DIR is unset on XDG systems.' },
  { name: 'TURNAL_NO_UPDATE_CHECK', description: 'Set to a truthy value to disable interactive npm update notices.' },
  { name: 'TURNAL_NPM_CACHE', description: 'Override the fallback binary cache used by the npm launcher.' },
  { name: 'PAGER', description: 'Pager for long turnal log output. Set to cat or pass --no-pager to disable paging.' },
  { name: 'CLAUDE_CONFIG_DIR', description: 'Allowed Claude transcript root used by turnal show --transcript.' },
  { name: 'CODEX_HOME', description: 'Allowed Codex transcript root used by turnal show --transcript.' },
];
