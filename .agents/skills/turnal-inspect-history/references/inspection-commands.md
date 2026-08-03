# Inspection command arguments

Use this reference after choosing an inspection operation. Run `turnal <command> --help` when the installed version differs; CLI help is authoritative for that binary.

## Discover sessions and turns

```sh
turnal sessions [--json]
turnal log [--session <session>] [--limit <n>] [--session-limit <n>]
turnal log [--transcript] [--verbose] [--durable|--index]
turnal search <query> [--session <session>] [--limit <n>] [--json] [--all-worktrees]
```

- Prefer `sessions --json` when copying identifiers into later commands.
- `log` is the readable checkpoint graph and has no JSON mode. `--all-worktrees` broadens it beyond the current worktree; `--worktree <wt-id>` selects one worktree. `--limit 0` and `--session-limit 0` mean unlimited. `--max-lanes` defaults to 8; `0` allows unlimited columns.
- `log --durable` reads durable logs/checkpoints. `log --index` explicitly selects the disposable index. Do not pass both.
- `search` requires a query, uses the disposable SQLite index, defaults to 20 results, and accepts `--limit 0` for all. `--all-worktrees` broadens the search beyond the current worktree. Run `turnal reindex` if Turnal reports a missing or stale index.

## Inspect one turn

```sh
turnal show [<turn>|latest|<session>:<turn|latest>] [--json]
turnal show <turn-ref> [--transcript|--raw|--full]
```

- No argument and `latest` both resolve the latest turn in the current worktree.
- `--transcript` includes captured assistant text. `--raw` includes referenced raw adapter records. `--full` includes both.
- Escalate to raw/full data only when normalized events cannot answer the question; captured provider data may be large or sensitive.

## Inspect file changes

```sh
turnal diff <session>:<turn> [--json]
turnal blame <path>[:line] [--session <session>] [--verbose] [--json]
```

- `diff` recomputes the pre-to-post change from hidden Git checkpoints. With `--json`, a target is required and file contents are base64 fields; binary or files larger than the document limit may be marked binary/truncated instead of carrying contents.
- `blame` uses the latest completed post checkpoint, not uncheckpointed workspace edits. Without `:line`, it reports every line. `--session` scopes both replay history and the latest checkpoint. JSON keeps the agent's stated problem under `origin.intent` and the human request under `origin.prompt`; inspect `status`, `timing`, and `confidence` rather than treating the statement as verified truth.

## Replay historical files

```sh
turnal replay list
turnal replay checkout <target> [--path <path>]
turnal replay show [--json|--transcript|--raw|--full]
turnal replay diff [--next|--workspace]
turnal replay prev|next
turnal replay goto <target>
turnal replay keep [<copy-path>]
turnal replay stop
turnal replay remove [<session-or-path>]
```

- `--next` and `--workspace` are mutually exclusive.
- `keep` preserves or copies the current replay; `stop` removes the active replay unless it was kept.
- `--path` and the legacy `--worktree` flag both select a replay path and cannot be combined.

## Verify state

```sh
turnal verify
turnal verify <session>:<turn>:<pre|post> [--json]
```

- With a target, Turnal materializes the recorded state in an isolated temporary directory and runs the repository's current verifier declarations.
- Without a target, verifier commands run directly in the mutable workspace and may change it. Use live verification only when that behavior is intended.
- Exit `0` means all checks passed; `3` means one or more checks failed, timed out, or failed to launch; `1` means an operational or invariant error prevented a valid verification.
