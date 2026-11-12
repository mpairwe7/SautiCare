#!/usr/bin/env bash
# Download a pinned release binary into ./bin and verify its sha256 before
# installing it (supply chain: never run an unverified tool).
# Usage: fetch.sh <name> <version-marker> <url> <sha256> [member-in-tarball]
set -euo pipefail
cd "$(dirname "$0")/../.."
name=$1 marker=$2 url=$3 sha=$4 member=${5:-}
if [ -x "bin/$name" ] && [ -f "bin/.$name.version" ] && [ "$(cat "bin/.$name.version")" = "$marker" ]; then
  exit 0
fi
mkdir -p bin
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/download" "$url"
echo "${sha}  $tmp/download" | sha256sum -c - >/dev/null
if [ -n "$member" ]; then
  tar -xzf "$tmp/download" -C "$tmp" "$member"
  install -m 0755 "$tmp/$member" "bin/$name"
else
  install -m 0755 "$tmp/download" "bin/$name"
fi
echo "$marker" > "bin/.$name.version"
echo "installed $name $marker"
