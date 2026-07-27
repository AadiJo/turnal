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
executables="turnal turnal-adapter-opencode turnal-adapter-gemini-cli turnal-adapter-copilot-cli"
mkdir -p "$release_dir" "$payload_dir"

archive_checksum() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

write_checksum() {
  checksum="$(archive_checksum "$release_dir/$archive")"
  printf '%s  %s\n' "$checksum" "$archive" > "$release_dir/checksums.txt"
}

write_valid_payload() {
  rm -rf "$payload_dir"
  mkdir -p "$payload_dir"
  for executable in $executables; do
    printf '#!/bin/sh\necho new-%s\n' "$executable" >"$payload_dir/$executable"
    chmod 755 "$payload_dir/$executable"
  done
  tar -czf "$release_dir/$archive" -C "$payload_dir" $executables
  write_checksum
}

run_installer() {
  TURNAL_ALLOW_INSECURE_TRANSPORT=1 \
    TURNAL_RELEASE_BASE_URL="file://$tmp_dir/releases" \
    sh "$repo_root/install.sh" "$@"
}

write_valid_payload
cp "$release_dir/$archive" "$tmp_dir/valid.tar.gz"
cp "$release_dir/checksums.txt" "$tmp_dir/valid-checksums.txt"

run_installer --version "$version" --install-dir "$install_dir"
for executable in $executables; do
  test -x "$install_dir/$executable"
done

printf '0%s  %s\n' "${checksum#?}" "$archive" > "$release_dir/checksums.txt"
if run_installer --version "$version" --install-dir "$install_dir" \
  >"$tmp_dir/tampered.stdout" 2>"$tmp_dir/tampered.stderr"; then
  echo "installer accepted a bad checksum" >&2
  exit 1
fi
grep -q "checksum verification failed" "$tmp_dir/tampered.stderr"

for invalid_version in ".." "latest" "1.2"; do
  if run_installer --version "$invalid_version" --install-dir "$install_dir" \
    >"$tmp_dir/invalid.stdout" 2>"$tmp_dir/invalid.stderr"; then
    echo "installer accepted invalid version $invalid_version" >&2
    exit 1
  fi
  grep -q "invalid version" "$tmp_dir/invalid.stderr"
done

cp "$tmp_dir/valid.tar.gz" "$release_dir/$archive"
cp "$tmp_dir/valid-checksums.txt" "$release_dir/checksums.txt"
mkdir -p "$tmp_dir/latest"
printf '' >"$tmp_dir/latest/v$version"
TURNAL_ALLOW_INSECURE_TRANSPORT=1 \
  TURNAL_LATEST_RELEASE_URL="file://$tmp_dir/latest/v$version" \
  TURNAL_RELEASE_BASE_URL="file://$tmp_dir/releases" \
  sh "$repo_root/install.sh" --install-dir "$tmp_dir/latest-bin"
test -x "$tmp_dir/latest-bin/turnal"

rm -rf "$payload_dir"
mkdir -p "$payload_dir"
for executable in $executables; do
  printf '#!/bin/sh\necho safe\n' >"$payload_dir/$executable"
  chmod 755 "$payload_dir/$executable"
done
printf 'sensitive\n' >"$tmp_dir/victim"
chmod 600 "$tmp_dir/victim"
rm -f "$payload_dir/turnal"
ln -s "$tmp_dir/victim" "$payload_dir/turnal"
tar -czf "$release_dir/$archive" -C "$payload_dir" $executables
write_checksum
if run_installer --version "$version" --install-dir "$tmp_dir/malicious-bin" \
  >"$tmp_dir/malicious.stdout" 2>"$tmp_dir/malicious.stderr"; then
  echo "installer accepted a symlink archive member" >&2
  exit 1
fi
grep -q "is not a regular file" "$tmp_dir/malicious.stderr"
test ! -x "$tmp_dir/victim"

write_valid_payload
printf 'unexpected\n' >"$payload_dir/unexpected"
tar -czf "$release_dir/$archive" -C "$payload_dir" $executables unexpected
write_checksum
if run_installer --version "$version" --install-dir "$tmp_dir/extra-bin" \
  >"$tmp_dir/extra.stdout" 2>"$tmp_dir/extra.stderr"; then
  echo "installer accepted an unexpected archive member" >&2
  exit 1
