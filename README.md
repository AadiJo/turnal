<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-lockup-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="assets/logo-lockup-light.svg">
    <img src="assets/logo-lockup-light.svg" alt="Turnal" width="360">
  </picture>
</p>

<p align="center">
  A local-first flight recorder for AI coding agents.
</p>

Turnal records what an agent did, why it did it, what the workspace looked like before and after each turn, and how to get back safely.

It combines an append-only activity log with private Git checkpoints. Your project history stays local, normal recording never commits to or modifies your existing `.git/`, and SQLite is only a disposable search index. The optional workspace-Git rollback mode is the explicit exception: it can restore a previously captured HEAD and index.

Turnal should be piloted before company-wide adoption. Pin a version and validate hook compatibility, retention, and rollback behavior on representative repositories; see the [compatibility policy](docs/compatibility.md), [retention semantics](docs/retention.md), and [recovery runbook](docs/recovery.md).

Telemetry networking is disabled. The local-only client foundation and the gates that must pass before any collection can be enabled are documented in the [telemetry policy](docs/telemetry.md).

## What you can do

- Inspect agent sessions, prompts, assistant responses, and tool activity.
- Diff the workspace before and after a specific agent turn.
- Attribute current lines to the turns that last changed them.
- Roll the workspace back with a safety checkpoint created first.
- Search recorded turns without making SQLite the source of truth.
- Replay checkpoints in isolated worktrees.
- Share one Turnal store across linked Git worktrees.

## Requirements

- Git available on `PATH`. Turnal uses Git plumbing for its private checkpoint store.
- Node.js 18 or newer when installing through npm.
- Claude Code or Codex for automatic agent capture. Manual checkpoints are also available.

Turnal does not initialize a Git repository for your project. It works in both Git and non-Git directories.

## Install

```sh
npm install -g @aadijo/turnal
```

The npm package ships prebuilt binaries for macOS, Linux, and Windows on x64 and arm64. Go is only needed when installing from source or using an unsupported npm platform.

From source:

```sh
go install github.com/AadiJo/turnal/cmd/turnal@latest
```

## Quick start

From the root of the project you want to record:

```sh
turnal init
turnal status
```

`turnal init` creates a `.turnal/` store, adds `.turnal/` to `.gitignore`, and configures hooks according to the `--agent` selection. The default `auto` mode detects Claude Code and Codex, falling back to configuring both when neither is discoverable. Initialization does not change your existing `.git/`.

Now use your agent normally. After it has completed a turn:

```sh
# Find recorded sessions and turn identifiers.
turnal sessions

# Read recent history or a transcript.
turnal log
turnal log --transcript

# Inspect and diff one turn.
turnal show <session>:<turn>
turnal diff <session>:<turn>

# See what would be restored, then perform the rollback.
turnal rollback --to <session>:<turn>:pre --dry-run
turnal rollback --to <session>:<turn>:pre
```

Rollback targets default to the post-turn checkpoint when the phase is omitted. Use `:pre` to return to the state before the turn and `:post` to return to the state after it.

### Codex wrapper mode

Turnal can launch Codex with wrapper checkpoints in addition to hook capture:

```sh
turnal run -- codex
```

Wrapper checkpoints remain available even when Codex hook payloads are unavailable. Prompt, tool, and assistant details still depend on Codex hooks.

### Manual turns

For an unsupported agent or a manual workflow:

```sh
turnal turn start --session demo
# Make changes or run the agent.
turnal turn finish --session demo
turnal diff demo:1
```

## How it works

Turnal deliberately keeps four responsibilities separate:

```text
.turnal/log/       append-only, hash-chained agent activity
.turnal/git/       private Git objects and checkpoint refs
.turnal/index/     rebuildable SQLite search and lookup cache
turnal             orchestration, validation, and user-facing commands
```

The event log answers _why did this happen?_ Private Git checkpoints answer _what did the files look like?_ Diffs, rollback plans, replay, and blame are derived from those durable records.

Checkpoint refs live under `refs/agent-vcs/...`. Hidden Git operations use explicit repository paths and scrub inherited `GIT_*` environment variables so they cannot be redirected into the project’s Git repository.

## What gets captured

By default Turnal records:

- User prompts and assistant messages exposed by agent hooks.
- Tool names, inputs, and results exposed by agent hooks.
- Raw adapter payloads, including malformed payloads when they can be preserved.
- Byte-exact contents and executable bits for checkpointed files.
- Symlinks as symlinks, without following their targets.
- User Git context such as branch, HEAD, and dirty state when the workspace is already a Git worktree.

Turnal excludes:

- `.turnal/` and `.git/` metadata at any depth.
- Files ignored by applicable `.gitignore` rules.
- Paths denied by `secrets.snapshot_deny_globs`.

Ignored and secrets-denied files are left untouched during checkpoint rollback. This means the default rollback mode restores the checkpointed project surface, not every byte under the workspace directory.

## Privacy and secrets

Turnal stores history locally under `.turnal/`; it does not upload it. Prompts and tool payloads can contain credentials or proprietary data, so review the policy before recording sensitive work.

Workspace configuration lives at `.turnal/config.toml`. Global defaults live at the platform-specific user configuration path, normally `~/.config/turnal/config.toml` on Linux.

```toml
version = 1

[secrets]
store_prompts = false
store_tool_io = false
snapshot_deny_globs = [
  ".env",
  ".env.*",
  "**/.env",
  "**/.env.*",
  "**/credentials.*",
  "**/*.pem",
]
```

When prompt or tool storage is disabled, Turnal writes an explicit redaction marker instead of the original content. Snapshot deny globs also apply during rollback, including rollback from older checkpoints that may contain a now-denied path.

