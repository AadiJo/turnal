# Turnal target syntax

Turnal does not have one universal turn-reference grammar. Choose the shape required by the receiving command and copy every identifier from Turnal output.

## Command matrix

| Command | Accepted target examples | Phase rule |
|---|---|---|
| `turnal show` | omitted, `latest`, `7`, `sess_a:7`, `sess_a:latest` | `pre`/`post` forbidden; `:turn:` spelling rejected |
| `turnal diff` | `sess_a:7`, `sess_a:turn:7` | phase forbidden |
| `turnal fork` | `sess_a:7`, `sess_a:turn:7`, or a `case_...` ID | phase forbidden |
| `turnal case create` | `sess_a:7`, `sess_a:turn:7` | phase forbidden |
| `turnal verify` | `sess_a:7:pre`, `sess_a:turn:7:post` | phase required |
| `turnal rollback --to` | `sess_a:7`, `sess_a:7:pre`, `sess_a:turn:7:post`, `chk_...`, or a commit SHA prefix | phase optional, defaults to `post` |
| `turnal replay checkout` / `goto` | session, turn, phased turn, or range forms described below | phase optional for a single turn |

`<session>:<turn>` and `<session>:turn:<turn>` are equivalent for `diff`, `fork`, and `case create`. Turnal commonly prints the canonical `:turn:` form, but `show` deliberately uses its own shorter resolver.

## `show` references

- Omit the argument or pass `latest` for the latest turn in the current worktree.
- Pass a bare positive turn number only when it is unique across sessions; Turnal errors if it is ambiguous.
- Pass `<session>:latest` for the most recent turn in one session.
- Pass `<session>:<turn>` for a precise turn.
- Do not pass `:pre`, `:post`, or the word `turn`.

## Turn targets without a phase

Use `<session>:<turn>` or `<session>:turn:<turn>` with `diff`, `fork`, and `case create`. These commands operate on the turn as a unit or choose the pre/post checkpoints internally, so they reject a supplied phase.

## Checkpoint targets with a phase

`pre` means the captured state before the turn; `post` means the state after it.

- `verify` requires the phase: `<session>:<turn>:<pre|post>` or `<session>:turn:<turn>:<pre|post>`.
- `rollback` accepts an omitted phase but defaults it to `post`. Write the phase explicitly whenever changing the workspace.
- A single-turn replay may include a phase. If omitted, replay chooses its sequence position according to the replay selection.

## Replay selections

Replay also accepts broader selections:

- `<session>`: all available checkpoints for the session.
- `<session>:<turn>[:pre|post]`: one turn, short form.
- `<session>:turn:<turn>[:pre|post]`: one turn, canonical form.
- `<session>:<start>..<end>` or `<session>:turn:<start>..<end>`: an inclusive turn range; ranges cannot include a phase.

Use `turnal replay list` to find existing replay sessions before creating another one.
