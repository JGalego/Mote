#!/bin/sh
# mote installer for Linux and macOS.
#
# Downloads one released mote binary from GitHub over HTTPS, verifies it
# against the release's SHA256SUMS, installs it to ~/.local/bin (no root),
# then runs `mote setup`. Nothing else is downloaded or executed here;
# `mote setup` explains and verifies each later download (llama.cpp, models).
#
# Environment:
#   MOTE_SOURCE    set to 1 to build the current source from GitHub instead
#                  of downloading a release (needs Go; see MOTE_VERSION)
#   MOTE_VERSION   release tag to install, or with MOTE_SOURCE the branch,
#                  tag or commit to build (default: latest, main from source)
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

# install_binary puts one built or downloaded mote in place, replacing any
# copy already there atomically so the script is safe to re-run.
install_binary() {
  mkdir -p "$PREFIX"
  cp "$1" "$PREFIX/.mote.new"
  chmod 755 "$PREFIX/.mote.new"
  mv -f "$PREFIX/.mote.new" "$PREFIX/mote"
  say "installed $PREFIX/mote ($("$PREFIX/mote" version))"
  case ":$PATH:" in
    *":$PREFIX:"*) ;;
    *) say "add $PREFIX to your PATH, e.g.: echo 'export PATH=\"$PREFIX:\$PATH\"' >> ~/.profile" ;;
  esac
}

# finish runs `mote setup` unless the caller asked us not to.
finish() {
  [ "${MOTE_NO_SETUP:-0}" = 1 ] && exit 0
  # When piped into sh, stdin is the script; ask questions on the terminal.
  if [ -t 0 ]; then
    exec "$PREFIX/mote" setup "$@"
  elif (: </dev/tty) 2>/dev/null; then
    exec "$PREFIX/mote" setup "$@" </dev/tty
  else
    exec "$PREFIX/mote" setup --yes "$@"
  fi
}

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

if [ "${MOTE_SOURCE:-0}" = 1 ]; then
  command -v go >/dev/null 2>&1 || die "building from source needs Go: https://go.dev/dl/"
  ref="$VERSION"
  [ "$ref" = latest ] && ref=main
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT INT TERM
  say "building github.com/$REPO/cmd/mote@$ref with $(go version | cut -d" " -f3)"
  # Go's module proxy caches what a branch points at for a few minutes, so
  # fetch straight from the repository unless a proxy is configured. The
  # checksum database has to fetch a commit it has not seen itself, which
  # fails often enough for one pushed a minute ago; mote comes straight
  # from GitHub over TLS anyway, so only its dependencies are checked there.
  nosum="${GONOSUMDB:-}"
  [ -z "${GOPROXY:-}" ] && nosum="${GONOSUMDB:+$GONOSUMDB,}github.com/$REPO"
  build() { GOBIN="$tmp" GOPROXY="${GOPROXY:-direct}" GONOSUMDB="$nosum" go install "github.com/$REPO/cmd/mote@$ref"; }
  # A direct fetch looks the module up under several paths at once, sharing
  # one git clone, and one lookup can unshallow it while another reads it
  # ("shallow file has changed"). A second try finds the clone complete.
  build || { say "retrying the build"; build; } ||
    die "build failed; check that $ref exists in https://github.com/$REPO"
  [ -x "$tmp/mote" ] || die "build produced no mote binary"
  install_binary "$tmp/mote"
  finish "$@"
fi

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
install_binary "$tmp/mote"
finish "$@"
