#!/bin/sh
# Builds Potluck for every supported kind of computer into dist/.
# The programs are self-contained: no installer, nothing else to download to start them.
set -e
cd "$(dirname "$0")"
VERSION="${1:-0.1.0}"
mkdir -p dist
for t in windows/amd64 windows/arm64 darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
  os=${t%/*}; arch=${t#*/}; out="dist/potluck-$os-$arch"; [ "$os" = windows ] && out="$out.exe"
  GOOS=$os GOARCH=$arch CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$out" .
  echo "built $out"
done
