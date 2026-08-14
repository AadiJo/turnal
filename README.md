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

Turnal records what an agent did, the problem it said it was trying to solve, what the workspace looked like before and after each turn, and how to get back safely.

It combines an append-only activity log with private Git checkpoints. Your project history stays local, normal recording never commits to or modifies your existing `.git/`, and SQLite is only a disposable search index. The optional workspace-Git rollback mode is the explicit exception: it can restore a previously captured HEAD and index.

Turnal should be piloted before company-wide adoption. Pin a version and validate hook compatibility, retention, and rollback behavior on representative repositories; see the [compatibility policy](docs/compatibility.md), [retention semantics](docs/retention.md), and [recovery runbook](docs/recovery.md).

## What you can do

- Inspect agent sessions, prompts, assistant responses, and tool activity.
- Diff the workspace before and after a specific agent turn.
- Attribute current lines to the turns that last changed them.
- Reply to a recorded turn with a note that rides the same durable history.
- Roll the workspace back with a safety checkpoint created first.
- Save an explicit rollback point without committing to the project's Git history.
- Search recorded turns without making SQLite the source of truth.
- Replay checkpoints in isolated worktrees.
- Run repository-defined checks against the live workspace or a recorded checkpoint.
- Promote recorded turns into immutable Cases, compare isolated Attempts, and apply a selected result.
- Share one Turnal store across linked Git worktrees.
- Publish an explicitly approved, privacy-filtered context history through Git for teammate review.

## Requirements

- Git available on `PATH`. Turnal uses Git plumbing for its private checkpoint store.
- Node.js 18 or newer when installing through npm.
- Go 1.26.6 or newer when installing from source or developing Turnal.
- Claude Code, Codex, OpenCode, Gemini CLI, or Copilot CLI for automatic agent capture. `turnal save` also works without an agent session.

Turnal does not initialize a Git repository for your project. It works in both Git and non-Git directories.

## Install

With npm:

```sh
npm install -g @aadijo/turnal
```

Install the standalone binaries without Node.js on macOS or Linux:

```sh
curl --proto '=https' --tlsv1.2 -fsSL \
  https://raw.githubusercontent.com/AadiJo/turnal/main/install.sh | sh
```

On Windows, run the PowerShell installer:

```powershell
irm https://raw.githubusercontent.com/AadiJo/turnal/main/install.ps1 | iex
```

The standalone installers detect x64 or arm64, verify the archive against the SHA-256 checksum published with the same GitHub release, and install `turnal` plus its external adapter executables. The macOS and Linux default is `~/.local/bin`. The Windows default is `%LOCALAPPDATA%\Programs\Turnal\bin`, which the PowerShell installer adds to the user `PATH` without elevation.

Each release provides `turnal_<version>_<platform>_<architecture>.tar.gz` for macOS, Linux, and Windows on x64 and arm64, plus `checksums.txt`. The checksum detects download corruption; it is not an independent signature if the GitHub release itself is compromised. Select a different directory or pin a version with installer arguments:

```sh
curl --proto '=https' --tlsv1.2 -fsSL \
  https://raw.githubusercontent.com/AadiJo/turnal/main/install.sh |
  sh -s -- --version 0.0.4 --install-dir "$HOME/bin"
```

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/AadiJo/turnal/main/install.ps1))) `
  -Version 0.0.4 `
  -InstallDir "$HOME\bin"
