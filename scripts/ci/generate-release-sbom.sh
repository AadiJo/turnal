#!/usr/bin/env bash

set -euo pipefail

repo_root="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

output_path="${1:-turnal.spdx.json}"
release_version="${2:-$(node -p "require('./package.json').version")}"
work_root="$(mktemp -d)"
scan_root="$work_root/release"
cleanup() {
  rm -rf "$work_root"
}
trap cleanup EXIT

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

npm_destination="$work_root/npm-pack"
mkdir -p "$npm_destination"
npm pack --pack-destination "$npm_destination" >/dev/null
npm_archives=("$npm_destination"/*.tgz)
if [[ "${#npm_archives[@]}" -ne 1 || ! -f "${npm_archives[0]}" ]]; then
  echo "SBOM invariant failed: npm pack must produce exactly one archive" >&2
  exit 1
fi
npm_archive="${npm_archives[0]}"
mkdir -p "$scan_root/npm"
tar -xzf "$npm_archive" -C "$scan_root/npm"

packed_manifest="$scan_root/npm/package/package.json"
packed_version="$(node -p "require(process.argv[1]).version" "$packed_manifest")"
if [[ "$packed_version" != "$release_version" ]]; then
  echo "SBOM invariant failed: packed npm version '$packed_version' does not match '$release_version'" >&2
  exit 1
fi

standalone_targets=(
  darwin_amd64
  darwin_arm64
  linux_amd64
  linux_arm64
  windows_amd64
  windows_arm64
)
for target in "${standalone_targets[@]}"; do
  archive="dist/releases/turnal_${release_version}_${target}.tar.gz"
  if [[ ! -f "$archive" ]]; then
    echo "SBOM invariant failed: missing standalone archive '$archive'" >&2
    exit 1
  fi
  mkdir -p "$scan_root/standalone/$target"
  tar -xzf "$archive" -C "$scan_root/standalone/$target"
done

"$syft_bin" "dir:$scan_root" \
  --source-name "Turnal" \
  --source-version "$release_version" \
  --output "spdx-json=$output_path"
node scripts/validate-release-sbom.js --sbom "$output_path" --version "$release_version"
