#!/bin/sh
# Build release archives for all supported platforms into dist/ with a
# SHA256SUMS file. Usage: scripts/dist.sh [VERSION]
set -eu
version="${1:-dev}"
out="dist"
rm -rf "$out"
mkdir -p "$out"
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os="${target%/*}"
  arch="${target#*/}"
  work="$(mktemp -d)"
  bin=mote
  [ "$os" = windows ] && bin=mote.exe
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X github.com/jgalego/mote/internal/cli.Version=$version" \
    -o "$work/$bin" ./cmd/mote
  cp LICENSE README.md "$work/"
  if [ "$os" = windows ]; then
    if command -v zip >/dev/null 2>&1; then
      (cd "$work" && zip -q -X "mote_${os}_${arch}.zip" "$bin" LICENSE README.md)
    else
      (cd "$work" && 7z a -tzip -bso0 "mote_${os}_${arch}.zip" "$bin" LICENSE README.md)
    fi
    mv "$work/mote_${os}_${arch}.zip" "$out/"
  else
    tar -C "$work" -czf "$out/mote_${os}_${arch}.tar.gz" "$bin" LICENSE README.md
  fi
  rm -rf "$work"
done
cd "$out"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum mote_* > SHA256SUMS
else
  shasum -a 256 mote_* > SHA256SUMS
fi
cat SHA256SUMS