```

The installers never invoke `sudo` or request elevation; the selected directory must already be writable. Pass `-NoPathUpdate` to the PowerShell installer to leave the user `PATH` unchanged.

Turnal is supported on Linux, macOS, and Windows on x64 and arm64. The npm package and standalone release archives ship prebuilt binaries for each. Go is only needed when installing from source or using an unsupported npm platform. Isolated `turnal fork` execution is available only on those three platforms. On any other target it fails closed with an explicit error instead of running a child without process containment.

From source:

```sh
go install github.com/AadiJo/turnal/cmd/turnal@latest
```

### VS Code extension

The first-party VS Code extension is under [`apps/vscode`](apps/vscode). It provides current-line AI blame, a native sessions and turns sidebar, read-only turn diffs, and previewed rollback while delegating history and restore operations to the installed `turnal` CLI. See the [extension README](apps/vscode/README.md) for development and local packaging instructions.

## Quick start

From the root of the project you want to record:

```sh
turnal init --agent all
turnal status
turnal status --probe-agent-capture
```

`turnal init --agent all` creates a `.turnal/` store, adds `.turnal/` to `.gitignore`, and configures hooks for all supported agents. Initialization does not change your existing `.git/`.

> [!IMPORTANT]
> **Trust the workspace hooks before using your agent.** For Codex, launch the Codex CLI in this workspace first and approve the Turnal hooks there before using Codex through another surface, such as the desktop app; those surfaces may not show the hook-trust prompt. For Claude Code, trust the workspace when prompted; no separate hook approval is needed.

When Codex hooks are configured, `turnal init` prints this trust reminder as a yellow terminal notice. At the end of an interactive initialization, Turnal also offers to install its project-scoped skills; Enter and `y` accept. The bundled files are stored once under `.turnal/skills/`, then linked into `.agents/skills/` for Codex and `.claude/skills/` for Claude Code according to the initialized agents. On Windows, Turnal prefers directory symlinks and falls back to directory junctions when symlink creation requires Developer Mode or elevation.

Normal status is offline. The explicit capture probe starts Codex app-server only long enough to call `hooks/list`; it does not start a thread or turn, invoke a model, modify workspace files, or change provider trust or configuration. Codex may still update its own local cache and runtime state while app-server starts. The probe also explains the Claude Agent SDK limitation that only the host knows whether it loads project settings.

### Agent skills

Turnal includes three project-scoped agent skills. Accept the installation prompt from `turnal init` to make them available to the initialized agents. Compatible agents can select a skill from its description or invoke it explicitly:

- [`$turnal-inspect-history`](.agents/skills/turnal-inspect-history/SKILL.md) searches recorded work when the prompt gives a lead into earlier attempts — "we tried this", "still failing", "why is it like this" — or when you name an inspection command directly. It can recover what the user asked for, what earlier agents attempted, what changed, and whether checks passed.
- [`$turnal-fork-history`](.agents/skills/turnal-fork-history/SKILL.md) previews fork readiness, reruns a recorded task from its historical pre-turn workspace, and compares or selects isolated attempts.
- [`$turnal-restore-history`](.agents/skills/turnal-restore-history/SKILL.md) resolves checkpoint arguments, previews rollback, and performs an explicitly requested restore or interrupted-rollback recovery safely.

For example, ask the agent directly:

```text
$turnal-inspect-history Check whether this request has been tried before and recover the earlier user intent before implementing it.
$turnal-fork-history Rerun <session>:<turn> in isolation and compare the result with the existing attempts.
$turnal-restore-history Preview restoring the workspace to before <session>:<turn>; do not apply it yet.
```

OpenCode, Gemini CLI, and Copilot CLI use the versioned external adapter SDK. Release packages ship their adapter executables; verify discovery with `turnal adapter list` and `turnal adapter doctor`, then follow the [provider hook examples](docs/adapters.md#included-adapters).

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

# Check what Turnal could reconstruct before rerunning a task.
turnal fork <session>:<turn> --dry-run

# Run the captured instruction from the historical pre-turn workspace.
turnal fork <session>:<turn> -- codex exec

# See what would be restored, then perform the rollback.
turnal rollback --to <session>:<turn>:pre --dry-run
turnal rollback --to <session>:<turn>:pre
```

Rollback targets default to the post-turn checkpoint when the phase is omitted. Use `:pre` to return to the state before the turn and `:post` to return to the state after it.

To record the current workspace as an explicit rollback point, use `save`. The optional message is descriptive metadata; rollback uses the hidden Git commit hash printed by the command.

```sh
turnal save "tests passing before refactor"
turnal rollback --to <printed-hash> --dry-run
turnal rollback --to <printed-hash>
```

Manual saves capture the same project surface as automatic checkpoints. They do not capture the project's Git HEAD or index, so `--workspace-git` rollback is unavailable for them.

### Codex wrapper mode

Turnal can launch Codex with wrapper checkpoints in addition to hook capture:

```sh
turnal run -- codex
```

Wrapper checkpoints remain available even when Codex hook payloads are unavailable. Prompt, tool, and assistant details still depend on Codex hooks.

