# Turnal for VS Code

Turnal’s VS Code extension puts the useful parts of the local flight recorder in the editor without recreating the CLI. It shells out to `turnal` for every history operation, so the CLI remains responsible for validating durable logs, resolving checkpoints, computing blame, and performing safe rollbacks.

## What’s in the first cut

- The current line shows a quiet end-of-line annotation with the agent, prompt, turn, and relative time. Hover any attributed line for the full turn context and links to its diff, details, and rollback action.
- The Turnal Activity Bar view groups recorded turns by session. Clicking a completed turn opens its unified diff in a read-only, syntax-highlighted virtual document.
- **Roll Back to Before Turn…** runs `turnal rollback --to <session>:turn:<n>:pre --dry-run`, checks for affected unsaved editors, shows the file plan in a native confirmation dialog, and verifies the plan again before it runs the real rollback.
- **Show Turn Details** opens the CLI’s readable `turnal show` output without a custom webview.

The extension intentionally leaves recording controls, replay, verification, search, retention, worktree management, and configuration editing in the CLI.

## Requirements

- VS Code 1.95 or newer.
- A `turnal` executable on `PATH`, or an explicit `turnal.cliPath` setting.
- A workspace initialized with `turnal init` and at least one completed turn for blame and diffs.

The extension never reads `.turnal/index` or private Git objects directly. This keeps its behavior aligned with the installed CLI and avoids coupling the UI to internal storage schemas.

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

## Commands and settings

- **Turnal: Refresh Sessions** clears cached session and blame data.
- **Turnal: Open Turn Diff** opens a completed turn’s unified diff.
- **Turnal: Show Turn Details** opens the CLI’s turn view.
- **Turnal: Roll Back to Before Turn…** previews and performs a checkpoint rollback.
- **Turnal: Toggle Inline Blame** updates `turnal.inlineBlame.enabled` for the workspace.
- `turnal.cliPath` selects the CLI executable and defaults to `turnal`.

Inline blame is suppressed when the editor text differs from the latest completed checkpoint. Missing attribution is safer than showing a turn against shifted or locally edited lines.
