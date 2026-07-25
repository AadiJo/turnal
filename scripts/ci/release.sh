#!/usr/bin/env bash

set -euo pipefail

usage() {
  echo "usage: release.sh <stable|nightly> <ref> <run-number> <sha>" >&2
}

if [[ $# -ne 4 ]]; then
  usage
  exit 2
fi

channel="$1"
ref="$2"
run_number="$3"
sha="$4"
repo_root="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

if [[ ! "$sha" =~ ^[0-9a-fA-F]{7,40}$ ]]; then
  echo "release invariant failed: invalid commit SHA '$sha'" >&2
  exit 1
fi

case "$channel" in
  stable)
    if [[ "$ref" == *-nightly.* ]]; then
      echo "release invariant failed: nightly tag '$ref' cannot publish on the stable channel" >&2
      exit 1
    fi
    metadata="$(node scripts/resolve-release.js stable "$ref")"
    ;;
  nightly)
    nightly_date="${TURNAL_NIGHTLY_DATE:-$(date -u +%Y%m%d)}"
    metadata="$(node scripts/resolve-release.js nightly "$nightly_date" "$run_number" "$sha")"
    ;;
  *)
    echo "release invariant failed: unsupported channel '$channel'" >&2
    exit 1
    ;;
esac

while IFS='=' read -r key value; do
  case "$key" in
    channel|base_version|version|tag|name|npm_tag|prerelease|make_latest) ;;
    *)
      echo "release invariant failed: unexpected metadata key '$key'" >&2
      exit 1
      ;;
  esac
  variable="RELEASE_${key^^}"
  printf -v "$variable" '%s' "$value"
  export "$variable"
done <<< "$metadata"

if [[ "${RELEASE_CHANNEL:-}" != "$channel" ]]; then
  echo "release invariant failed: resolved channel '${RELEASE_CHANNEL:-}' does not match '$channel'" >&2
  exit 1
fi
if [[ "$channel" == stable && "${RELEASE_TAG:-}" != "$ref" ]]; then
  echo "release invariant failed: resolved tag '${RELEASE_TAG:-}' does not match '$ref'" >&2
  exit 1
fi

node scripts/set-release-version.js "$RELEASE_VERSION"
npm test
npm run build

export TURNAL_RELEASE_CHANNEL="$RELEASE_CHANNEL"
export TURNAL_COMMIT="$sha"
npm run pack:dry-run
if [[ -n "${TURNAL_RELEASE_TARGETS:-}" ]]; then
  echo "release invariant failed: TURNAL_RELEASE_TARGETS must be unset for publication" >&2
  exit 1
fi
npm run build:release-archives

expected_archives=(
  "turnal_${RELEASE_VERSION}_darwin_amd64.tar.gz"
  "turnal_${RELEASE_VERSION}_darwin_arm64.tar.gz"
  "turnal_${RELEASE_VERSION}_linux_amd64.tar.gz"
  "turnal_${RELEASE_VERSION}_linux_arm64.tar.gz"
)
release_assets=(turnal.spdx.json dist/releases/checksums.txt)
for archive in "${expected_archives[@]}"; do
  archive_path="dist/releases/$archive"
  if [[ ! -f "$archive_path" ]]; then
    echo "release invariant failed: missing standalone archive '$archive_path'" >&2
    exit 1
  fi
  checksum_matches="$(awk -v name="$archive" '$2 == name { count++ } END { print count + 0 }' dist/releases/checksums.txt)"
  if [[ "$checksum_matches" -ne 1 ]]; then
    echo "release invariant failed: checksums.txt must contain exactly one entry for '$archive'" >&2
    exit 1
  fi
  release_assets+=("$archive_path")
done
checksum_lines="$(awk 'NF { count++ } END { print count + 0 }' dist/releases/checksums.txt)"
if [[ "$checksum_lines" -ne 4 ]]; then
  echo "release invariant failed: checksums.txt contains $checksum_lines entries; expected 4" >&2
  exit 1
fi

syft_bin="${SYFT_BIN:-}"
if [[ -z "$syft_bin" ]]; then
  if command -v syft >/dev/null 2>&1; then
    syft_bin="$(command -v syft)"
  else
    tool_dir="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/turnal-ci-bin"
    mkdir -p "$tool_dir"
    GOBIN="$tool_dir" go install github.com/anchore/syft/cmd/syft@v1.33.0
    syft_bin="$tool_dir/syft"
  fi
fi
"$syft_bin" dir:. --output spdx-json=turnal.spdx.json

case "${TURNAL_PUBLISH:-true}" in
  false)
    echo "release rehearsal complete; publication disabled"
    exit 0
    ;;
  true) ;;
  *)
    echo "release invariant failed: TURNAL_PUBLISH must be true or false" >&2
    exit 1
    ;;
esac

if [[ -z "${GH_TOKEN:-}" ]]; then
  echo "release invariant failed: GH_TOKEN is required to create the GitHub release" >&2
  exit 1
fi
if ! command -v gh >/dev/null 2>&1; then
  echo "release invariant failed: gh is required to create the GitHub release" >&2
  exit 1
fi

existing_release="$(mktemp)"
trap 'rm -f "$existing_release"' EXIT
if gh release view "$RELEASE_TAG" --json assets,isDraft,isPrerelease >"$existing_release" 2>/dev/null; then
  node - "$existing_release" "$RELEASE_PRERELEASE" "${release_assets[@]}" <<'NODE'
const fs = require('node:fs');
const [, , manifestPath, expectedPrerelease, ...assetPaths] = process.argv;
const release = JSON.parse(fs.readFileSync(manifestPath, 'utf8'));
const expected = assetPaths.map((value) => value.split('/').pop()).sort();
const actual = release.assets.map((asset) => asset.name).sort();
if (release.isDraft || String(release.isPrerelease) !== expectedPrerelease) {
  throw new Error('existing GitHub release metadata does not match the resolved release');
}
if (JSON.stringify(actual) !== JSON.stringify(expected)) {
  throw new Error(`existing GitHub release assets do not match: expected ${expected.join(', ')}, got ${actual.join(', ')}`);
}
NODE
  echo "verified existing GitHub release $RELEASE_TAG"
else
  gh_args=(
    "$RELEASE_TAG"
    "${release_assets[@]}"
    --target "$sha"
    --title "$RELEASE_NAME"
    --generate-notes
  )
  if [[ "$RELEASE_PRERELEASE" == true ]]; then
    gh_args+=(--prerelease)
  fi
  if [[ "$channel" == stable ]]; then
    gh_args+=(--verify-tag)
  fi
  if [[ "$RELEASE_MAKE_LATEST" == false ]]; then
    gh_args+=(--latest=false)
  fi
  gh release create "${gh_args[@]}"
fi

npm_args=(publish --access public --tag "$RELEASE_NPM_TAG")
if [[ "${NPM_PUBLISH_PROVENANCE:-false}" == true ]]; then
  npm_args+=(--provenance)
fi
npm "${npm_args[@]}"