An application embedding Codex app-server does not automatically pass through this wrapper. App-server may discover Turnal's project hooks but skip untrusted definitions until you review and trust them in the host's hooks UI; Turnal diagnoses trust but never grants it.

### Manual turns

For an unsupported agent or a manual workflow:

```sh
turnal turn start --session demo
# Make changes or run the agent.
turnal turn finish --session demo
turnal diff demo:1
```

### Shared history

Shared history is opt-in. It publishes a signed, context-only projection to a Git remote without publishing Turnal snapshots, patches, raw hook payloads, tool inputs, or tool outputs. Preview the exact bundle and approve its policy hash before the first push:

```sh
turnal share enable --remote <git-url-or-path> --prompt-mode redacted_text
turnal share preview <session>:<turn> --json
turnal share preview <session>:<turn> --approve
turnal sync push --dry-run
turnal sync push
turnal sync pull
```

Use `turnal share status` to inspect consent, pending bundles, blocked projections, and quarantined publishers without contacting the network; add `--check-remote` for a bounded remote check. When sharing is configured, `turnal status` also reports a one-line publication summary. Discover local and pulled bundles with `turnal share list`, which names each source commit and branch and accepts `--commit <sha>`, and open a locator such as `v1:<device-id>:<bundle-id>` with `turnal share show <locator>`. Stop future synchronization with `turnal share disable --yes`; this preserves existing history and cannot recall published copies. See [shared history](docs/shared-history.md) for the protocol, privacy boundary, and failure semantics.

Teammates joining from independently initialized clones use the publisher's shared repository id: `turnal share enable --remote <same-remote> --repo-id <publisher-repo-id> --prompt-mode omit`, then `turnal sync pull`.

## How it works

Turnal deliberately keeps four responsibilities separate:

```text
.turnal/log/       append-only, hash-chained agent activity
.turnal/git/       private Git objects and checkpoint refs
.turnal/index/     rebuildable SQLite search and lookup cache
turnal             orchestration, validation, and user-facing commands
```

The event log preserves what the agent explicitly said it was trying to solve. Private Git snapshots preserve what actually changed. Turnal keeps those claims and facts separate, then derives diffs, rollback plans, replay, and line-level blame from both.

Checkpoint refs live under `refs/agent-vcs/...`. Hidden Git operations use explicit repository paths and scrub inherited `GIT_*` environment variables so they cannot be redirected into the project’s Git repository.

## What gets captured

By default Turnal records:

- User prompts and assistant messages exposed by agent hooks.
- For Claude Code and Codex, compact agent intent statements: the problem, expected scope, and evidence references supplied before an edit.
- Tool names, inputs, and results exposed by agent hooks.
- For Claude Code and Codex, before/after workspace snapshots around potentially mutating tool actions, so separate edits in one turn can carry separate intent. External adapters currently retain turn-level checkpoints and tool events without per-action snapshots.
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

Turnal stores history locally under `.turnal/` and does not upload it during normal recording. The explicit `turnal sync push` shared-history workflow is the exception. It publishes only the approved projection described above. Prompts and tool payloads can contain credentials or proprietary data, so review both the recording and publication policies before sensitive work.

`turnal search --semantic` also contacts the network, but only to download its embedding model on first use. Queries and the turns they match are embedded on this machine and are never sent.

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