## Rollback safety

The default checkpoint rollback:

1. Resolves and verifies the target checkpoint.
2. Computes a restore plan.
3. Creates a private safety snapshot of the current workspace.
4. Writes a rollback journal.
5. Restores checkpointed files.
6. Appends a rollback event to durable history.

Always inspect destructive changes first:

```sh
turnal rollback --to <session>:<turn>:pre --dry-run
```

If a rollback is interrupted, `turnal status` reports the journal phase. Once a safety checkpoint exists, rollback errors include its ref and commit. Preserve that information, and do not delete `.turnal/tmp/rollback-journal.json` until you have inspected the workspace and safety checkpoint.

### Restoring workspace Git state

Checkpoint rollback intentionally does not move the project’s branch, HEAD, or index. In an existing Git worktree, Turnal can optionally capture and restore that state:

```sh
turnal init --git-sync

# Later:
turnal rollback --to <session>:<turn>:pre --workspace-git --dry-run
turnal rollback --to <session>:<turn>:pre --workspace-git
```

Git-sync capture requires the workspace to already be a valid Git worktree. It is opt-in because it makes rollback materially more invasive.

## History and inspection

```sh
turnal sessions                         # Session summaries
turnal sessions --json                  # Scriptable session output
turnal log                              # Turn graph for this worktree
turnal log --transcript                 # Prompt/assistant/tool transcript
turnal log --all-worktrees              # Shared-store history
turnal show <session>:<turn>             # Normalized events for one turn
turnal show <session>:<turn> --full      # Include raw records/transcript text
turnal diff <session>:<turn>             # Pre-to-post patch
turnal search "authentication failure"  # Search the SQLite projection
turnal blame src/auth.go:42              # Turn that last changed a line
```

If the disposable index is missing or stale:

```sh
turnal reindex
```

Reindexing rebuilds SQLite from the event logs and private checkpoint refs.

## Replay

Replay opens history in a separate worktree so inspection does not disturb the active workspace:

```sh
turnal replay checkout <session>:<turn>:post
turnal replay show
turnal replay diff
turnal replay prev
turnal replay next
turnal replay stop
```

Use `turnal replay list` to find active replay sessions and `turnal replay --help` for path and retention controls.

## Worktrees and store history

When initialized inside linked Git worktrees, Turnal can use a shared physical store while preserving an independent worktree identity and event stream.

```sh
turnal worktree list
turnal worktree attach --store /path/to/project/.turnal
turnal worktree repair
```

Immutable history from another Turnal store can be verified and imported:

```sh
turnal merge /path/to/source/.turnal --dry-run
turnal merge /path/to/source/.turnal
```

Turnal does not merge or rewrite the project’s Git history.

## Retention and removal

```sh
turnal session drop <session> --dry-run
turnal session drop <session>
turnal retention prune --dry-run
turnal retention prune
turnal maintenance gc
```

Dropping a session removes its durable event records and refs. Git objects are physically reclaimed only after refs are removed and explicit maintenance runs.

To remove Turnal from a workspace:

```sh
turnal destroy --dry-run
turnal destroy --remove-hooks
```

## Configuration

Common workspace options:

```toml
version = 1

[init]
agent = "auto"          # auto | claude | codex | all | none
install_hooks = true

[run]
install_hooks = true
quiet = false
bypass_hook_trust = false

[hooks]
command = "turnal"

[bootstrap]
update_gitignore = true

[git_sync]
enabled = false

[rollback]
mode = "checkpoint"     # checkpoint | workspace-git

[secrets]
store_prompts = true
store_tool_io = true
snapshot_deny_globs = [
  ".env",
  ".env.*",
  "**/.env",
  "**/.env.*",
  "**/credentials.*",
]
```

Environment variables:

- `TURNAL_CONFIG` selects a global configuration file.
- `TURNAL_HOOK_COMMAND` overrides the command installed into agent hooks.
- `TURNAL_NO_UPDATE_CHECK=1` disables interactive update notices.

## Troubleshooting

Start with:

```sh
turnal status
turnal sessions
turnal recovery status
```

- **Hooks need attention:** rerun `turnal init --agent claude`, `--agent codex`, or `--agent all`.
- **No Codex hook payloads:** review Codex hook trust, or use `turnal run -- codex` for wrapper checkpoints.
- **A session is active after an interrupted agent run:** resume the same session so the next prompt can close the stale turn, or finalize it manually with `turnal turn finish --session <session>` after inspecting the workspace.
- **Search index missing or stale:** run `turnal reindex`.
- **Pending store import:** run `turnal merge --recover` or `turnal merge --abort` as directed by `status`.
- **Pending rollback journal:** preserve the reported safety ref and inspect the workspace before taking further action.

## Upgrade

```sh
turnal upgrade
```

`turnal upgrade` preserves the current release channel. Use `turnal upgrade --stable` or `turnal upgrade --nightly` to switch channels explicitly.

For npm installs, Turnal may occasionally print a channel-preserving update notice after interactive commands. Set `TURNAL_NO_UPDATE_CHECK=1` to disable these notices.

## Development

```sh
go test ./...
go vet ./...
go build -o bin/turnal ./cmd/turnal
```

Authenticated provider testing is intentionally excluded from the default suite. Set `TURNAL_LIVE_CODEX_TEST=1` to run the live Codex integration test in a trusted disposable repository.

## Security

Workspace metadata can contain prompts, tool input/output, file history, and raw provider payloads. Review [SECURITY.md](SECURITY.md) before using Turnal with sensitive repositories.

## License

Apache-2.0
