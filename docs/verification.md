# Verification commands

Run commands from the repository root. Install dependencies with `npm ci`,
`npm --prefix apps/vscode ci`, and `npm --prefix apps/marketing ci`.
Use the Go version in `go.mod` and Node.js 24, matching CI.

| Command | Coverage |
| --- | --- |
| `npm run check:web` | Viewer TypeScript checks without rebuilding assets. |
| `npm run check:web:assets` | Viewer TypeScript checks, asset rebuild, and tracked asset differences against the Git index. |
| `npm run check:vscode` | Extension compilation and existing Node.js tests. |
| `npm run check:marketing` | Marketing site's production build. |
| `npm run check:go` | Go formatting, module tidiness, vet, race tests, and CLI compilation. Requires Bash. |
| `npm run check:all` | Viewer types and build, extension checks, marketing build, postinstall tests, release SBOM tests, and Go checks, in that order. |
| `scripts/ci/quality.sh` | All checks above, committed viewer asset freshness, and `govulncheck`. Installs the pinned vulnerability scanner. |
| `npm test` | Go tests without the race detector. |

`check:all` rebuilds the embedded viewer assets. Review and commit those changes
with viewer source changes. `check:web:assets` fails for unstaged asset changes,
so use `check:web` during editing and the asset check after staging the rebuild.

`check:go` runs `go mod tidy` and fails if `go.mod` or `go.sum` differs from the
Git index. It builds the CLI in a temporary directory and removes that directory
on exit. Neither Go command enables opt-in tests that require provider tools.

The CI quality job runs `scripts/ci/quality.sh`. Separate CI jobs test Go and
installers on Linux, macOS, and Windows, plus the Windows npm package lifecycle.
`check:all` does not run those installer checks or the release workflow.
