#!/bin/sh
# Install limit-ledger from GitHub Releases on Linux or macOS.
#
#   curl -fsSL https://raw.githubusercontent.com/mehdashti/limit-ledger/main/install.sh | sh
#
# Environment:
#   LIMIT_LEDGER_VERSION   a tag such as v0.1.0 (default: the latest release)
#   LIMIT_LEDGER_BIN_DIR   where to put the binary (default: ~/.local/bin)
#   LIMIT_LEDGER_FROM      a local folder holding the release files, instead of downloading
#
# The archive is checked against the release's checksums.txt before anything is installed.
set -eu

repo="mehdashti/limit-ledger"
version="${LIMIT_LEDGER_VERSION:-latest}"
bin_dir="${LIMIT_LEDGER_BIN_DIR:-$HOME/.local/bin}"
from="${LIMIT_LEDGER_FROM:-}"

fail() { echo "limit-ledger install: $*" >&2; exit 1; }

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "unsupported system $(uname -s); download a release by hand from https://github.com/$repo/releases" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "unsupported machine $(uname -m)" ;;
esac

if [ -z "$from" ]; then
  command -v curl >/dev/null 2>&1 || fail "curl is required"
  if [ "$version" = latest ]; then
    version=$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" |
      sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
    [ -n "$version" ] || fail "could not find the latest release"
  fi
fi
case "$version" in
  v*) ;;
  latest) fail "set LIMIT_LEDGER_VERSION when installing from LIMIT_LEDGER_FROM" ;;
  *) version="v$version" ;;
esac
archive="limit-ledger_${version#v}_${os}_${arch}.tar.gz"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM
if [ -n "$from" ]; then
  cp "$from/$archive" "$from/checksums.txt" "$tmp/" || fail "$archive or checksums.txt not found in $from"
else
  base="https://github.com/$repo/releases/download/$version"
  echo "downloading $archive ($version)"
  curl -fsSL -o "$tmp/$archive" "$base/$archive" || fail "download failed: $base/$archive"
  curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail "download failed: $base/checksums.txt"
fi

expected=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$expected" ] || fail "$archive is not listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
else
  actual=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
fi
[ "$expected" = "$actual" ] || fail "checksum mismatch for $archive"

tar -xzf "$tmp/$archive" -C "$tmp" limit-ledger
mkdir -p "$bin_dir"
install -m 0755 "$tmp/limit-ledger" "$bin_dir/limit-ledger" 2>/dev/null ||
  { cp "$tmp/limit-ledger" "$bin_dir/limit-ledger" && chmod 0755 "$bin_dir/limit-ledger"; }

echo "installed $("$bin_dir/limit-ledger" version) to $bin_dir/limit-ledger"
case ":$PATH:" in
  *":$bin_dir:"*) ;;
  *) echo "note: $bin_dir is not on your PATH; add it, or run $bin_dir/limit-ledger" ;;
esac
echo "next: limit-ledger ingest && limit-ledger report weekly"
