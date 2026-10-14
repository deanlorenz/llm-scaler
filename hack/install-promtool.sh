#!/usr/bin/env bash
# Install a pinned promtool into $1 from the Prometheus release tarball.
# `go install .../cmd/promtool` is refused: prometheus's go.mod carries replace
# directives. The tarball is checked against the release's sha256sums.txt.
#
#   hack/install-promtool.sh <bin-dir> <version, e.g. 2.54.1>
set -eu

BIN=$1
VERSION=$2
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) echo "FAIL: no promtool release for $(uname -m)" >&2; exit 1 ;;
esac
NAME="prometheus-${VERSION}.${OS}-${ARCH}"
URL="https://github.com/prometheus/prometheus/releases/download/v${VERSION}"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
# --retry-all-errors covers the release CDN's occasional HTTP 5xx, which plain
# --retry does not.
for f in "$NAME.tar.gz" sha256sums.txt; do
  curl -fsSL --retry 6 --retry-delay 10 --retry-all-errors -o "$WORK/$f" "$URL/$f"
done
WANT=$(awk -v f="$NAME.tar.gz" '$2 == f {print $1}' "$WORK/sha256sums.txt")
if [ -z "$WANT" ]; then
  echo "FAIL: $NAME.tar.gz is not listed in the release checksums" >&2
  exit 1
fi
GOT=$( (sha256sum "$WORK/$NAME.tar.gz" 2>/dev/null || shasum -a 256 "$WORK/$NAME.tar.gz") | awk '{print $1}')
if [ "$GOT" != "$WANT" ]; then
  echo "FAIL: $NAME.tar.gz checksum $GOT, release lists $WANT" >&2
  exit 1
fi
tar -xzf "$WORK/$NAME.tar.gz" -C "$WORK" "$NAME/promtool"
mkdir -p "$BIN"
mv "$WORK/$NAME/promtool" "$BIN/promtool"
"$BIN/promtool" --version | head -1
