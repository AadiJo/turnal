#!/bin/sh

set -eu

repo="${TURNAL_REPOSITORY:-AadiJo/turnal}"
release_base="${TURNAL_RELEASE_BASE_URL:-https://github.com/$repo/releases/download}"
install_dir="${TURNAL_INSTALL_DIR:-$HOME/.local/bin}"
version=""

usage() {
  cat <<'EOF'
Install Turnal on macOS or Linux.

Usage: install.sh [--version VERSION] [--install-dir DIRECTORY]

Options:
  --version VERSION       Install a specific version instead of the latest stable release.
  --install-dir DIRECTORY Install executables into DIRECTORY (default: ~/.local/bin).
  -h, --help              Show this help.
EOF
}

fail() {
  echo "turnal installer: $*" >&2
  exit 1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      [ "$#" -ge 2 ] || fail "--version requires a value"
      version="${2#v}"
      shift 2
      ;;
    --install-dir)
      [ "$#" -ge 2 ] || fail "--install-dir requires a value"
      install_dir="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

case "$(uname -s)" in
  Darwin) platform="darwin" ;;
  Linux) platform="linux" ;;
  *) fail "unsupported operating system: $(uname -s)" ;;
esac

case "$(uname -m)" in
  x86_64|amd64) architecture="amd64" ;;
  arm64|aarch64) architecture="arm64" ;;
  *) fail "unsupported architecture: $(uname -m)" ;;
esac

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v tar >/dev/null 2>&1 || fail "tar is required"

if [ -z "$version" ]; then
  latest_url="${TURNAL_LATEST_RELEASE_URL:-https://github.com/$repo/releases/latest}"
  resolved_url="$(curl -fsSL -o /dev/null -w '%{url_effective}' "$latest_url")" ||
    fail "could not resolve the latest stable release"
  version="${resolved_url##*/}"
  version="${version#v}"
  [ -n "$version" ] || fail "latest stable release did not resolve to a version"
fi

case "$version" in
  *[!0-9A-Za-z.-]*|'') fail "invalid version: $version" ;;
esac

archive="turnal_${version}_${platform}_${architecture}.tar.gz"
asset_root="$release_base/v$version"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/turnal-install.XXXXXX")"
backup_dir="$tmp_dir/backups"
stage_dir="$tmp_dir/stage"
mkdir -p "$backup_dir" "$stage_dir"

cleanup() {
  rm -rf "$tmp_dir"
}
trap cleanup EXIT HUP INT TERM

echo "Downloading Turnal $version for ${platform}/${architecture}..."
curl -fsSL "$asset_root/$archive" -o "$tmp_dir/$archive" ||
  fail "could not download $archive"
curl -fsSL "$asset_root/checksums.txt" -o "$tmp_dir/checksums.txt" ||
  fail "could not download checksums.txt"

checksum_line="$(grep "  $archive\$" "$tmp_dir/checksums.txt" || true)"
[ -n "$checksum_line" ] || fail "checksums.txt does not contain $archive"
printf '%s\n' "$checksum_line" > "$tmp_dir/archive.checksum"

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$tmp_dir" && sha256sum -c archive.checksum >/dev/null) ||
    fail "checksum verification failed for $archive"
elif command -v shasum >/dev/null 2>&1; then
  (cd "$tmp_dir" && shasum -a 256 -c archive.checksum >/dev/null) ||
    fail "checksum verification failed for $archive"
else
  fail "sha256sum or shasum is required"
fi

tar -xzf "$tmp_dir/$archive" -C "$stage_dir" ||
  fail "could not extract $archive"

executables="turnal turnal-adapter-opencode turnal-adapter-gemini-cli turnal-adapter-copilot-cli"
for executable in $executables; do
  [ -f "$stage_dir/$executable" ] || fail "archive is missing $executable"
  chmod 755 "$stage_dir/$executable"
done

mkdir -p "$install_dir" || fail "could not create $install_dir"
[ -w "$install_dir" ] || fail "$install_dir is not writable"

installed=""
rollback() {
  for executable in $installed; do
    rm -f "$install_dir/$executable"
    if [ -f "$backup_dir/$executable" ]; then
      mv "$backup_dir/$executable" "$install_dir/$executable"
    fi
  done
}

for executable in $executables; do
  candidate="$install_dir/.$executable.new.$$"
  cp "$stage_dir/$executable" "$candidate" || {
    rollback
    fail "could not stage $executable"
  }
  chmod 755 "$candidate"
  if [ -e "$install_dir/$executable" ]; then
    mv "$install_dir/$executable" "$backup_dir/$executable" || {
      rm -f "$candidate"
      rollback
      fail "could not back up $executable"
    }
  fi
  mv "$candidate" "$install_dir/$executable" || {
    rollback
    fail "could not install $executable"
  }
  installed="$executable $installed"
done

echo "Turnal $version installed in $install_dir"
case ":$PATH:" in
  *":$install_dir:"*) ;;
  *) echo "Add $install_dir to PATH to run turnal." ;;
esac
