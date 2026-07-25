---
title: Turnal documentation | Record, verify, and recover agent work
description: Production documentation for Turnal, including local agent history, verification, reproducibility analysis, replay, rollback, and the public CLI.
---

<h1 id="overview">Record the work.<br>Keep the reason.</h1>

<p class="docs-lead">Turnal is a local flight recorder for Claude Code and Codex. It captures each agent turn as an append-only event trail and a pair of hidden Git checkpoints, so you can reconstruct what happened, find the prompt behind a line, test an earlier state, or safely return to it.</p>

[Start in five minutes](#quickstart) · [Understand the model](#mental-model)

| Guarantee | What it means |
| --- | --- |
| **Local recording** | Recording data stays in `.turnal/`. |
| **Separate history** | Your workspace Git history remains independent. |
| **Recoverable by design** | Rollback saves the current workspace first. |

---

## Installation

Install Turnal globally with npm. The package selects a prebuilt binary for macOS, Linux, or Windows on x64 and arm64.

```sh
npm install -g @aadijo/turnal
```

On macOS or Linux, install standalone binaries without Node.js:

```sh
curl --proto '=https' --tlsv1.2 -fsSL \
  https://raw.githubusercontent.com/AadiJo/turnal/main/install.sh | sh
```

The standalone installer verifies the release checksum and installs Turnal and its external adapters under `~/.local/bin`. Pass `--version VERSION` to pin a release or `--install-dir DIRECTORY` to select a writable destination. It never invokes `sudo`.

On an unsupported npm platform Turnal can fall back to a local Go build. Isolated `turnal fork` execution requires Linux, macOS, or Windows; elsewhere it fails closed rather than running an uncontained child.

### Requirements

- Git available on `PATH`
- Node.js 18 or newer when installing through npm
- Go only when a prebuilt binary is unavailable

Verify the installation:

```sh
turnal version
turnal --help
```

Turnal preserves the installed release channel during upgrades. Use `turnal upgrade --check` to check without installing, or select `--stable` or `--nightly` explicitly when switching channels. npm installations upgrade through npm; standalone installations download, verify, and transactionally replace the matching release binaries. Source and unknown installations receive manual instructions.

---

## Quickstart

Initialize Turnal at the directory you consider the workspace root. The `--agent all` selection prepares every supported agent integration.

```sh
npm install -g @aadijo/turnal
cd path/to/your/project
turnal init --agent all
turnal status
turnal status --probe-agent-capture

# Use Claude Code or Codex normally, then inspect the recording.
turnal sessions
turnal log --transcript
```

> **Trust the workspace hooks before using your agent.** For Codex, launch the Codex CLI in this workspace first and approve the Turnal hooks there before using Codex through another surface, such as the desktop app; those surfaces may not show the hook-trust prompt. For Claude Code, trust the workspace when prompted; no separate hook approval is needed.

1. **Initialize storage.** Turnal creates or attaches `.turnal/` and adds the store to `.gitignore`. It does not initialize or modify the project's Git repository.
2. **Install agent hooks.** Existing non-Turnal hooks are preserved. If a supported hook configuration file is invalid or has an unexpected shape, initialization leaves it unchanged and asks you to repair it before retrying.
3. **Work normally.** The provider launches as usual; Turnal records hook events and snapshots in the background.

Normal `turnal status` is offline. The explicit `--probe-agent-capture` check starts Codex app-server only long enough to inspect hook discovery, enablement, and trust; it does not start a thread or turn, invoke a model, change provider trust, or modify workspace files. Codex may update its own cache or runtime state while app-server starts. For Claude Agent SDK hosts, the probe explains that only the host can determine whether project settings are loaded.

### Give the agent Turnal workflows

Turnal includes three project-scoped skills. At the end of an interactive `turnal init`, press Enter or type `y` to install the bundled skills under `.turnal/skills/` and link them into the project skill directories for the initialized agents: `.agents/skills/` for Codex and `.claude/skills/` for Claude Code. Windows uses directory junctions when ordinary symlink creation requires Developer Mode or elevation. Compatible agents can select them from their descriptions or you can invoke them explicitly:

- [`$turnal-inspect-history`](https://github.com/AadiJo/turnal/blob/main/.agents/skills/turnal-inspect-history/SKILL.md) checks recorded work before implementation when a request may have been tried before, revisits existing code, or depends on missing prior intent. It recovers what the user asked for, what earlier agents attempted, what changed, and whether checks passed.
- [`$turnal-fork-history`](https://github.com/AadiJo/turnal/blob/main/.agents/skills/turnal-fork-history/SKILL.md) previews fork readiness, reruns a recorded task from its historical pre-turn workspace, and compares or selects isolated attempts.
- [`$turnal-restore-history`](https://github.com/AadiJo/turnal/blob/main/.agents/skills/turnal-restore-history/SKILL.md) resolves checkpoint arguments, previews rollback, and performs an explicitly requested restore or interrupted-rollback recovery safely.

```text
$turnal-inspect-history Check whether this request has been tried before and recover the earlier user intent before implementing it.
$turnal-fork-history Rerun <session>:<turn> in isolation and compare the result with the existing attempts.
$turnal-restore-history Preview restoring the workspace to before <session>:<turn>; do not apply it yet.
```

> **Check health before relying on recovery.** Run `turnal status` after setup. It validates identities, hidden Git, durable refs, configuration, hook wiring, and unfinished merge or rollback journals. A non-zero exit status means the workspace needs attention.

---

## Mental model

Turnal separates the meaning of an agent run from the bytes in the workspace. One store contains three cooperating layers, each with a deliberately different job.

| Layer | Purpose | Durability |
| --- | --- | --- |
| **Event streams** | Append-only, hash-chained JSONL records preserve prompts, replies, tools, errors, checkpoints, and adapter metadata. | Durable; explains why |
| **Hidden Git** | A bare repository stores byte-exact synthetic commits for pre- and post-turn workspace snapshots. | Durable; preserves what |
| **SQLite index** | An FTS5 index makes history searchable and is derived from durable logs and checkpoint refs. | Disposable; accelerates queries |

A **session** follows the provider session ID. A **turn** normally begins with a user prompt and becomes complete only after Turnal has both a pre checkpoint and a post checkpoint. Tool activity can start a turn when a prompt event is unavailable.

> **The index is a cache, not the record.** Normal activity does not continuously rebuild full-text search. Run `turnal reindex` before searching and after recording new work. If the SQLite database is missing or damaged, rebuild it from the durable layers.

---

## Capture boundaries

A normal turn has a before-and-after boundary. The prompt hook records context and creates the pre checkpoint; tool hooks add normalized activity; the stop hook records the result and creates the post checkpoint.

1. **Prompt:** event and pre checkpoint
2. **Agent work:** tools, replies, and errors
3. **Stop:** event and post checkpoint

### Included in snapshots

- Regular files under the workspace root
- Symbolic links, captured as links
- Executable and non-executable file modes
- Untracked files unless ignored or denied

### Excluded and preserved

- Any `.git` or `.turnal` path segment
- Files ignored by workspace Git
- Paths matching `secrets.snapshot_deny_globs`
- Excluded files already present during restore

> **Turnal Git is not your project Git.** Turnal works in Git and non-Git directories without initializing a project repository. Normal checkpoints and checkpoint-mode rollback do not write the project's `HEAD`, index, branches, or refs. Full workspace-Git capture is a separate, opt-in mode and requires the Turnal workspace root to be the Git worktree root with an initial commit.

---

## Agent integrations

Turnal currently supports Claude Code and Codex. Hook installation is additive: it removes or refreshes Turnal-owned commands while leaving unrelated hook commands in place.

<h3 class="agent-heading"><img src="/brands/claude.svg" alt="" aria-hidden="true"><span>Claude Code</span></h3>

Claude Code is configured in `.claude/settings.json`.

| Hook | Recorded boundary |
| --- | --- |
| `UserPromptSubmit` | Prompt and pre boundary |
| `PostToolUse` | Tool activity |
| `Stop` | Reply and post boundary |

<h3 class="agent-heading"><img src="/brands/codex.svg" alt="" aria-hidden="true"><span>Codex</span></h3>

Codex is configured in `.codex/config.toml` with hooks enabled.

| Hook | Recorded boundary |
| --- | --- |
| `SessionStart` | Session metadata |
| `UserPromptSubmit` | Prompt and pre boundary |
| `PostToolUse` | Tool activity |
| `Stop` | Reply and post boundary |

Select integrations explicitly when needed:

```sh
# Configure only one adapter.
turnal init --agent claude
turnal init --agent codex

# Configure both explicitly.
turnal init --agent all

# Create storage without changing hook files.
turnal init --agent none --skip-hooks
```

### Codex wrapper checkpoints

`turnal run -- codex` launches Codex with hooks enabled and adds independent wrapper-level pre/post checkpoints. If hooks emit no prompt, tool, or assistant payloads, the safety checkpoints still exist but the semantic transcript will be sparse. The wrapper currently supports Codex only.

### Manual turns

Use manual turn boundaries when an agent has no compatible hook integration or when you need to capture a non-agent workflow. `start` creates the pre checkpoint and `finish` closes the active turn with its post checkpoint.

```sh
turnal turn start --session demo
# Make changes or run the unsupported agent.
turnal turn finish --session demo
turnal diff demo:1
```

Pass `--turn N` when you need to choose the turn number explicitly; otherwise `start` uses the next turn and `finish` uses the active turn.

---

## History and turns

Start with sessions, then move from a timeline into a single turn or its exact file patch. Session listing is store-wide; history commands default to the current worktree.

```sh
turnal sessions
turnal log
turnal log --transcript --session claude-7f2a
turnal show claude-7f2a:2
turnal diff claude-7f2a:2
```

| Command | Purpose |
| --- | --- |
| `turnal sessions` | Lists provider, turn counts, activity range, latest prompt, tools, and head checkpoint for each recorded session. |
| `turnal log` | Builds the timeline from durable data by default. Use `--all-worktrees` for a shared-store view and `--no-pager` in scripts. |
| `turnal show` | Accepts latest, a bare unambiguous turn number, `SESSION:TURN`, or `SESSION:latest`. |
| `turnal diff` | Compares the selected turn's pre and post hidden-Git checkpoints. |

> **Normalized history and provider transcripts are different.** `--transcript` on `turnal log` renders normalized Turnal events. `turnal show --transcript` can also read the provider's own transcript from its captured path on demand. Turnal does not copy that provider transcript into its store.

---

## Search

Search covers normalized prompts, assistant replies, tool activity, file paths, and event text. Whitespace-separated query terms are combined with AND, so every term must match the indexed turn.

```sh
turnal reindex
turnal search "invoice duplicate"
turnal search "stripe" --session codex-b91e
turnal search "timeout" --all-worktrees --json
```

Search defaults to the current worktree and 20 results. Use `--all-worktrees` for attached or imported worktrees and `--limit 0` for every result. Re-run `turnal reindex` whenever newer activity is absent from results.

---

## Prompt-aware blame

Turnal replays completed checkpoint history and attributes each current line to the turn that last changed it. Rename tracking and moved-line origins preserve authorship better than a simple last-snapshot comparison.

```sh
turnal blame src/webhooks/stripe.ts
turnal blame src/webhooks/stripe.ts:24 --verbose
turnal blame src/webhooks/stripe.ts --session codex-b91e --json
```

- Only complete turns with both pre and post checkpoints participate.
- The latest completed post checkpoint is the file source; live, uncheckpointed edits are not included.
- Binary files are not supported.
- The optional blame cache is derived and can be rebuilt.

---

## Rollback

Rollback changes the source workspace. Turnal first records the current workspace as a hidden safety snapshot, journals the operation, then restores the selected checkpoint. Preview target resolution and planned file changes with `--dry-run`.

```sh
# Undo the work performed by turn 2.
turnal rollback --to claude-7f2a:2:pre --dry-run
turnal rollback --to claude-7f2a:2:pre

# Restore the completed result of turn 2.
turnal rollback --to claude-7f2a:2:post
```

| Phase | State restored | Use it when |
| --- | --- | --- |
| `:pre` | Before the agent worked | You want to undo the changes made during the selected turn. |
| `:post` | After the agent finished | You want the completed result. This is the default when the phase is omitted. |

Targets can also be a `chk_` checkpoint ID prefix or a hidden Git SHA prefix; prefixes must be long enough to resolve safely. For another attached worktree, add `--from-worktree ID`.

> **Excluded files survive restore.** Checkpoint mode restores captured workspace files while preserving Git-ignored files, deny-glob matches, `.git`, and `.turnal`. The safety snapshot ref is printed after a successful rollback and in recovery-oriented errors.

### Workspace-Git rollback

Enable `git_sync.enabled` before the turn when you need to capture and later restore project Git state: `HEAD`, index, staged and unstaged patches, and non-ignored untracked files. The Turnal workspace root must be the Git worktree root, and the repository must already have an initial `HEAD` commit. Then pass `--workspace-git`, or set `rollback.mode = "workspace-git"`. A checkpoint captured before Git sync was enabled cannot be upgraded retroactively.

---

## Replay worktrees

Replay materializes a checkpoint in an isolated directory, leaving the source workspace untouched. Use it to inspect an old state, step through a session, run tests, or compare a checkpoint with what exists now.

```sh
turnal replay checkout claude-7f2a:2:pre
turnal replay show
turnal replay next
turnal replay diff
turnal replay diff --workspace
turnal replay keep
turnal replay stop
```

A managed replay lives under `.turnal/tmp/replay/worktrees`. By default, `turnal replay stop` removes it. Run `turnal replay keep` first to preserve that directory, or provide an empty destination path to copy the current replay state elsewhere. A range such as `SESSION:2..6` constrains navigation.

Replay navigation commands are `prev`, `next`, `goto`, `show`, `diff`, `list`, `keep`, `stop`, and `remove`.

---

## Verification

`turnal verify` runs repository-defined checks against the live workspace or an isolated historical checkpoint. Historical verification materializes the captured state in a temporary worktree, runs the same commands there, and leaves the source workspace untouched.

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

```sh
# Check the current workspace.
turnal verify

# Check the exact captured state after turn 4.
turnal verify claude-7f2a:4:post
```

Verifier definitions must come from the workspace `.turnal/config.toml`; user-level configuration cannot silently add repository commands. Historical files are fixed by the checkpoint, but commands still use the current machine's toolchain, credentials, network, and external services. Human output summarizes every check, while `--json` emits the complete versioned report. A completed verification with failed checks exits with status 3.

---

## Reproducibility and cases

Before attempting to rerun old work, `turnal fork --dry-run` reports what Turnal can reconstruct and what still depends on live context. The report covers the pre-turn workspace, instruction, recorded model and permissions, conversation context, toolchain, secrets, network, and configured evaluators.

```sh
turnal fork claude-7f2a:4 --dry-run
turnal fork claude-7f2a:4 --dry-run --json
```

The preflight is read-only and does not claim that a recorded turn is deterministic. When the report has a usable base, execute a supervised attempt by putting the runner after `--`:

```sh
# Reuse the captured instruction with Codex.
turnal fork claude-7f2a:4 -- codex exec

# Or use any runner; Turnal exports TURNAL_FORK_* provenance variables.
turnal fork claude-7f2a:4 -- sh -c 'my-runner "$TURNAL_FORK_INSTRUCTION"'
```

Turnal creates or reuses a Case, materializes its pre-turn checkpoint into an owner-only temporary directory, and runs the child there. The source workspace is never the child's working directory, `.git/` and `.turnal/` are excluded, and inherited `GIT_*` variables are removed. The wrapper records pre/post checkpoints, command status, and results from the verifier contract frozen into the Case. The directory is removed by default; `--keep` preserves it.

Fork execution is supported on Linux, macOS, and Windows. Other source-build targets can inspect readiness with `--dry-run`, but execution fails before starting the requested command when whole-process-tree containment is unavailable.

Experimental cases turn that readiness report into an immutable record. A case preserves the source turn, task revision, starting checkpoint, repository verifier contract, known limitations, and links to any existing wrapper attempts associated with the source turn.

```sh
# The first case creates its task identity.
turnal case create claude-7f2a:4

# Inspect the immutable case or its evolving task identity.
turnal case show case_...
turnal task show task_...

# Create a sibling case when the recorded instruction matches the task revision.
turnal case create codex-b91e:2 --task task_...
```

Compare completed attempts against their common immutable base, select one, and apply it only when the live workspace still matches that base:

```sh
turnal compare case_...
turnal compare case_... --patch attempt_...
turnal select case_... attempt_...
turnal apply case_... --dry-run
turnal apply case_...
```

Apply is exact-base only and does not attempt a three-way merge. A real apply uses the journaled rollback engine, captures a safety checkpoint before changing files, restores the selected attempt's post-checkpoint, and records the application on the Case.

---

## VS Code

The first-party VS Code extension keeps Turnal's durable CLI model visible in the editor. It adds prompt-aware inline blame for the current line, session and recent-activity views, native single- and multi-file turn diffs, readable turn details, and rollback previews in VS Code's Changes editor.

Editor rollback always performs a fresh dry run, warns about affected unsaved editors, and asks for confirmation before changing the workspace. It uses checkpoint mode and never moves the project's Git HEAD or index. Recording, verification, replay, search, retention, and configuration remain CLI workflows.

[Install the extension from the Visual Studio Marketplace](https://marketplace.visualstudio.com/items?itemName=aadijo.turnal-vscode).

---

## Git worktrees

Linked Git worktrees can share the primary worktree's physical `.turnal` store. Turnal records a distinct worktree identity on every stream and uses a user-state registry to find the shared store again.

```sh
# Usually discovery is automatic from a linked worktree.
turnal init

# Attach explicitly when discovery is unavailable.
turnal worktree attach --store /path/to/primary/.turnal
turnal worktree list
turnal worktree repair
```

The registry lives under `TURNAL_STATE_DIR` when set; otherwise Turnal uses the platform state directory, with `XDG_STATE_HOME` on XDG systems. History, search, and rollback remain current-worktree scoped unless you opt into their cross-worktree flags.

> **Copied stores need new identities.** If you copy `.turnal` rather than attaching the same physical store, run `turnal store rekey --confirm` on the copy. This creates new store, worktree, and producer IDs; existing event records and commits are intentionally not rewritten.

---

## Merge stores

`turnal merge` imports immutable event streams and private checkpoint refs from another store or a workspace containing one. The destination keeps a manifest of imported material and rebuilds its search index after a successful durable import.

```sh
turnal merge /path/to/other/workspace --dry-run
turnal merge /path/to/other/workspace

# Resume or discard an interrupted import.
turnal merge --recover
turnal merge --abort
```

- Source and destination cannot have the same store ID.
- Hidden-Git object formats must match.
- Divergent immutable streams are rejected instead of silently reconciled.
- A different repository ID requires `--adopt-source-as-current-repo` and should only be used for the same logical project.

> **Index failure does not erase a successful import.** Imported event logs and refs are durable before reindexing. If the automatic index rebuild warns or fails, repair the cause and run `turnal reindex` manually.

---

## Configuration

Configuration is layered from broad defaults to workspace-specific behavior. Later sources override earlier ones:

1. **Built-in defaults:** safe, zero-config baseline
2. **User config:** platform user config directory, or `TURNAL_CONFIG`
3. **Workspace config:** `.turnal/config.toml` in the physical store
4. **Environment:** `TURNAL_HOOK_COMMAND` overrides the hook command
5. **CLI flags:** command-specific, highest precedence

```toml
version = 1

[init]
agent = "auto"
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
mode = "checkpoint"

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

### Configuration keys

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `version` | integer | `1` | Configuration schema version. Only version 1 is accepted. |
| `init.agent` | enum | `"auto"` | Hook target: auto, claude, codex, all, or none. |
| `init.install_hooks` | boolean | `true` | Install or refresh agent hooks during `turnal init`. |
| `run.install_hooks` | boolean | `true` | Refresh Codex hooks before `turnal run` launches Codex. |
| `run.quiet` | boolean | `false` | Suppress Turnal wrapper status messages. |
| `run.bypass_hook_trust` | boolean | `false` | Pass Codex the dangerous hook-trust bypass flag. |
| `hooks.command` | string | `"turnal"` | Executable prefix written into Claude Code and Codex hooks. |
| `bootstrap.update_gitignore` | boolean | `true` | Ensure `.turnal/` appears in the workspace `.gitignore`. |
| `git_sync.enabled` | boolean | `false` | Capture `HEAD`, index, tracked patches, and untracked files alongside future checkpoints. |
| `rollback.mode` | enum | `"checkpoint"` | Default rollback engine: checkpoint or workspace-git. |
| `secrets.store_prompts` | boolean | `true` | Store normalized prompt and assistant text in Turnal logs. |
| `secrets.store_tool_io` | boolean | `true` | Store tool inputs and outputs in Turnal logs. |
| `secrets.snapshot_deny_globs` | string array | Default secret globs | Paths excluded from new snapshots and protected during restore. |

### Environment variables

| Variable | Purpose |
| --- | --- |
| `TURNAL_CONFIG` | Use this config path instead of the OS user config path. |
| `TURNAL_HOOK_COMMAND` | Override `hooks.command` after file configuration is loaded. |
| `TURNAL_STATE_DIR` | Absolute directory for the cross-worktree store registry. |
| `XDG_STATE_HOME` | Base state directory when `TURNAL_STATE_DIR` is unset on XDG systems. |
| `TURNAL_NO_UPDATE_CHECK` | Disable interactive npm update notices when set to a truthy value. |
| `TURNAL_NPM_CACHE` | Override the fallback binary cache used by the npm launcher. |
| `PAGER` | Pager for long `turnal log` output. Set it to `cat` or pass `--no-pager` to disable paging. |
| `CLAUDE_CONFIG_DIR` | Allowed Claude transcript root used by `turnal show --transcript`. |
| `CODEX_HOME` | Allowed Codex transcript root used by `turnal show --transcript`. |

---

## Privacy and storage

Turnal does not upload recording data. Event logs, snapshots, and the search index stay in the local Turnal store. The npm launcher may contact npm for an interactive update notice unless `TURNAL_NO_UPDATE_CHECK` is set.

| Layer | Contains | Policy |
| --- | --- | --- |
| **Event streams** | Normalized semantic events and raw adapter references | Durable and hash-chained |
| **Hidden Git** | Checkpoint commits and safety refs | Durable and local |
| **SQLite** | FTS search and derived metadata | Disposable and rebuildable |
| **Provider transcript** | Claude Code or Codex-owned conversation file | Read on demand and not copied |

### Recording policy

Prompts and tool I/O are stored by default. Set `secrets.store_prompts = false` or `secrets.store_tool_io = false` to redact those fields from new normalized events and structured raw hook payloads. This setting does not modify transcript files written by the provider itself.

> **Malformed raw payloads cannot be structurally redacted.** When an adapter provides a non-JSON raw hook payload, Turnal preserves the opaque raw record because it cannot reliably identify individual prompt or tool fields inside it. Treat access to `.turnal` as access to development history, and set recording policy before starting sensitive work.

### Snapshot deny globs

The default deny list excludes common environment and credential files. Add project-specific secrets before recording. Denied paths are excluded from new checkpoints and protected from deletion or replacement during checkpoint restore.

```toml
[secrets]
snapshot_deny_globs = [
  ".env",
  ".env.*",
  "**/.env",
  "**/.env.*",
  "**/credentials.*",
  "**/*.pem",
]
```

Transcript reads are limited to recognized Claude Code or Codex roots, such as `CLAUDE_CONFIG_DIR` and `CODEX_HOME`. They reject `.git` paths and enforce a 64 MiB file-size limit.

---

## Retention and removal

Durable history and hidden-Git object bytes are removed in deliberate stages. Preview each stage before committing to it.

1. **Release Case retention when necessary:** `turnal case delete CASE_ID --dry-run` confirms the experimental Case exists and previews its tombstone so its source and attempt sessions can become eligible for deletion. Confirm with `--yes` only when the Case is no longer needed; deletion refuses while an attempt is still running.
2. **Drop a session:** `turnal session drop ID --dry-run` previews removal of its event log, stream metadata, temporary state, and related private refs. Run the drop separately, or use `--purge` for immediate ref pruning and hidden-Git garbage collection.
3. **Prune refs:** `turnal retention prune --dry-run` removes private refs no durable record, journal, active turn, or import manifest still needs.
4. **Collect objects:** `turnal maintenance gc --dry-run` previews expiration of hidden reflogs and pruning of unreachable Git objects.

> **Garbage collection is the irreversible step.** Dropping a session or pruning a ref does not immediately erase underlying Git objects. `turnal maintenance gc` and `turnal session drop --purge` perform immediate object pruning. Review a separate dry run first and keep an external backup when the history may still matter.

### Remove Turnal from a workspace

```sh
turnal destroy --dry-run --remove-hooks --agent all
turnal destroy --remove-hooks --agent all
```

Destroy removes Turnal metadata and, when requested, Turnal-owned agent hook commands. It does not delete workspace source files. Always inspect `--dry-run` first.

---

## Command reference

These are Turnal's primary public commands. Low-level hook and checkpoint plumbing commands are intentionally omitted; the supported manual turn workflow is included.

### `turnal init`

Create or attach the Turnal store for the current directory and configure agent hooks.

```text
turnal init [--agent auto|claude|codex|all|none] [--skip-hooks]
            [--git-sync] [--store PATH]
```

| Flag | Description |
| --- | --- |
| `--agent VALUE` | Select auto, claude, codex, all, or none. Default: auto. |
| `--skip-hooks` | Initialize storage without changing agent hook configuration. |
| `--git-sync` | Capture workspace Git state for future workspace-git rollbacks. |
| `--store PATH` | Use or create an explicit physical `.turnal` store. |

Run this from the directory that should become the workspace root. By default Turnal creates its own hidden Git store, adds `.turnal/` to `.gitignore`, and installs detected hooks without initializing or modifying the project's Git repository. `--git-sync` requires an existing Git worktree rooted at this directory with an initial commit.

### `turnal status`

Inspect storage, identities, hidden Git, hook health, integrity, and pending journals.

```text
turnal status [--probe-agent-capture]
```

Returns a non-zero status when the workspace needs attention. `--probe-agent-capture` adds a runtime compatibility check for configured execution surfaces; normal status remains offline.

### `turnal adapter`

Discover external adapter executables and verify that they implement the supported protocol.

```text
turnal adapter list
turnal adapter doctor [ADAPTER...]
```

`list` finds `turnal-adapter-*` executables on `PATH` and prints their advertised versions. `doctor` checks discovery, executable naming, and protocol compatibility for every discovered adapter or only the named adapters.

### `turnal sessions`

List recorded sessions, adapters, turn counts, activity, latest prompt, and head checkpoint.

```text
turnal sessions [--json]
```

`--json` emits structured JSON instead of formatted text.

### `turnal log`

Render checkpoint history across one or more agent sessions.

```text
turnal log [--session ID] [--limit N] [--transcript] [--verbose]
           [--worktree ID | --all-worktrees] [--stream ID]
           [--session-limit N] [--max-lanes N]
           [--index | --durable] [--no-pager]
```

| Flag | Description |
| --- | --- |
| `--session ID` | Restrict output to one provider session. |
| `-n, --limit N` | Maximum turns per session; 0 shows all. Default: 0. |
| `--transcript` | Show normalized human, agent, and tool-call text. |
| `--verbose` | Show full refs, checkpoint IDs, event counts, and per-file statistics. |
| `--worktree ID` | Select one attached worktree; current is the default. |
| `--all-worktrees` | Show every attached worktree in the store. |
| `--stream ID` | Select one durable event stream. |
| `--session-limit N` | Include only the most recently active sessions; 0 shows all. Default: 0. |
| `--max-lanes N` | Maximum graph columns; 0 allows unlimited columns. Default: 8. |
| `--index` | Read the disposable index when available; falls back to durable data. |
| `--durable` | Explicitly read event logs and checkpoint refs, which is the default path. |
| `--no-pager` | Write directly even when the output is taller than the terminal. |

The alias `turnal graph` is equivalent to `turnal log`.

### `turnal show`

Show normalized events and checkpoint metadata for one turn.

```text
turnal show [latest|TURN|SESSION:TURN|SESSION:latest]
            [--raw] [--transcript] [--full] [--json]
```

| Flag | Description |
| --- | --- |
| `--raw` | Include referenced raw adapter hook records. |
| `--transcript` | Read matching text from the provider transcript path. |
| `--full` | Enable both `--raw` and `--transcript`. |
| `--json` | Emit the complete structured turn object. |

No target means latest. A bare turn number works only when it is unambiguous across sessions in the current worktree.

### `turnal diff`

Print the patch between the pre and post checkpoints of one turn.

```text
turnal diff SESSION:TURN [--json]
```

Diffs come from hidden Git snapshots, not provider-reported file changes. `--json` emits changed-file metadata and checkpoint contents in a structured document.

### `turnal blame`

Attribute lines to the completed turn and prompt that last changed them.

```text
turnal blame PATH[:LINE] [--session ID] [--verbose] [--json]
```

| Flag | Description |
| --- | --- |
| `--session ID` | Restrict replay history and the latest checkpoint to one session. |
| `--verbose` | Include prompt, tools, checkpoint ID, and origin metadata. |
| `--json` | Emit structured blame results. |

Only completed pre/post turn pairs participate. Uncheckpointed workspace edits are not considered, and binary files are not supported.

### `turnal reindex`

Rebuild the disposable SQLite index from event logs and checkpoint refs.

```text
turnal reindex [--quiet]
```

`--quiet` suppresses the rebuild summary.

### `turnal search`

Search indexed prompts, replies, tools, paths, and normalized event text.

```text
turnal search QUERY [--session ID] [--all-worktrees]
              [-n N] [--json]
```

| Flag | Description |
| --- | --- |
| `--session ID` | Restrict results to one session. |
| `--all-worktrees` | Search attached and imported worktrees instead of only the current one. |
| `-n, --limit N` | Maximum results; 0 shows all. Default: 20. |
| `--json` | Emit structured ranked results. |

Run `turnal reindex` after new activity. Every whitespace-separated query term must match.

### `turnal rollback`

Restore the source workspace to a selected checkpoint with a safety snapshot.

```text
turnal rollback --to SESSION:TURN[:pre|post] [--dry-run]
                [--workspace-git] [--from-worktree ID]
```

| Flag | Description |
| --- | --- |
| `--to TARGET` | Select a turn checkpoint, `chk_` checkpoint ID, or Git SHA prefix. |
| `--dry-run` | Resolve the target and print planned changes without writing. |
| `--workspace-git` | Restore captured `HEAD`, index, tracked changes, and untracked files. |
| `--from-worktree ID` | Select a cross-worktree checkpoint when the target is elsewhere or ambiguous. |

Omitting the phase selects post. Use `:pre` to return to the state before that turn. Workspace-Git mode requires `git_sync.enabled` during the target capture.

### `turnal replay`

Materialize checkpoints in an isolated directory without changing the source workspace.

```text
turnal replay checkout SESSION[:TURN[:pre|post]] [--path PATH]
turnal replay checkout SESSION:START..END
turnal replay next | prev | goto TARGET | diff | show | keep | stop | list
```

| Flag or command | Description |
| --- | --- |
| `--path PATH` | Choose the replay directory instead of the managed default. |
| `--worktree PATH` | Alias for `--path`; the two flags cannot be combined. |
| `replay diff --next` | Compare the current checkpoint with the next checkpoint. |
| `replay diff --workspace` | Compare the current checkpoint with the live source workspace. |
| `replay keep [PATH]` | Keep the managed replay directory or copy the current state to an empty path. |
| `replay remove [ID|PATH]` | Remove a selected replay session and its directory. |

Stopping removes the managed directory unless `turnal replay keep` marked it to be preserved.

### `turnal save`

Create an explicit checkpoint of the current captured workspace without committing to the project's Git history.

```text
turnal save [MESSAGE] [--json]
```

The optional message is descriptive metadata. The command prints the hidden Git commit that can be passed to `turnal rollback --to`. Manual saves do not contain workspace-Git sync state, so they always use checkpoint-mode rollback.

### `turnal verify`

Run the repository verifier contract against the live workspace or an isolated historical checkpoint.

```text
turnal verify [SESSION:TURN:pre|post] [--json]
```

With no target, checks run in the live workspace. Historical verification requires an explicit phase and never changes the source workspace. Failed checks exit with status 3; launch or infrastructure errors remain distinct in the report.

### `turnal fork`

Inspect fork readiness or execute a supervised command from the historical pre-turn workspace.

```text
turnal fork SESSION:TURN --dry-run [--json]
turnal fork SESSION:TURN|CASE_ID [--keep] [--no-replay-instruction]
            [--json] -- COMMAND [ARGS...]
```

`--dry-run` is read-only and distinguishes exact captured state from missing, live, or reauthorization-dependent context. Execution creates or reuses a Case, runs outside the source workspace, records a durable Attempt, and removes its temporary directory unless `--keep` is set. Bare `codex` and `codex exec` commands receive the captured instruction automatically; explicit command arguments are preserved.

### `turnal compare`, `turnal select`, and `turnal apply`

Review attempts against their common Case base, record the chosen result, and restore it safely.

```text
turnal compare CASE_ID [--patch ATTEMPT_ID] [--json]
turnal select CASE_ID ATTEMPT_ID [--json]
turnal apply CASE_ID [--attempt ATTEMPT_ID] [--dry-run] [--json]
```

Comparison reports command status, the frozen verifier outcome, aggregate additions and deletions, and per-file stats; `--patch` includes one full base-to-result patch. Selection is append-only and may be changed without altering attempts. Apply requires the live captured surface to equal the Case base, previews the restore with `--dry-run`, and creates a rollback safety checkpoint before an actual restore. Diverged workspaces are rejected because three-way application is not implemented.

### `turnal case` and `turnal task`

Create and inspect experimental immutable cases derived from recorded turns.

```text
turnal case create SESSION:TURN [--task TASK_ID] [--json]
turnal case show CASE_ID [--json]
turnal case delete CASE_ID --dry-run
turnal case delete CASE_ID --yes
turnal task show TASK_ID [--json]
```

Creating a case without `--task` creates a task identity and its initial revision. `--task` creates a sibling case only when the recorded instruction matches the task's applicable revision. `case show` includes linked attempt status, verifier outcome, and the current selection; `turnal fork` is the command that launches the isolated runner.

Cases retain their source and attempt sessions. `case delete` writes an irreversible tombstone that makes those sessions eligible for `turnal session drop`; use `--dry-run` to confirm and preview the target, then pass `--yes` to confirm deletion. Deletion refuses while any linked attempt is still running.

### `turnal recovery`

Inspect or resolve a rollback that was interrupted after its journal was written.

```text
turnal recovery status
turnal recovery resume --yes
turnal recovery restore-safety --yes
```

`resume` reapplies the recorded target and finalizes the rollback. `restore-safety` abandons that target and restores the safety checkpoint captured before rollback began.

### `turnal run`

Wrap a Codex process with independent pre/post safety checkpoints.

```text
turnal run [--quiet] [--skip-hook-install]
           [--bypass-hook-trust] -- codex [CODEX_ARGS...]
```

| Flag | Description |
| --- | --- |
| `--quiet` | Suppress wrapper status messages. |
| `--skip-hook-install` | Do not update `.codex/config.toml` before launch. |
| `--bypass-hook-trust` | Pass `--dangerously-bypass-hook-trust` to Codex for this invocation. |

The wrapper currently supports Codex only. Wrapper checkpoints still exist if hooks emit no prompt, tool, or assistant payloads.

### `turnal turn start` and `turnal turn finish`

Create explicit pre/post boundaries for an unsupported agent or a manual workflow.

```text
turnal turn start --session SESSION [--turn N]
turnal turn finish --session SESSION [--turn N]
```

Without `--turn`, `start` chooses the next turn number and `finish` closes the active turn. A manual turn participates in history, diff, replay, and rollback like a hook-captured turn, although its semantic transcript contains only events that were explicitly recorded.

### `turnal worktree`

Inspect and repair the Git worktrees attached to one physical Turnal store.

```text
turnal worktree list
turnal worktree attach --store PATH_TO_DOT_TURNAL
turnal worktree repair
```

`list` marks the current worktree and identifies the primary worktree. `repair` refreshes the worktree binding and user-state registry.

### `turnal merge`

Import immutable event streams and private refs from another Turnal store.

```text
turnal merge PATH [--dry-run] [--adopt-source-as-current-repo]
turnal merge --recover
turnal merge --abort
```

| Flag | Description |
| --- | --- |
| `--dry-run` | Validate and report the import without changing the destination. |
| `--adopt-source-as-current-repo` | Assert that a differing repo ID represents the same logical project. |
| `--recover` | Resume the single pending import journal. |
| `--abort` | Remove staging data for the single pending import journal. |

A successful merge rebuilds the destination index. Durable history remains imported if that rebuild fails.

### `turnal session drop`

Delete one session event log, stream metadata, temporary state, and related private refs.

```text
turnal session drop SESSION [--dry-run | --purge]
```

`--dry-run` reports the refs and files that would be deleted. Cases can retain source and attempt sessions until the Case is deleted. `--purge` also prunes unreferenced private refs and immediately garbage-collects hidden Git objects; it cannot be combined with `--dry-run`, and filesystem backups, sync history, and disk snapshots remain outside Turnal's deletion boundary. Without `--purge`, run `turnal reindex` afterward and use the staged retention commands when object bytes must be reclaimed.

### `turnal retention prune`

Delete private refs that no durable event, journal, active turn, or import manifest references.

```text
turnal retention prune [--dry-run]
```

`--dry-run` lists the count without deleting refs.

### `turnal maintenance gc`

Expire hidden-Git reflogs and immediately prune unreachable objects.

```text
turnal maintenance gc [--dry-run]
```

`--dry-run` prints the garbage-collection policy without invoking Git. Run this only after reviewing session-drop and retention-prune results.

### `turnal maintenance clear-hook-failures`

Acknowledge recorded fail-open hook capture errors after investigating them.

```text
turnal maintenance clear-hook-failures --yes
```

Hook capture failures do not block the agent, but `turnal status` retains them so missing history remains visible. Review the failures first; `--yes` confirms that review and clears the health ledger.

### `turnal store rekey`

Give a copied `.turnal` store new store, worktree, and producer identities.

```text
turnal store rekey --confirm
```

`--confirm` is required acknowledgement that this is a copied store. Existing events and checkpoint commits are not rewritten.

### `turnal destroy`

Remove Turnal metadata and optionally uninstall Turnal-owned hook commands.

```text
turnal destroy [--dry-run] [--remove-hooks]
               [--agent auto|claude|codex|all|none]
```

| Flag | Description |
| --- | --- |
| `--dry-run` | Show metadata and hook changes without deleting them. |
| `--remove-hooks` | Remove Turnal commands from supported agent hook configs. |
| `--agent VALUE` | Limit hook removal to selected adapters. Default: auto. |

Workspace source files are not deleted. Start with `--dry-run`.

### `turnal version`

Print version, release channel, build commit, and install source.

```text
turnal version [--json]
```

`--json` emits structured build metadata.

### `turnal upgrade`

Check or install the newest release while preserving the current channel by default.

```text
turnal upgrade [--check [--exit-code]] [--dry-run] [--json]
               [--stable | --nightly] [--yes]
```

| Flag | Description |
| --- | --- |
| `--check` | Check availability without installing. |
| `--exit-code` | With `--check`, exit 3 when an update is available. |
| `--dry-run` | Show the selected target and action without installing. |
| `--stable`, `--nightly` | Switch release channel. |
| `--yes` | Confirm a channel switch or downgrade non-interactively. |
| `--json` | Emit the upgrade plan as JSON. |

The alias `turnal update` is equivalent to `turnal upgrade`.

### `turnal completion`

Generate shell completion scripts.

```text
turnal completion bash|zsh|fish|powershell
```

---

## Troubleshooting

Run `turnal status` first. It is the fastest offline way to separate hook, store, identity, integrity, and unfinished-operation failures. Add `turnal status --probe-agent-capture` when static hook files look correct but capture still does not run in the provider surface.

<div class="troubleshooting-list not-typeset" data-not-typeset>
  <details open>
    <summary>Search says the index is missing, or recent turns are absent</summary>
    <p>Run <code>turnal reindex</code> from the workspace. The SQLite database is intentionally disposable and normal recording does not rebuild it after every turn.</p>
  </details>
  <details>
    <summary>Hooks are configured, but the agent is not being captured</summary>
    <p>Run <code>turnal status --probe-agent-capture</code>. For Codex app-server, review the reported discovery, enablement, and trust state in the host's hooks UI. For a Claude Agent SDK host, confirm that it loads project settings; Turnal cannot infer that host-controlled choice from the workspace.</p>
  </details>
  <details>
    <summary>Status reports earlier hook capture failures</summary>
    <p>Hook capture fails open so it cannot block the agent. Review the reported failures and any resulting history gaps, then acknowledge them with <code>turnal maintenance clear-hook-failures --yes</code>.</p>
  </details>
  <details>
    <summary>A linked worktree cannot find its Turnal store</summary>
    <p>From the linked worktree, run <code>turnal worktree attach --store /path/to/primary/.turnal</code>, then <code>turnal worktree repair</code> and <code>turnal status</code>.</p>
  </details>
  <details>
    <summary>A turn or checkpoint target is ambiguous</summary>
    <p>Use the explicit <code>SESSION:TURN:PHASE</code> form. For shared stores, add <code>--from-worktree ID</code>. Checkpoint and SHA prefixes must uniquely identify a target.</p>
  </details>
  <details>
    <summary>Workspace-Git rollback is unavailable</summary>
    <p>Git sync must have been enabled when the target was captured, the Turnal workspace root must equal the Git worktree root, and the repository must have an initial commit. Enable it with <code>turnal init --git-sync</code> or <code>git_sync.enabled = true</code> for future turns; use normal checkpoint rollback for older turns.</p>
  </details>
  <details>
    <summary>Provider transcript text cannot be loaded</summary>
    <p>The captured file must still exist under an allowed Claude Code or Codex config root, must not traverse a <code>.git</code> directory, and must be no larger than 64 MiB. Normalized Turnal events remain available without it.</p>
  </details>
  <details>
    <summary>An interrupted merge is reported</summary>
    <p>Inspect <code>turnal status</code>, then choose <code>turnal merge --recover</code> to resume the single pending journal or <code>turnal merge --abort</code> to remove its staging data.</p>
  </details>
  <details>
    <summary>A session cannot be dropped because a Case retains it</summary>
    <p>Confirm and preview the target with <code>turnal case delete CASE_ID --dry-run</code>, then use <code>turnal case delete CASE_ID --yes</code> only when the Case and its experimental record are no longer needed and no attempt is still running. Its source and attempt sessions then become eligible for <code>turnal session drop</code>.</p>
  </details>
  <details>
    <summary>Disk use remains after dropping a session</summary>
    <p>For staged cleanup, run <code>turnal reindex</code>, preview <code>turnal retention prune --dry-run</code>, then preview and run <code>turnal maintenance gc</code> only when you are ready to irreversibly prune unreachable objects. For immediate local cleanup during the drop, use <code>turnal session drop SESSION --purge</code> after reviewing a separate dry run.</p>
  </details>
</div>

Still stuck? Include `turnal version --json` and the non-sensitive parts of `turnal status` in a [GitHub issue](https://github.com/AadiJo/turnal/issues).
