---
name: turnal-fork-history
description: Rerun or branch from a task recorded by Turnal and compare the resulting attempts. Use when asked to retry a past agent turn, explore an alternative implementation from historical state, run A/B attempts, inspect fork readiness, compare or select Case attempts, or apply a chosen attempt. Covers source-turn discovery, isolated execution, Case/Attempt/Task identifiers, and exact-base application.
---

# Fork Turnal history

Fork from a captured pre-turn checkpoint into a supervised isolated workspace. The child does not run in the source workspace, but execution writes durable Case and Attempt records to the Turnal store. Read-only history questions belong to `$turnal-inspect-history`; reach for a fork only when something must be rerun.

## Find the source

1. Run `turnal status` and confirm the workspace has Turnal history.
2. Use `turnal sessions --json`, `turnal log`, or `turnal search --json "<query>"` to obtain a real session ID and turn number.
3. Inspect the source with `turnal show <session>:<turn>` and `turnal diff <session>:<turn>` when the user's intended base or task is unclear.
4. Copy every identifier from Turnal output.

Read [references/fork-arguments.md](references/fork-arguments.md) before constructing the fork command. It defines accepted targets, the `--` boundary, instruction replay, and JSON stream behavior.

## Preview readiness

Always begin with a non-executing readiness check:

```sh
turnal fork <session>:<turn> --dry-run --json
```

Inspect the pre-turn base, captured file count, instruction status, fidelity, and limitations. When the instruction is missing or redacted, ask the user for a prompt — raw storage is not a recovery path for it. A dry run accepts exactly one target and no child command.

## Execute an attempt

Run only after the user has asked to retry, branch, or experiment:

```sh
turnal fork <session>:<turn> -- codex exec
```

Place Turnal flags before `--` and the complete child command after it. Bare `codex` and `codex exec` commands receive the captured instruction automatically. If the child command already includes an explicit prompt, Turnal leaves it unchanged. Use `--no-replay-instruction` only when the child should not receive the capture, and provide an explicit prompt when needed.

Use `--keep` only when the isolated workspace must survive for inspection. With `--json`, parse stdout as the Turnal result and treat child output on stderr separately.

Record `result.case_id`, `result.attempt_id`, and `result.run_id` from JSON, or the `case:`, `attempt:`, and `run:` lines from human output. Read [references/cases-and-ids.md](references/cases-and-ids.md) before creating sibling Cases or using durable IDs.

## Compare and choose

1. Run `turnal compare <case-id> --json` to compare every completed Attempt with the same immutable base.
2. Request a full patch only for a candidate: `turnal compare <case-id> --patch <attempt-id>`.
3. Weigh command status, file changes, captured limitations, and frozen verifier results together; patch size alone decides nothing.
4. Record the choice with `turnal select <case-id> <attempt-id> --json` when the user asks to select an Attempt.

## Apply only with explicit authorization

Applying changes the live workspace. Do not treat a request to compare, recommend, or select as permission to apply.

Preview first:

```sh
turnal apply <case-id> --dry-run --json
```

Run `turnal apply <case-id>` only after the user explicitly asks to apply the selected Attempt. Use `--attempt <attempt-id>` to override the recorded selection only when the user identifies that Attempt. Apply is exact-base only: it refuses if the captured workspace surface differs from the Case base, creates a safety checkpoint before restoring, and never performs a three-way merge.