fi
grep -q "archive contains unexpected entry" "$tmp_dir/extra.stderr"

cp "$tmp_dir/valid.tar.gz" "$release_dir/$archive"
cp "$tmp_dir/valid-checksums.txt" "$release_dir/checksums.txt"
rollback_dir="$tmp_dir/rollback-bin"
old_dir="$tmp_dir/old"
mkdir -p "$rollback_dir" "$old_dir"
for executable in $executables; do
  printf 'old-%s\n' "$executable" >"$rollback_dir/$executable"
  chmod 755 "$rollback_dir/$executable"
done
mv "$rollback_dir/turnal-adapter-opencode" "$old_dir/opencode"
ln -s "../old/opencode" "$rollback_dir/turnal-adapter-opencode"

if TURNAL_INSTALLER_TESTING=1 \
  TURNAL_TEST_FAIL_INSTALL=turnal-adapter-gemini-cli \
  run_installer --version "$version" --install-dir "$rollback_dir" \
  >"$tmp_dir/rollback.stdout" 2>"$tmp_dir/rollback.stderr"; then
  echo "installer test failure injection unexpectedly succeeded" >&2
  exit 1
fi
unset TURNAL_INSTALLER_TESTING TURNAL_TEST_FAIL_INSTALL
grep -q "could not install turnal-adapter-gemini-cli" "$tmp_dir/rollback.stderr"
for executable in turnal turnal-adapter-gemini-cli turnal-adapter-copilot-cli; do
  grep -q "^old-$executable\$" "$rollback_dir/$executable"
done
test -L "$rollback_dir/turnal-adapter-opencode"
grep -q '^old-turnal-adapter-opencode$' "$rollback_dir/turnal-adapter-opencode"

signal_dir="$tmp_dir/signal-bin"
mkdir -p "$signal_dir"
for executable in $executables; do
  printf 'old-%s\n' "$executable" >"$signal_dir/$executable"
  chmod 755 "$signal_dir/$executable"
done

TURNAL_INSTALLER_TESTING=1 \
  TURNAL_TEST_PAUSE_INSTALL=turnal-adapter-gemini-cli \
  TURNAL_ALLOW_INSECURE_TRANSPORT=1 \
  TURNAL_RELEASE_BASE_URL="file://$tmp_dir/releases" \
  sh "$repo_root/install.sh" --version "$version" --install-dir "$signal_dir" \
  >"$tmp_dir/signal.stdout" 2>"$tmp_dir/signal.stderr" &
signal_pid=$!

signal_waits=0
while [ "$signal_waits" -lt 300 ]; do
  if ls "$signal_dir"/.turnal-install.*/paused >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
  signal_waits=$((signal_waits + 1))
done
if [ "$signal_waits" -ge 300 ]; then
  kill -TERM "$signal_pid" 2>/dev/null || true
  wait "$signal_pid" 2>/dev/null || true
  echo "installer never reached the interrupt window" >&2
  exit 1
fi

# A background job in a non-interactive shell ignores SIGINT, so signal the
# installer with SIGTERM; both share the interrupt handler.
kill -TERM "$signal_pid" 2>/dev/null || true
wait "$signal_pid" 2>/dev/null && {
  echo "interrupted installer reported success" >&2
  exit 1
}

for executable in $executables; do
  if [ ! -f "$signal_dir/$executable" ]; then
    echo "interrupted installer deleted $executable" >&2
    exit 1
  fi
  grep -q "^old-$executable\$" "$signal_dir/$executable" ||
    {
      echo "interrupted installer did not restore $executable" >&2
      exit 1
    }
done
if ls -d "$signal_dir"/.turnal-install.* >/dev/null 2>&1; then
  echo "interrupted installer left a transaction directory behind" >&2
  exit 1
fi

relative_root="$tmp_dir/relative"
mkdir -p "$relative_root"
(
  cd "$relative_root"
  run_installer --version "$version" --install-dir relative-bin
)
test -x "$relative_root/relative-bin/turnal"

echo "installer tests passed"
