#!/usr/bin/env bash
# Install the pinned sqlc release binary into ./bin, verifying its sha256.
# sqlc v1.31.1 cannot be built here without cgo, and the release publishes no
# checksum file, so the hash below was recorded on first download (2026-11-12)
# and is the trust anchor (ADR-0011).
set -euo pipefail
cd "$(dirname "$0")/../.."
VERSION=1.31.1
SHA256=497ae4fcdfa64c5b0c311ffe4c2bd991e43991e82e5367792ed78bc2dca27354
URL="https://github.com/sqlc-dev/sqlc/releases/download/v${VERSION}/sqlc_${VERSION}_linux_amd64.tar.gz"
if [ -x bin/sqlc ] && [ "$(bin/sqlc version)" = "v${VERSION}" ]; then
  exit 0
fi
mkdir -p bin
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/sqlc.tgz" "$URL"
echo "${SHA256}  $tmp/sqlc.tgz" | sha256sum -c - >/dev/null
tar -xzf "$tmp/sqlc.tgz" -C "$tmp" sqlc
install -m 0755 "$tmp/sqlc" bin/sqlc
echo "installed sqlc $(bin/sqlc version)"