When prompt or tool storage is disabled, Turnal writes an explicit redaction marker instead of the original content. Agent intent is treated as prompt-like text: `store_prompts = false` redacts both the durable statement and its `turnal intent` tool arguments, even when general tool I/O remains enabled. Snapshot deny globs apply to action snapshots and rollback, including rollback from older checkpoints that may contain a now-denied path.

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
turnal log --max-lanes 12               # Override the bounded eight-column graph
turnal log --max-lanes 0                # Allow unlimited graph columns
turnal log --session-limit 10           # Keep the ten most recently active sessions
turnal log --limit 20                   # Keep at most twenty turns per session
turnal log --transcript                 # Prompt/assistant/tool transcript
turnal log --all-worktrees              # Shared-store history
turnal show <session>:<turn>             # Normalized events for one turn
turnal show <session>:<turn> --full      # Include raw records/transcript text
turnal diff <session>:<turn>             # Pre-to-post patch
turnal search "authentication failure"  # Search the SQLite projection
turnal search "push must fail open" --all-projects
turnal search "why did sync block push" --all-projects --semantic
turnal blame src/auth.go:42              # Agent intent behind a line
turnal blame src/auth.go:42 --verbose    # Intent, evidence, human request, and action facts
turnal note add <session>:<turn> "..."   # Reply to a recorded turn
turnal note list                         # Notes recorded in this store
```

## Notes

A reviewer reading recorded history usually learns something the recording does not contain. Notes keep that where the turn is, instead of in a chat thread.

```sh
turnal note add <session>:<turn> "this broke auth, the intent statement was wrong"
turnal note add <session>:<turn> --path src/auth.go --line 42 "this is the regression"
turnal note add <session>:<turn> --path src/auth.go --line 40-48 --file -
turnal note list <session>:<turn>
turnal note remove <note-id>
```

A note is your statement about a turn, not recorded evidence, and Turnal never treats it as proof that a turn was right or wrong. Notes are written to this worktree's own note log, so noting a turn does not modify the agent history it discusses and does not change that turn's recorded duration. They appear in `turnal show`, in `turnal blame`, and in `turnal search`.

An anchored note records the file text as it existed at the turn's post checkpoint. When that text later changes or moves, Turnal reports the anchor as drifted rather than guessing where the line went. A note anchored to a line whose latest change came from a different turn still displays against its own line, because the turn that last touched a line is not necessarily the turn a reviewer meant.

`turnal note remove` hides a note. It does not erase one: the original stays in the append-only log, and any copy already published to teammates cannot be recalled. Dropping a session leaves notes about it in place and reports how many were orphaned, because commentary you did not write is not Turnal's to delete.

Notes can also be published to teammates, on a channel you enable separately from turn context:

```sh
turnal share notes enable --prompt-mode redacted_text
turnal share notes preview <note-id> --approve
turnal sync notes push
turnal sync notes pull
```

Published notes ride their own ref namespace, so enabling them does not change the turn-context policy and a teammate on an older Turnal build keeps pulling turn context normally instead of failing. `turnal share show <locator>` lists the notes replying to a shared turn. See [shared history](docs/shared-history.md) for the publication boundary.

When the workspace secrets policy sets `store_prompts = false`, a note's text, author, and anchor metadata are all withheld, because the path and line range describe workspace content just as the body does.

`blame` reports the agent's stated problem first and keeps the human request as separate context. Its confidence label is derived from recorded timing and scope: an available statement captured before the action is high confidence, while a late statement or a change outside the stated scope is labeled accordingly. Redacted intent is explicit and remains low confidence because its scope is unavailable. A normal turn with no intent says that none was recorded. When a recorded statement cannot be tied safely to one action, or when turns overlap, Turnal uses explicit `ambiguous` or `concurrent` origins instead of borrowing an agent's statement.

The checkpoint graph packs non-overlapping or timestamp-touching session spans into reusable lanes and caps the display at eight columns by default. When more lanes are needed, the most recently active spans keep dedicated lanes and the eighth becomes an overflow lane with turn markers but no connecting line; each inline session label and session-derived true color continues to identify the turn. Graph summaries always include the displayed lane count and disclose overflow or session/turn filtering. Use `--verbose` to print the full session legend.

If the disposable index is missing or stale:

```sh
turnal reindex
```

Reindexing rebuilds SQLite from the event logs and private checkpoint refs.

Search defaults to the current worktree. `--all-worktrees` broadens the query
within its Turnal store; `--all-projects` searches the healthy indexes of every
project registered on this machine and labels each result with its project and
root. A project whose index is missing or stale is reported as a warning
without suppressing results from the healthy ones.

Keyword search remains the offline default. Add `--semantic` to also match on
meaning, which finds turns that share no words with the query. Keyword hits
always rank above meaning-only hits, and every result states which path found
it. The first semantic search downloads the 8 MB `minishlab/potion-base-2M`
model from Hugging Face into the user cache; Turnal sends no prompts,
transcripts, tool data, or other recorded history.

### Local viewer

Run `turnal ui` to open Turnal Prism, a local browser interface for browsing recorded projects, sessions, turns, prompts, tool activity, diffs, and line-level blame. It runs on the loopback interface and can be launched from inside a recorded project or elsewhere to open the project index.

```sh
turnal ui
turnal ui --no-open
```

See the [Prism guide](docs/viewer.md) for project and session launch options, data limits, and its security model.

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

## Repository verification

Declare an ordered set of verifier commands in the repository's `.turnal/config.toml`:

```toml
[[verify]]
name = "unit-tests"
command = "go"
args = ["test", "./..."]
timeout = "2m"

