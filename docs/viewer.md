# Turnal Prism local viewer

Turnal Prism is the browser interface launched by `turnal ui`. It shows every project recorded on this machine, and explores the same durable history the CLI uses: the append-only event log explains activity, private checkpoint Git commits provide file state, and per-project SQLite remains an optional disposable index.

## Launch

```sh
turnal ui
turnal ui --no-open
turnal ui --port 0
turnal ui --project <path>
turnal ui --session <session-id>
```

`turnal ui` runs from any directory, including one that has no Turnal store. Launched inside a recorded project, that project is preselected; launched anywhere else, the project index opens. `--session` needs a recorded project, so it must be run inside one or combined with `--project`.

The command prints the loopback URL, the number of indexed projects, the project index path, the process ID, and the shutdown instruction. The default port is selected by the operating system. Press Ctrl-C in the launching terminal to stop the server and invalidate its in-memory session.

## Project index

The machine-wide list of recorded projects comes from two files in the Turnal state directory (`$TURNAL_STATE_DIR`, else `$XDG_STATE_HOME/turnal`, else `~/.local/state/turnal`):

- `registry.json` stays authoritative for which projects exist. `turnal init` writes it.
- `projects.sqlite` is a derived index over that registry plus each store's durable records. It exists so the cross-project view does not have to open every store on every page load.

`projects.sqlite` is disposable. Delete it and the next launch rebuilds it. It is never a source of truth, and nothing is recoverable only from it.

A project whose store directory has disappeared stays listed and is marked absent. Recorded history outlives the working tree, so silently dropping the entry would make lost history look like history that never existed.

## Security boundary

Prism exposes source, prompts, tool results, and deleted file content for **every registered project**, not just the one it was launched from. One loopback origin now reaches all of them, so the launch secret carries more weight than it did when the viewer was scoped to a single workspace.

- The server binds to `127.0.0.1`; there is no external listen option.
- Each launch uses a random path and a 256-bit fragment secret. URL fragments are not sent in HTTP requests.
- The browser exchanges the fragment secret once for a short-lived `HttpOnly`, `SameSite=Strict` cookie scoped to the random launch path.
- API requests require the scoped session and a custom viewer header.
- Every history route is scoped to one project (`/api/v1/projects/<store-id>/...`). The project is resolved before any path is opened, and an unknown project fails as `unknown_project`. A resource key minted for one store cannot resolve inside another.
- The two state-changing routes additionally require the session token echoed in an `X-Turnal-Write` header. A cross-origin page can cause a cookie to be sent but cannot read an `HttpOnly` cookie, so it cannot forge that header.
- The write token is returned once, to same-origin script that proved it holds the launch secret. It is held in memory only and never placed in a URL or a readable cookie. A reloaded tab can still read history but cannot add or remove projects.
- The server rejects unexpected Host and Origin values, sends no CORS allowance, and applies a restrictive Content Security Policy.
- Viewer content and assets make no remote requests. There are no CDN fonts, telemetry calls, update checks, or hosted dependencies.
- Captured content is rendered as text. Repository paths and canonical resource keys are validated by the Go service before Git reads.

These controls defend against drive-by websites, DNS rebinding, accidental local clients, and token leakage between browser origins. They do not claim to defeat a malicious process running as the same operating-system user with permission to inspect Turnal files or browser state.

## Writes

History is read-only. Prism never changes workspace files, event logs, checkpoint refs, retention policy, or rollback state. Blame reads use the existing disposable cache only in read-only mode. Rollback, editing, and retention changes remain CLI-only; Prism can copy an inspect command but does not execute it.

Two operations do write, and both are about which projects Turnal knows:

**Add project** runs the same steps as `turnal init` in the chosen directory: create the `.turnal` store, optionally append `.turnal/` to `.gitignore`, install agent hooks for the selected agent, and register the store. The dialog lists these effects before you confirm and shows the equivalent CLI command. Workspace-Git rollback capture is off by default, because it is the one mode that can later modify your existing `.git/`. Your existing `.git/` is not modified by adding a project.

**Remove project** deregisters the store and nothing more. The `.turnal` directory, its recorded history, and any installed hooks are left on disk, so re-adding the directory or running `turnal init` in it restores the project with its history intact. Use `turnal destroy` to actually delete recorded history.

The viewer retries a transient partial event tail only while an event-writer lock is active. A partial tail without an active writer remains a visible integrity failure.

## Data quality and limits

- Checkpoint diffs are computed from validated private Git commits, not provider payloads.
- Prompts and tool activity come from normalized event records and are identified separately from checkpoint-backed file facts.
- Canonical URLs use versioned opaque keys containing store, worktree, stream, session, and turn identity. Friendly IDs are display labels only.
- One patch response is limited to 512 KB and 6,000 lines.
- One blame response is limited to 1,500 lines.
- One turn response is limited to 500 normalized events.
- The review surface fetches at most 20 file patches per turn.
- The cross-project activity feed is capped per request and defaults to the most recent 40 sessions.
- Truncation is reported in the response and UI.

Prism intentionally omits rollback buttons, editing, cloud accounts, automatic uploads, arbitrary network binding, Electron packaging, and raw adapter-record browsing. Cross-project search is not implemented yet; the index makes it possible but the query surface is still CLI-only.

## Build and release

`npm run build:web` creates deterministic production assets in `internal/viewer/web/dist`. Those generated assets are committed and embedded with `go:embed`, so direct `go install` and unsupported-platform npm fallback builds do not require Node.

`npm run check:web` type-checks, rebuilds, and fails if the committed assets differ; `scripts/ci/quality.sh` runs it in CI. Tests enforce a 1.5 MB compressed asset budget, reject source maps, and reject remote runtime URL dependencies.
