# Turnal for VS Code

See which AI turn wrote the line under your cursor, retrace what your agents changed, and roll back a bad turn safely, all without leaving the editor.

Turnal reads from a local flight recorder of your agents’ work. The extension surfaces that history inside VS Code and hands every history operation to the `turnal` CLI, so the CLI stays the single source of truth for durable logs, checkpoints, blame, and safe rollbacks.

## What you get

- **Intent-first blame for the current line.** A quiet end-of-line note shows the agent, the problem it said it was solving, and the relative time for the line you’re on. Hover it for evidence, the separate human request, confidence, and links to the turn’s diff, details, and rollback.

- **Your agent history, two ways.** The Turnal view in the Activity Bar groups recorded turns and rollbacks by session, or flattens everything into a newest-first Recent Activity list. Both layouts are backed entirely by the CLI.

- **Native turn diffs.** Click a completed turn to open exactly what it changed in VS Code’s built-in diff, whether single-file or multi-file, with no custom viewer to learn.

- **Safe rollback, previewed first.** **Roll Back to Before Turn…** runs a dry run, checks for affected unsaved editors, and opens the exact file plan in VS Code’s native Changes editor before asking for confirmation. It verifies that plan again before touching the workspace.

Editor rollbacks always use Turnal’s checkpoint-only mode, even when `rollback.mode` is set to `workspace-git`. The extension never moves your project’s Git HEAD or index; use the CLI when you intentionally want the more invasive workspace-Git restore.

Recording, replay, verification, search, retention, worktree management, and configuration editing stay in the CLI by design. The extension also never reads `.turnal/index` or private Git objects directly, so its behavior tracks the installed CLI instead of coupling to internal storage.

## Screenshots

### Inline blame and turn details

Hover the current line to see the agent's stated problem, evidence, human request, timing confidence, action, session, turn, and links to the recorded changes. A missing, late, or out-of-scope intent is shown explicitly rather than reconstructed from the prompt.

![Inline blame hover showing the provenance behind the current line, with links to its diff, details, and rollback.](https://i.imgur.com/eMnXYM9.png)

### Native turn changes

Completed turns open as familiar, read-only multi-file changes in VS Code’s built-in diff editor.

![A completed agent turn opened in VS Code’s native multi-file Changes editor.](https://i.imgur.com/nHN4gNo.png)

### Native rollback preview

Before confirmation, Turnal opens the complete rollback plan against the live workspace in the same native changes UI.

![A rollback plan opened in VS Code’s native multi-file Changes editor before confirmation.](https://i.imgur.com/aOOe2gd.png)

## Requirements

- VS Code 1.95 or newer.
- A Turnal CLI that exposes `sessions` JSON schema 1, planned for CLI release 0.0.1.
- A workspace initialized with `turnal init` and at least one completed turn, so there’s history to blame and diff.

## Install the CLI

Install or update the release build with npm:

```sh
npm install -g @aadijo/turnal@latest
turnal version
```

If the installed CLI is older, the sidebar shows **Turnal CLI needs an update** and keeps the incompatibility visible instead of showing empty history.

## Commands and settings

- **Turnal: Refresh History** clears cached session and blame data.
- **Turnal: Show Recent Activity** switches the sidebar to a newest-first list across sessions.
- **Turnal: Group by Session** restores the session hierarchy.
- **Turnal: View Turn Changes** opens a completed turn in the native diff or multi-file Changes editor.
- **Turnal: Show Turn Details** opens the CLI’s readable `turnal show` output.
- **Turnal: Roll Back to Before Turn…** previews and performs a checkpoint rollback.
- **Turnal: Toggle Inline Blame** flips `turnal.inlineBlame.enabled` for the workspace; the sidebar icon reflects the current state.
- `turnal.history.layout` persists the sidebar layout as `sessions` or `activity`.
- `turnal.cliPath` selects the CLI executable and defaults to `turnal`.

Inline blame is suppressed when the editor text differs from the latest completed checkpoint. Missing attribution is safer than pinning a turn to shifted or locally edited lines.

## Develop

```sh
cd apps/vscode
npm install
npm test
```

Open `apps/vscode` as the VS Code workspace and press `F5` to launch the Extension Development Host. To use a repository build of the CLI, set `turnal.cliPath` to the absolute path of `bin/turnal` after running `make build` at the repository root.

Package a local VSIX with:

```sh
npm run package
```
