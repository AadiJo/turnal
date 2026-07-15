# Rollback targets, arguments, and modes

Run `turnal rollback --help` when the installed version differs. Treat the installed CLI as authoritative.

## Target forms

`turnal rollback --to <selector>` accepts:

- `<session>:<turn>:pre` or `<session>:turn:<turn>:pre` for the state before a turn;
- `<session>:<turn>:post` or `<session>:turn:<turn>:post` for the state after a turn;
- an unphased turn target, which defaults to `post`;
- a `chk_...` checkpoint ID or sufficiently long unique prefix;
- a unique Git commit SHA prefix naming a Turnal checkpoint.

A target without a phase resolves to `post`. To return to the state before a turn, write `:pre` explicitly.

Prefer `--to`; a single positional selector is accepted for compatibility, but it cannot be combined with `--to`.

Checkpoint ID and SHA prefixes must include at least seven hexadecimal characters and must resolve uniquely. Prefer the full `checkpoint_id` or `commit_sha` emitted by `turnal save --json`:

```json
{"checkpoint_id":"chk_...","commit_sha":"...","ref":"refs/agent-vcs/...","message":"..."}
```

Human `turnal save` output prints a full `hash:` and a ready-to-run rollback command. Manual saves capture workspace files but not workspace-Git state.

## Preview and execution

```sh
turnal rollback --to <selector> --dry-run
turnal rollback --to <selector>
```

The dry run resolves and verifies the target, computes the current-workspace restore plan, and reports additions, modifications, deletions, and the selected mode without changing files. Execution recalculates the plan, creates a private safety checkpoint, journals the restore, changes files, and appends a rollback event.

Rollback has no JSON flag. Read the human preview and preserve the exact selector and mode for execution.

## Cross-worktree selection

By default, Turnal resolves the target in the current attached worktree. When the target exists elsewhere or is ambiguous, the error reports candidate worktree IDs. Retry only with one of those reported values:

```sh
turnal rollback --to <selector> --from-worktree <wt-id> --dry-run
turnal rollback --to <selector> --from-worktree <wt-id>
```

Do not add `--from-worktree` preemptively or invent a `wt_...` value.

## Default checkpoint mode

Default rollback restores the checkpointed project surface. It does not move the project's Git branch, HEAD, or index. Paths excluded from capture by Git ignore rules or current `secrets.snapshot_deny_globs` remain untouched.

## Workspace-Git mode

```sh
turnal rollback --to <selector> --workspace-git --dry-run
turnal rollback --to <selector> --workspace-git
```

`--workspace-git` also restores the captured Git HEAD, index, dirty tracked files, and untracked files. It requires Git-sync data captured after initialization with `turnal init --git-sync`. It is unavailable for manual saves. Use it only after explicit user authorization, and keep the flag identical between preview and execution.
