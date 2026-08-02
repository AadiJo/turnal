#!/usr/bin/env bash

set -euo pipefail

repo_root="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"
build_dir="$(mktemp -d)"
trap 'rm -rf "$build_dir"' EXIT

unformatted="$(gofmt -l .)"
if [[ -n "$unformatted" ]]; then
  echo "Go files need formatting:" >&2
  echo "$unformatted" >&2
  exit 1
fi

go mod tidy
git diff --exit-code -- go.mod go.sum
go vet ./...
go test -race ./...
npm run test:postinstall
npm run test:release-sbom
go build -o "$build_dir/turnal" ./cmd/turnal

go install golang.org/x/vuln/cmd/govulncheck@v1.6.0
"$(go env GOPATH)/bin/govulncheck" ./...
