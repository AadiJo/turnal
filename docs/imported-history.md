# Imported history

`turnal import` converts local Claude Code or Codex transcripts for the current workspace into Turnal's append-only event model. Discovery defaults to `~/.claude/projects` or `$CLAUDE_CONFIG_DIR/projects` for Claude Code and `~/.codex/sessions` or `$CODEX_HOME/sessions` for Codex. Without `--session`, only files modified during the last 30 days are considered. `--path` replaces the provider's default transcript directory.

Always preview a conversion when the transcript source is unfamiliar:

```sh
turnal import claude-code --dry-run
turnal import codex --session <provider-session-id> --dry-run --json
```

The preview parses every candidate, checks that its recorded working directory identifies the current Turnal workspace, assigns stable Turnal turn ids, and checks existing source identities for collisions. It does not append durable events. A real import uses the same plan while holding the normal per-session capture lock.

## Evidence boundary

Imported sessions record `origin: imported` and `read_only: true`. Prompt, assistant, tool-call, and tool-result events pass through the current `secrets.store_prompts` and `secrets.store_tool_io` settings. Provider developer or system instructions are not converted into user prompts.

No workspace snapshot is reconstructed from a transcript. Imported turns therefore remain event-only evidence:

- `turnal sessions`, `turnal show`, `turnal log`, Prism, and local search can inspect them.
- `turnal diff`, rollback, exact replay, line blame, and shared-history publication require native checkpoint evidence and stay unavailable.
- If native capture later continues the same provider session, its next turn number advances past imported event-only turns.

Each converted event has a deterministic provider source identity. Repeating the same import skips matching events. If a provider reuses an identity for different content, Turnal reports a collision instead of silently replacing append-only history.

## Commit attachment

`turnal session attach <session-id>` resolves `HEAD` by default; use `--commit <revision>` to choose another existing commit. If the session has not been imported, `--adapter claude-code` or `--adapter codex` discovers and imports its transcript first. The default `--adapter auto` checks both supported stores.

An attachment is an integrity-checked `session.attach` event containing the resolved full commit id and `history_rewritten: false`. It does not amend the commit message, add Git notes, update refs, stage files, or alter the source worktree. Reattaching the same session and commit is idempotent, including when a different revision spelling resolves to the same commit.
