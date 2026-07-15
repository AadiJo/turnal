# Fork targets and arguments

Run `turnal fork --help` when the installed version differs. Never guess at flags or identifiers.

## Accepted targets

Use one of:

- `<session>:<turn>`
- `<session>:turn:<turn>`
- a full `case_...` ID already emitted by Turnal

Turn targets must not include `:pre` or `:post`; fork always uses the recorded pre-turn checkpoint as its base. A Case target reuses that Case's frozen base, instruction, verifier contract, and limitations.

## Dry-run form

```sh
turnal fork <turn-target|case-id> --dry-run [--json]
```

`--dry-run` takes exactly one positional argument and no child command. It performs readiness analysis without creating files or running an agent. Inspect:

- `target` and `source` for the resolved turn;
- `base.status`, checkpoint ref/commit, and captured file count;
- `instruction.status` and text availability;
- `readiness`, `fidelity_level`, and listed limitations.

Missing or redacted instructions require new user input and are never recovered from raw storage.

## Execution form

```sh
turnal fork <turn-target|case-id> [--keep] [--json] [--no-replay-instruction] -- <command> [args...]
```

`--` separates Turnal's flags from the child command. Everything after `--` belongs to the child. A real fork requires the target plus at least one child-command token.

Examples:

```sh
# Turnal appends the captured instruction.
turnal fork <session>:<turn> -- codex exec

# This explicit prompt is left unchanged.
turnal fork <session>:<turn> -- codex exec "Try a smaller implementation"

# A custom runner can consume exported provenance.
turnal fork <session>:<turn> -- sh -c 'my-runner "$TURNAL_FORK_INSTRUCTION"'
```

Bare `codex` and `codex exec` receive the captured instruction as a final argument unless `--no-replay-instruction` is set. Other child commands receive no automatic prompt; use the exported environment when needed.

The child receives these provenance variables:

- `TURNAL_FORK_CASE_ID`
- `TURNAL_FORK_ATTEMPT_ID`
- `TURNAL_FORK_RUN_ID`
- `TURNAL_FORK_SOURCE`
- `TURNAL_FORK_BASE_COMMIT`
- `TURNAL_FORK_INSTRUCTION`

`--keep` preserves the isolated Attempt workspace; without it, Turnal removes the workspace after execution. The source workspace remains unchanged in either case.

With `--json`, Turnal writes the structured result to stdout and redirects the child's stdout to stderr so stdout stays parseable. Read `case_created`, `result.case_id`, `result.attempt_id`, `result.run_id`, `result.status`, result commits, verification, and `result.workspace_kept` from stdout only.