[[verify]]
name = "race-detector"
command = "go"
args = ["test", "-race", "./..."]
timeout = "5m"
```

Each command and argument is passed directly to the operating system in declaration order; Turnal does not invoke a shell or expand variables, globs, pipes, or redirects. Names must be unique printable UTF-8 text without formatting controls, commands and timeouts are required, and malformed or excessive definitions make verification fail before any check starts.

Run the checks directly in the current workspace:

```sh
turnal verify
turnal verify --json
```

Live verification does not create a checkpoint or move the project's branch, HEAD, index, or Git configuration. The checks run in the mutable workspace and may modify it themselves, so the report identifies this target as live and does not claim that the result is reproducible.

Run the same declarations against a recorded state:

```sh
turnal verify <session>:<turn>:pre
turnal verify <session>:<turn>:post --json
```

The canonical `<session>:turn:<turn>:pre|post` spelling is accepted too. Turnal verifies the durable checkpoint metadata and refs before launching a command, then materializes the captured project surface into a Turnal-owned temporary directory. It never runs checkpoint checks in the active workspace and never moves or initializes the project's `.git/`. The evaluation omits `.git/`, `.turnal/`, ignored paths, uncaptured files, and empty directories that were not represented by the checkpoint tree. Turnal reapplies the current `secrets.snapshot_deny_globs` during verifier materialization, so a newly denied path stays absent even if an older checkpoint captured it; exact replay materialization retains the historical surface.

Checks run sequentially and continue after ordinary failures. Turnal terminates remaining supervised descendants when the direct verifier exits, and a configured timeout terminates the supervised process tree through a Windows Job Object or the child's process group on Unix. If containment cleanup fails, the check keeps its original result while JSON and human output report a separate infrastructure error and the aggregate verification remains unsuccessful. A Unix descendant that deliberately detaches from that process group is outside Turnal's containment boundary, but retained output pipes have a bounded one-second wait so such a descendant cannot keep `turnal verify` blocked indefinitely. Ctrl+C and SIGTERM cancel the active verifier and trigger owned checkpoint-directory cleanup. Stdout and stderr are captured separately, with the first 1 MiB retained for each stream and independent truncation flags in JSON. Children inherit the caller's environment, toolchain, network access, and current external-service state, but reports record only that inheritance policy and never serialize environment-variable values.

The command exits `0` when every check passes and `3` after running every eligible check when any check fails, times out, or cannot start. Invalid configuration, unresolved or corrupt targets, materialization failures, and cleanup errors are ordinary Turnal errors and exit `1`. A passing verifier is evidence for the property that command checks; it is not proof that the entire change is correct.

Standalone verifier reports are printed or returned as JSON. Forked Case attempts also run the verifier contract frozen when the Case was created, and the report is stored with the durable attempt result so later comparisons retain the evidence used at execution time.

## Reproducibility and Cases

Before rerunning a recorded task, inspect what Turnal can reconstruct:

```sh
turnal fork <session>:<turn> --dry-run
turnal fork <session>:<turn> --dry-run --json
```

The report identifies the exact pre-turn checkpoint, captured file count,
observable instruction status, provider metadata, and known gaps such as the
conversation context that cannot currently be reconstructed, unpinned
toolchain, live external services, and secrets that require fresh authorization.
A redacted or missing prompt is reported as requiring new user input and is
never recovered from raw storage.

Execute a supervised attempt from that checkpoint by placing the command after
`--`:

```sh
# A bare Codex command receives the exact captured instruction automatically.
turnal fork <session>:<turn> -- codex exec

