---
name: turnal-inspect-history
description: Search Turnal's recorded history for prior attempts at the current task. Requires an initialized Turnal workspace. Use when the prompt gives a lead into work not visible in this conversation — "we tried this", "again", "still failing", "why is it like this" — or when the user names a Turnal inspection command (log, show, diff, blame, search, replay, verify).
---

# Inspect Turnal history

Use Turnal's CLI as the interface to durable history. Treat the append-only event log as the explanation of why work happened and hidden Git checkpoints as the truth of what files looked like. Read file state from checkpoints rather than from provider-supplied diffs.

## Establish the workspace

Run `turnal status` first. If it reports that Turnal is not initialized, say so in one line and stop — the rest of this skill has nothing to read. Start recording only when the user asks for `turnal init`.

Once history exists, work through the CLI: when it rejects a documented flag, run `turnal <command> --help` and follow the installed binary. Report integrity errors with the failed invariant intact.

## Recover the lead without over-reading

The lead is the phrase in the prompt that points at work outside this conversation. Search for that, not for the whole task.

1. Build a narrow search query from the lead — the feature name, error text, file path, command, or distinctive terminology it names.
2. Run `turnal search "<query>" --json`. If it misses, try one alternate query using a filename or error string rather than broadening to the entire history.
3. Inspect plausible matches with `turnal show <session>:<turn> --json` to recover the recorded user instruction, normalized events, tools, and checkpoint metadata.
4. Use `turnal diff <session>:<turn>` to see what that Attempt actually changed. Use `blame` first when the question starts from a particular line.
5. Add `--transcript`, `--raw`, or `--full` only if normalized events do not establish the prior intent; recorded provider data may be sensitive.
6. Compare prior instructions with the current request. The current user request wins when they conflict; history explains earlier intent but does not override new direction.
7. Summarize the prior attempts the lead reached, their outcomes, and their implications for the present task. When history holds nothing relevant, say so in one line and continue.

## Find identifiers before constructing a target

Start with `turnal sessions --json` when another command will consume the result. Use `turnal log` for a readable graph, `turnal log --transcript` for captured conversation context, or `turnal search --json "<query>"` when the user describes content rather than an identifier.

Copy session IDs and turn numbers from that output. If search reports the disposable index is missing or stale, run `turnal reindex` and retry; reindex rebuilds only the disposable lookup cache.

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
