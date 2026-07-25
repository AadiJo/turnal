#!/bin/sh

set -eu

repo_root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/turnal-install-test.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM

case "$(uname -s)" in
  Darwin) platform="darwin" ;;
  Linux) platform="linux" ;;
  *) echo "installer test skipped on unsupported operating system"; exit 0 ;;
esac

case "$(uname -m)" in
  x86_64|amd64) architecture="amd64" ;;
  arm64|aarch64) architecture="arm64" ;;
  *) echo "installer test skipped on unsupported architecture"; exit 0 ;;
esac

version="9.8.7"
archive="turnal_${version}_${platform}_${architecture}.tar.gz"
release_dir="$tmp_dir/releases/v$version"
payload_dir="$tmp_dir/payload"
install_dir="$tmp_dir/bin"
mkdir -p "$release_dir" "$payload_dir"

for executable in turnal turnal-adapter-opencode turnal-adapter-gemini-cli turnal-adapter-copilot-cli; do
  cp "$repo_root/install.sh" "$payload_dir/$executable"
  chmod 755 "$payload_dir/$executable"
done

tar -czf "$release_dir/$archive" -C "$payload_dir" \
  turnal turnal-adapter-opencode turnal-adapter-gemini-cli turnal-adapter-copilot-cli

if command -v sha256sum >/dev/null 2>&1; then
  checksum="$(sha256sum "$release_dir/$archive" | awk '{print $1}')"
else
  checksum="$(shasum -a 256 "$release_dir/$archive" | awk '{print $1}')"
fi
printf '%s  %s\n' "$checksum" "$archive" > "$release_dir/checksums.txt"

TURNAL_RELEASE_BASE_URL="file://$tmp_dir/releases" \
  sh "$repo_root/install.sh" --version "$version" --install-dir "$install_dir"

for executable in turnal turnal-adapter-opencode turnal-adapter-gemini-cli turnal-adapter-copilot-cli; do
  test -x "$install_dir/$executable"
done

printf '0%s  %s\n' "${checksum#?}" "$archive" > "$release_dir/checksums.txt"
if TURNAL_RELEASE_BASE_URL="file://$tmp_dir/releases" \
  sh "$repo_root/install.sh" --version "$version" --install-dir "$install_dir" \
  >"$tmp_dir/tampered.stdout" 2>"$tmp_dir/tampered.stderr"; then
  echo "installer accepted a bad checksum" >&2
  exit 1
fi
grep -q "checksum verification failed" "$tmp_dir/tampered.stderr"

echo "installer tests passed"
