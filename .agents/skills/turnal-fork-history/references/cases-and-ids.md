# Cases, Attempts, Tasks, and durable IDs

## Do not construct durable IDs

Turnal durable IDs are random values with a typed prefix and 32 hexadecimal characters, for example `case_0123456789abcdef0123456789abcdef`. Always copy them from CLI or JSON output.

| Prefix | Meaning | Find or use it with |
|---|---|---|
| `case_` | One immutable set of experimental conditions | fork output, `case create`, `case show`, `fork`, `compare`, `select`, `apply` |
| `attempt_` | One observable execution against a Case | fork output, `case show`, `compare`, `select`, `compare --patch`, `apply --attempt` |
| `task_` | One evolving human problem episode that may group Cases | `case create` output, `case show`, `task show`, `case create --task` |
| `run_` | One supervised process execution | fork output, Case/Attempt details |
| `chk_` | One canonical checkpoint identity | save JSON, checkpoint/rollback output, `rollback --to` |
| `wt_` | One attached worktree identity | `turnal worktree list`, ambiguity errors, `rollback --from-worktree` |

If an ID is unavailable, return to the command that emits it. Do not scrape `.turnal/` or guess. A source turn can be passed directly to `fork`; Turnal creates a Case when none exists and reuses the sole matching Case. If multiple Cases match, it requires an explicit Case ID. If that ID was not retained and no CLI output exposes it, report the discovery gap rather than bypassing the CLI.

## Object model

- A Task represents an evolving human problem and preserves observable instruction revisions.
- A Case freezes one source turn, pre-turn base checkpoint, observable instruction, verifier definitions, scope, and limitations.
- An Attempt records one execution against a Case, including the child command, result checkpoint, exit status, and verifier report.
- Selection records which completed Attempt is preferred. Application is a separate, explicit live-workspace mutation.

## Case workflow and arguments

```sh
turnal case create <session>:<turn> [--json]
turnal case create <session>:<turn> --task <task-id> [--json]
turnal case show <case-id> [--json]
turnal task show <task-id> [--json]
turnal compare <case-id> [--json]
turnal compare <case-id> --patch <attempt-id>
turnal select <case-id> <attempt-id> [--json]
turnal apply <case-id> [--attempt <attempt-id>] --dry-run [--json]
turnal apply <case-id> [--attempt <attempt-id>] [--json]
```

Use `case create` when the user wants to freeze experimental conditions before executing. Ordinary `fork <session>:<turn> -- <command>` creates or reuses a suitable Case automatically.

Use `--task <task-id>` only to create a sibling Case for the same observable problem under an existing Task. Turnal rejects a source whose observable instruction does not match the Task's current revision.

`compare` summarizes each Attempt against the common Case base. `--patch` accepts exactly one Attempt ID and includes that Attempt's full base-to-result patch.

`select` requires a completed Attempt belonging to the Case. `apply` uses the selected Attempt unless `--attempt` names another completed Attempt. Always dry-run application; it requires an exact base and does not merge.
