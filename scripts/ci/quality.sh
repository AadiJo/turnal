#!/usr/bin/env bash

set -euo pipefail

repo_root="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"
# Local and CI verification share the same commands. Run the inexpensive
# frontend checks before the Go race suite.
npm run check:all
# Embedded assets must match their sources before a release can ship.
npm run check:web:assets

go install golang.org/x/vuln/cmd/govulncheck@v1.6.0
"$(go env GOPATH)/bin/govulncheck" ./...
