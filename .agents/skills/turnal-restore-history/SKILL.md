---
name: turnal-restore-history
description: Preview and safely restore a workspace from Turnal history. Use when explicitly asked to roll back, restore, revert, or return files to a recorded turn or manual checkpoint, or when a previous rollback was interrupted. Covers pre/post target selection, manual checkpoint IDs and commit hashes, cross-worktree disambiguation, optional workspace-Git restoration, dry runs, safety checkpoints, and recovery journals.
---

# Restore Turnal history

Treat rollback as a live-workspace mutation. Diagnose and preview freely, but perform a rollback or recovery action only when the user explicitly asks to change the workspace.

## Establish safety state

1. Run `turnal status`.
2. If it reports a pending rollback journal, do not start another rollback. Follow [references/recovery.md](references/recovery.md).
3. Find real session and turn identifiers with `turnal sessions --json` or `turnal log`. For a manual checkpoint, copy the `checkpoint_id` or `commit_sha` from `turnal save --json` output.
4. Read [references/rollback-arguments.md](references/rollback-arguments.md) before choosing a target or mode.

## Resolve the user's intent to a phase

- Use `:pre` to restore the state before a turn or to undo that turn's captured file changes.
- Use `:post` to restore the state after a turn.
- Write the phase explicitly every time you translate natural-language intent. Turnal defaults an unphased rollback target to `:post`, which is wrong for requests such as “undo turn 7.”
- If the requested before/after state is ambiguous and either choice could lose work, ask the user which state they mean before performing the rollback.

## Preview every rollback

Run the same target and mode intended for execution with `--dry-run`:

```sh
turnal rollback --to <session>:<turn>:pre --dry-run
```

Review the resolved target, worktree, mode, and every add/modify/delete action. Explain that ignored files and paths denied by current secret globs are left untouched, so checkpoint rollback restores the captured project surface rather than every byte under the workspace.

If Turnal reports a cross-worktree or ambiguous target, copy one of the reported `wt_...` identifiers and retry with `--from-worktree <worktree-id>`.

## Perform only the reviewed operation

After explicit authorization, rerun the previewed command without `--dry-run`:

```sh
turnal rollback --to <session>:<turn>:pre
```

Turnal creates a private safety snapshot before restoring and records the rollback. Preserve the printed safety ref and commit until the user confirms the workspace is correct.

## Use workspace-Git mode only when requested

Default checkpoint rollback restores captured files without moving the project's branch, HEAD, or index. `--workspace-git` additionally restores captured Git HEAD, index, dirty tracked files, and untracked files, so it is materially more invasive.

Use `--workspace-git` only when the user explicitly wants Git state restored and the target was captured after `turnal init --git-sync`. Preview with the flag and execute with the same flag. Manual saves do not contain Git-sync state and cannot be restored with `--workspace-git`.

## Report the result

State the exact target and mode restored, summarize changed paths, and include the safety ref and commit. If any restore fails after creating a safety checkpoint, stop and move to the recovery workflow instead of retrying ad hoc or deleting the journal.
