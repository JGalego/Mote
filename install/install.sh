#!/bin/sh
# mote installer for Linux and macOS.
#
# Downloads one released mote binary from GitHub over HTTPS, verifies it
# against the release's SHA256SUMS, installs it to ~/.local/bin (no root),
# then runs `mote setup`. Nothing else is downloaded or executed here;
# `mote setup` explains and verifies each later download (llama.cpp, models).
#
# Environment:
#   MOTE_VERSION   release tag to install (default: latest)
#   MOTE_PREFIX    install directory (default: ~/.local/bin)
#   MOTE_NO_SETUP  set to 1 to skip `mote setup`
#   MOTE_BASE_URL  alternative release directory (https:// or file://), for mirrors and tests
# Arguments are passed to `mote setup`, e.g. `sh install.sh --yes`.
set -eu

REPO="jgalego/mote"
VERSION="${MOTE_VERSION:-latest}"
PREFIX="${MOTE_PREFIX:-$HOME/.local/bin}"

say() { printf 'mote-install: %s\n' "$*"; }
die() { printf 'mote-install: error: %s\n' "$*" >&2; exit 1; }

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported OS $(uname -s); on Windows use install.ps1" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) die "unsupported CPU architecture $(uname -m) (need x86_64 or arm64)" ;;
esac

if [ -n "${MOTE_BASE_URL:-}" ]; then
  base="$MOTE_BASE_URL"
elif [ "$VERSION" = latest ]; then
  base="https://github.com/$REPO/releases/latest/download"
else
  base="https://github.com/$REPO/releases/download/$VERSION"
fi
case "$base" in
  https://* | file://*) ;;
  *) die "refusing non-HTTPS download location: $base" ;;
esac

if command -v curl >/dev/null 2>&1; then
  fetch() { curl --proto '=https,file' --tlsv1.2 -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget --https-only -q "$1" -O "$2"; }
else
  die "curl or wget is required"
fi
if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  die "sha256sum or shasum is required to verify the download"
fi
command -v tar >/dev/null 2>&1 || die "tar is required"

asset="mote_${os}_${arch}.tar.gz"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "downloading $asset ($VERSION) from $base"
fetch "$base/$asset" "$tmp/$asset" || die "download failed: $base/$asset"
fetch "$base/SHA256SUMS" "$tmp/SHA256SUMS" || die "download failed: $base/SHA256SUMS"

want="$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")"
[ -n "$want" ] || die "$asset is not listed in SHA256SUMS"
got="$(sha256 "$tmp/$asset")"
[ "$want" = "$got" ] || die "checksum mismatch for $asset (want $want, got $got)"
say "sha256 verified"

tar -xzf "$tmp/$asset" -C "$tmp" mote || die "archive does not contain mote"
mkdir -p "$PREFIX"
# Replace atomically so re-running over an installed copy is safe.
cp "$tmp/mote" "$PREFIX/.mote.new"
chmod 755 "$PREFIX/.mote.new"
mv -f "$PREFIX/.mote.new" "$PREFIX/mote"
say "installed $PREFIX/mote ($("$PREFIX/mote" version))"

case ":$PATH:" in
  *":$PREFIX:"*) ;;
  *) say "add $PREFIX to your PATH, e.g.: echo 'export PATH=\"$PREFIX:\$PATH\"' >> ~/.profile" ;;
esac

[ "${MOTE_NO_SETUP:-0}" = 1 ] && exit 0
# When piped into sh, stdin is the script; ask questions on the terminal.
if [ -t 0 ]; then
  exec "$PREFIX/mote" setup "$@"
elif (: </dev/tty) 2>/dev/null; then
  exec "$PREFIX/mote" setup "$@" </dev/tty
else
  exec "$PREFIX/mote" setup --yes "$@"
fi
