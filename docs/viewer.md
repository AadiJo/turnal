# Turnal Prism local viewer

Turnal Prism is the read-only browser interface launched by `turnal ui`. It explores the same durable history used by the CLI: the append-only event log explains activity, private checkpoint Git commits provide file state, and SQLite remains an optional disposable index.

## Launch

```sh
turnal ui
turnal ui --no-open
turnal ui --port 0
turnal ui --session <session-id>
```

The command prints the loopback URL, workspace, process ID, and shutdown instruction. The default port is selected by the operating system. Press Ctrl-C in the launching terminal to stop the server and invalidate its in-memory session.

## Security boundary

Prism exposes source, prompts, tool results, and deleted file content, so loopback binding is only the first control.

- The server binds to `127.0.0.1`; there is no external listen option.
- Each launch uses a random path and a 256-bit fragment secret. URL fragments are not sent in HTTP requests.
- The browser exchanges the fragment secret once for a short-lived `HttpOnly`, `SameSite=Strict` cookie scoped to the random launch path.
- API requests require the scoped session and a custom viewer header.
- The server rejects unexpected Host and Origin values, sends no CORS allowance, and applies a restrictive Content Security Policy.
- Viewer content and assets make no remote requests. There are no CDN fonts, telemetry calls, update checks, or hosted dependencies.
- Captured content is rendered as text. Repository paths and canonical resource keys are validated by the Go service before Git reads.

These controls defend against drive-by websites, DNS rebinding, accidental local clients, and token leakage between browser origins. They do not claim to defeat a malicious process running as the same operating-system user with permission to inspect Turnal files or browser state.

## Read-only behavior

Prism never changes workspace files, event logs, checkpoint refs, retention policy, or rollback state. Blame reads use the existing disposable cache only in read-only mode. Potentially mutating operations remain CLI-only; Prism can copy an inspect command but does not execute it.

The viewer retries a transient partial event tail only while an event-writer lock is active. A partial tail without an active writer remains a visible integrity failure.

## Data quality and limits

- Checkpoint diffs are computed from validated private Git commits, not provider payloads.
- Prompts and tool activity come from normalized event records and are identified separately from checkpoint-backed file facts.
- Canonical URLs use versioned opaque keys containing store, worktree, stream, session, and turn identity. Friendly IDs are display labels only.
- One patch response is limited to 512 KB and 6,000 lines.
- One blame response is limited to 1,500 lines.
- One turn response is limited to 500 normalized events.
- Truncation is reported in the response and UI.

The first release intentionally omits rollback buttons, editing, cloud accounts, automatic uploads, arbitrary network binding, Electron packaging, global cross-workspace search, side-by-side diffing, and raw adapter-record browsing.

## Build and release

`npm run build:web` creates deterministic production assets in `internal/viewer/web/dist`. Those generated assets are committed and embedded with `go:embed`, so direct `go install` and unsupported-platform npm fallback builds do not require Node.

CI type-checks and rebuilds the frontend, then fails if the committed assets differ. Tests enforce a 1.5 MB compressed asset budget, reject source maps, and reject remote runtime URL dependencies.
