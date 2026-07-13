# Turnal for VS Code

Turnal’s VS Code extension puts the useful parts of the local flight recorder in the editor without recreating the CLI. It shells out to `turnal` for every history operation, so the CLI remains responsible for validating durable logs, resolving checkpoints, computing blame, and performing safe rollbacks.

## What’s in the first cut

- The current line shows a quiet end-of-line annotation with the agent, prompt, turn, and relative time. Hover any attributed line for the full turn context and links to its diff, details, and rollback action.
- The Turnal Activity Bar view can group recorded turns and rollbacks by session or show every event in a newest-first Recent Activity list. Clicking a completed turn opens its changes in VS Code's native diff UI, while clicking a rollback opens its recorded target and file summary.
- **Roll Back to Before Turn…** runs `turnal rollback --to <session>:turn:<n>:pre --dry-run`, checks for affected unsaved editors, shows the file plan in a native confirmation dialog, and verifies the plan again before it runs the real rollback.
- **Show Turn Details** opens the CLI’s readable `turnal show` output without a custom webview.

The extension intentionally leaves recording controls, replay, verification, search, retention, worktree management, and configuration editing in the CLI.

## Requirements

- VS Code 1.95 or newer.
- A Turnal CLI that exposes `sessions` JSON schema 1, planned for CLI release 0.0.1.
- A workspace initialized with `turnal init` and at least one completed turn for blame and diffs.

The extension never reads `.turnal/index` or private Git objects directly. This keeps its behavior aligned with the installed CLI and avoids coupling the UI to internal storage schemas.

## Install the CLI

Install or update the release build with npm:

```sh
npm install -g @aadijo/turnal@latest
turnal version
```

The compatible 0.0.1 CLI has not reached npm's `latest` channel yet. Until it does, build this repository's `vscode-extension` branch and point the extension at that binary:

```sh
git clone https://github.com/AadiJo/turnal.git
cd turnal
git switch vscode-extension
make build
./bin/turnal sessions --json
```

The JSON response must contain `"schema_version": 1`. Set **Turnal: CLI Path** to the absolute path of `bin/turnal`, or add the binary to `PATH`. The binary must be available in the environment running the extension host, which means a WSL, SSH, or Dev Container window needs a CLI installed inside that remote environment rather than only on the local machine.

If the installed CLI is older, the sidebar says **Turnal CLI needs an update** and keeps the incompatibility visible instead of displaying an empty history.

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

- **Turnal: Refresh History** clears cached session and blame data.
- **Turnal: Show Recent Activity** switches the sidebar to a newest-first list across sessions.
- **Turnal: Group by Session** restores the session hierarchy.
- **Turnal: View Turn Changes** opens a completed turn in VS Code’s native diff or multi-file Changes editor.
- **Turnal: Show Turn Details** opens the CLI’s turn view.
- **Turnal: Roll Back to Before Turn…** previews and performs a checkpoint rollback.
- **Turnal: Toggle Inline Blame** updates `turnal.inlineBlame.enabled` for the workspace; the sidebar icon changes between visible and hidden states.
- `turnal.history.layout` persists the sidebar layout as `sessions` or `activity`.
- `turnal.cliPath` selects the CLI executable and defaults to `turnal`.

Inline blame is suppressed when the editor text differs from the latest completed checkpoint. Missing attribution is safer than showing a turn against shifted or locally edited lines.

Editor rollbacks always use Turnal’s checkpoint-only mode, even if `rollback.mode` is configured as `workspace-git`. The extension won’t move the project’s Git HEAD or index; use the CLI when you intentionally need the more invasive workspace-Git restore.
