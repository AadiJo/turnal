#!/bin/sh

set -eu

executables="turnal turnal-adapter-opencode turnal-adapter-gemini-cli turnal-adapter-copilot-cli"

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

warn() {
  echo "turnal installer warning: $*" >&2
}

curl_request() {
  if [ "${TURNAL_ALLOW_INSECURE_TRANSPORT:-}" = "1" ]; then
    curl -fsSL "$@"
  else
    curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL "$@"
  fi
}

path_exists() {
  [ -e "$1" ] || [ -L "$1" ]
}

main() {
  repo="${TURNAL_REPOSITORY:-AadiJo/turnal}"
  release_base="${TURNAL_RELEASE_BASE_URL:-https://github.com/$repo/releases/download}"
  install_dir="${TURNAL_INSTALL_DIR:-$HOME/.local/bin}"
  version=""
  tmp_dir=""
  transaction_dir=""
  installed=""
  active=""
  active_had_original=0
  completed=0
  cleaned=0

  rollback_active() {
    [ -n "$active" ] || return 0
    target="$install_dir/$active"
    backup="$transaction_dir/$active.old"
    if [ "$active_had_original" -eq 1 ]; then
      rm -f "$target"
      if path_exists "$backup" && ! mv "$backup" "$target"; then
        warn "could not restore $target from $backup"
      fi
    fi
  }

  rollback_installed() {
    for installed_name in $installed; do
      target="$install_dir/$installed_name"
      backup="$transaction_dir/$installed_name.old"
      rm -f "$target"
      if path_exists "$backup" && ! mv "$backup" "$target"; then
        warn "could not restore $target from $backup"
      fi
    done
  }

  cleanup() {
    [ "$cleaned" -eq 0 ] || return 0
    cleaned=1
    if [ "$completed" -ne 1 ]; then
      rollback_active
      rollback_installed
    fi
    [ -z "$transaction_dir" ] || rm -rf "$transaction_dir"
    [ -z "$tmp_dir" ] || rm -rf "$tmp_dir"
  }

  interrupted() {
    cleanup
    exit 1
  }

  trap cleanup EXIT
  trap interrupted HUP INT TERM

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
        completed=1
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
  command -v grep >/dev/null 2>&1 || fail "grep is required"

  if [ -z "$version" ]; then
    latest_url="${TURNAL_LATEST_RELEASE_URL:-https://github.com/$repo/releases/latest}"
    resolved_url="$(curl_request -o /dev/null -w '%{url_effective}' "$latest_url")" ||
      fail "could not resolve the latest stable release"
    version="${resolved_url##*/}"
    version="${version#v}"
  fi

  printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$' ||
    fail "invalid version: $version"

  archive="turnal_${version}_${platform}_${architecture}.tar.gz"
  asset_root="$release_base/v$version"
  tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/turnal-install.XXXXXX")"
  stage_dir="$tmp_dir/stage"
  mkdir -p "$stage_dir"

  echo "Downloading Turnal $version for ${platform}/${architecture}..."
  curl_request "$asset_root/$archive" -o "$tmp_dir/$archive" ||
    fail "could not download $archive"
  curl_request "$asset_root/checksums.txt" -o "$tmp_dir/checksums.txt" ||
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

  tar -tzf "$tmp_dir/$archive" >"$tmp_dir/members" ||
    fail "could not inspect $archive"
  for member in $executables; do
    member_count="$(awk -v expected="$member" '
      {
        value = $0
        sub(/^\.\//, "", value)
        if (value == expected) count++
      }
      END { print count + 0 }
    ' "$tmp_dir/members")"
    [ "$member_count" -eq 1 ] ||
      fail "archive must contain exactly one regular candidate named $member"
  done
  while IFS= read -r member; do
    normalized="${member#./}"
    case " $executables " in
      *" $normalized "*) ;;
      *) fail "archive contains unexpected entry: $member" ;;
    esac
  done <"$tmp_dir/members"

  # Every listed entry was checked above, so extract the whole archive rather
  # than naming members: tar does not match bare names against "./"-prefixed
  # entries, and the two spellings normalize to the same staged paths.
  tar -xzf "$tmp_dir/$archive" -C "$stage_dir" ||
    fail "could not extract $archive"
  for executable in $executables; do
    [ -f "$stage_dir/$executable" ] && [ ! -L "$stage_dir/$executable" ] ||
      fail "archive entry $executable is not a regular file"
    chmod 755 "$stage_dir/$executable"
  done

  mkdir -p "$install_dir" || fail "could not create $install_dir"
  install_dir="$(CDPATH= cd -- "$install_dir" && pwd -P)" ||
    fail "could not resolve the installation directory"
  [ -w "$install_dir" ] || fail "$install_dir is not writable"
  transaction_dir="$(mktemp -d "$install_dir/.turnal-install.XXXXXX")" ||
    fail "could not create a private transaction in $install_dir"
  chmod 700 "$transaction_dir"

  for executable in $executables; do
    candidate="$transaction_dir/$executable.new"
    cp "$stage_dir/$executable" "$candidate" ||
      fail "could not stage $executable"
    chmod 755 "$candidate"

    active="$executable"
    active_had_original=0
    target="$install_dir/$executable"
    backup="$transaction_dir/$executable.old"
    if path_exists "$target"; then
      mv "$target" "$backup" || fail "could not back up $executable"
      active_had_original=1
    fi

    if [ "${TURNAL_INSTALLER_TESTING:-}" = "1" ] &&
      [ "${TURNAL_TEST_PAUSE_INSTALL:-}" = "$executable" ]; then
      : >"$transaction_dir/paused"
      pause_ticks=0
      while [ "$pause_ticks" -lt 100 ]; do
        sleep 0.1
        pause_ticks=$((pause_ticks + 1))
      done
    fi

    if [ "${TURNAL_INSTALLER_TESTING:-}" = "1" ] &&
      [ "${TURNAL_TEST_FAIL_INSTALL:-}" = "$executable" ]; then
      install_ok=0
    elif mv "$candidate" "$target"; then
      install_ok=1
    else
      install_ok=0
    fi
    [ "$install_ok" -eq 1 ] || fail "could not install $executable"

    installed="$executable $installed" active="" active_had_original=0
  done

  completed=1
  echo "Turnal $version installed in $install_dir"
  resolved_turnal="$(command -v turnal 2>/dev/null || true)"
  if [ -n "$resolved_turnal" ] && [ "$resolved_turnal" != "$install_dir/turnal" ]; then
    warn "$resolved_turnal shadows $install_dir/turnal earlier in PATH"
  else
    case ":$PATH:" in
      *":$install_dir:"*) ;;
      *) echo "Add $install_dir to PATH to run turnal." ;;
    esac
  fi
}

main "$@"