# Any runner can consume the provenance exported as TURNAL_FORK_* variables.
turnal fork <session>:<turn> -- sh -c 'my-runner "$TURNAL_FORK_INSTRUCTION"'
```

Execution creates or reuses an immutable Case for the source turn, materializes
its pre-turn checkpoint into an owner-only temporary directory, and runs the
child there. The child never runs in the source workspace, `.git/` and
`.turnal/` are excluded, and inherited `GIT_*` variables are removed. Turnal
records wrapper pre/post checkpoints, the command status, and the Case's frozen
verifier report as a durable Attempt. The temporary directory is removed by
default; `--keep` preserves it for inspection. Bare `codex` and `codex exec`
commands receive the captured instruction unless `--no-replay-instruction` is
set; commands with explicit arguments are left unchanged.

Create a Case without running an Attempt when you want to preserve the
reproducibility contract first:

```sh
# The first Case creates its Task identity.
turnal case create <session>:<turn>

# Inspect the immutable Case or its evolving Task identity.
turnal case show <case-id>
turnal task show <task-id>

# Create a sibling Case when another turn has the same Task instruction.
turnal case create <other-session>:<turn> --task <task-id>
```

A Case freezes the source turn, Task revision, starting checkpoint, repository
verifier contract, known limitations, and linked Attempts. Creating a sibling
with `--task` succeeds only when the recorded instruction matches the applicable
Task revision. Once a Case exists, use its ID to avoid ambiguity:

```sh
turnal fork <case-id> --dry-run
turnal fork <case-id> -- codex exec
```

Compare every completed Attempt against the same Case base, record a choice,
then preview or apply it:

```sh
turnal compare <case-id>
turnal compare <case-id> --patch <attempt-id>
turnal select <case-id> <attempt-id>
turnal apply <case-id> --dry-run
turnal apply <case-id>
```

Apply is intentionally exact-base only. It refuses to change a workspace whose
captured surface differs from the Case base; when the base matches, it uses the
journaled rollback engine, creates a safety checkpoint first, restores the
selected post-checkpoint, and records the application on the Case. It does not
perform a three-way merge.

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
turnal case delete <case-id> --dry-run
turnal case delete <case-id> --yes
turnal session drop <session> --dry-run
turnal session drop <session>
turnal retention prune --dry-run
turnal retention prune
turnal maintenance gc
```

Cases retain their source and Attempt sessions. Case deletion writes an
irreversible tombstone that makes those sessions eligible for removal; it
requires `--yes` and refuses while a linked Attempt is still running. Dropping
a session then removes its durable event records and refs. Git objects are
physically reclaimed only after refs are removed and explicit maintenance runs.

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

[[verify]]
name = "unit-tests"
command = "go"
args = ["test", "./..."]
timeout = "2m"

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
turnal status --probe-agent-capture
turnal sessions
turnal recovery status
```

- **Hooks need attention:** status distinguishes a missing event from an event configured with a different command; rerun `turnal init --agent claude`, `--agent codex`, or `--agent all` only after reviewing the reported configuration.
- **Claude Agent SDK is host-controlled:** the host must omit `settingSources` or include `"project"`. Turnal cannot infer arbitrary SDK host configuration and does not consume the SDK stream directly.
- **Codex app-server hooks are untrusted:** review the project and exact hook definitions in Codex's hooks UI. Turnal does not change project trust, hook trust, or private provider trust databases.
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

The upgrade action follows the installation source:

- npm installations run the matching global npm install.
- Standalone installations download the matching GitHub release archive, verify its checksum and embedded build metadata, and transactionally replace `turnal` plus its external adapter executables.
- Source or unknown installations print manual update instructions.

Turnal may occasionally print a channel-preserving update notice after interactive commands for npm and standalone installations. Set `TURNAL_NO_UPDATE_CHECK=1` to disable these notices.

## Development

```sh
go test ./...
go vet ./...
go build -o bin/turnal ./cmd/turnal
```

The Astro marketing and documentation site is kept outside the npm package:

```sh
cd apps/marketing
npm install
npm run dev
```

Authenticated provider testing is intentionally excluded from the default suite. Set `TURNAL_LIVE_CODEX_TEST=1` to run the live Codex integration test in a trusted disposable repository.

## Security

Workspace metadata can contain prompts, tool input/output, file history, and raw provider payloads. Review [SECURITY.md](SECURITY.md) before using Turnal with sensitive repositories.

## License

Turnal's original code and documentation are licensed under Apache-2.0.
Third-party names, logos, and trademarks remain the property of their
respective owners and are not licensed under Apache-2.0 by this project. See
[NOTICE](NOTICE) for details.
