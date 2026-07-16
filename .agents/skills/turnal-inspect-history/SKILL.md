---
name: turnal-inspect-history
description: Inspect Turnal history before acting when a request may have been tried before, revisits existing work, refers to a prior decision without enough context, or touches unfamiliar code whose intent may be recorded. Use to recover exactly what the user previously asked for, what agents attempted, why an approach was chosen or abandoned, what files changed, whether checks passed, and which turn introduced a line. Also use for explicit history, diff, blame, replay, or recorded-state questions. Skip only when the task is clearly new and history cannot materially affect the approach.
---

# Inspect Turnal history

Use Turnal's CLI as the interface to durable history. Treat the append-only event log as the explanation of why work happened and hidden Git checkpoints as the truth of what files looked like. Do not infer file state from provider-supplied diffs.

## Establish the workspace

1. Run `turnal status` before querying history.
2. If Turnal is not initialized, report that no Turnal history is available. Do not run `turnal init` unless the user asks to start recording.
3. If the installed command rejects a documented flag, run `turnal <command> --help` and follow the installed CLI. Never guess a replacement flag.
4. Do not read or modify `.turnal/` directly to bypass the CLI. Report integrity errors with the failed invariant intact.

## Look back before editing when history may matter

Treat history as project context, not merely an audit tool. Check it before implementation when the request:

- sounds like a retry or continuation, including “again,” “continue,” “revisit,” “still failing,” or “we tried this”;
- asks why existing code, architecture, or behavior is the way it is;
- refers to an earlier decision, implementation, conversation, or user preference that is absent from the current prompt;
- changes unfamiliar or partially completed code where a recorded instruction may explain the intended outcome;
- concerns a regression, recurring error, abandoned approach, or feature that may already have an Attempt;
- would benefit from knowing whether a similar solution failed checks or was rejected before.

Do not require the user to say “check history.” If past context could materially change the implementation, run one focused search before editing. Do not delay clearly greenfield work with speculative archaeology.

## Recover relevant context without over-reading

1. Build a narrow search query from the user's feature name, error text, file path, command, or distinctive terminology.
2. Run `turnal search "<query>" --json`. If needed, try one alternate query using a filename or error string rather than broadening to the entire history.
3. Inspect plausible matches with `turnal show <session>:<turn> --json` to recover the recorded user instruction, normalized events, tools, and checkpoint metadata.
4. Use `turnal diff <session>:<turn>` to see what that Attempt actually changed. Use `blame` first when the question starts from a particular line.
5. Add `--transcript`, `--raw`, or `--full` only if normalized events do not establish the prior intent; recorded provider data may be sensitive.
6. Compare prior instructions with the current request. The current user request wins when they conflict; history explains earlier intent but does not override new direction.
7. Summarize only the relevant prior attempts, outcomes, and implications before proceeding with the present task. If no relevant history is found, say so briefly and continue normally.

## Find identifiers before constructing a target

Start with `turnal sessions --json` when another command will consume the result. Use `turnal log` for a readable graph, `turnal log --transcript` for captured conversation context, or `turnal search --json "<query>"` when the user describes content rather than an identifier.

Copy session IDs and turn numbers from output. Never fabricate them. If search says the disposable index is missing or stale, run `turnal reindex` and retry; reindex rebuilds only the disposable lookup cache.

Read [references/target-syntax.md](references/target-syntax.md) before passing a target to `show`, `diff`, `verify`, or `replay`. These commands intentionally accept different target shapes.

## Choose the narrowest inspection

- Use `turnal show <turn-ref> --json` for normalized events and checkpoint metadata. Add `--transcript`, `--raw`, or `--full` only when the question requires provider text or raw adapter records; these may expose recorded prompts, tool I/O, or secrets.
- Use `turnal diff <session>:<turn>` for the durable pre-to-post patch. Use `--json` when reasoning about paths or contents programmatically.
- Use `turnal blame <path>:<line> --json` for line provenance. Here the final colon introduces a line number, not a checkpoint phase.
- Use `turnal search "<query>" --json` to locate turns by indexed text, then confirm the result with `show` or `diff`.
- `turnal log` reads durable logs and checkpoints by default. Pass `--index` only to opt into the disposable index; use `--durable` to force durable reads if index mode would otherwise be selected.

Read [references/inspection-commands.md](references/inspection-commands.md) for exact arguments, flags, and selection guidance.

## Inspect a checkpoint in isolation

Use replay when the agent needs to browse files at a checkpoint or move through several checkpoints without disturbing the source workspace:

```sh
turnal replay checkout <session>:<turn>:post
turnal replay show
turnal replay diff
turnal replay prev
turnal replay next
turnal replay stop
```

`replay checkout` creates an isolated worktree and replay metadata. Run `turnal replay list` before creating another replay, and stop or remove the replay when finished unless the user wants it kept.

## Verify historical state

Use `turnal verify <session>:<turn>:<pre|post> --json` to run repository-defined checks against an isolated recorded state. The phase is mandatory. Prefer this over live `turnal verify` for historical questions because configured live checks run in the mutable workspace and may modify it.

Interpret exit codes precisely: `0` means every configured check passed, `3` means at least one check failed, timed out, or could not start, and `1` means Turnal could not validate the configuration, resolve/materialize the target, or clean up correctly.

## Report findings

Separate observed event evidence from checkpoint-derived file evidence. State the exact session, turn, and phase used; disclose redacted or missing context; and avoid claiming that a passing verifier proves more than the configured checks establish.
