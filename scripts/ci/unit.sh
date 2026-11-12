#!/usr/bin/env bash
# Unit tests (no build tags). Race detector needs cgo, so it runs where a C
# toolchain exists (CI) and is skipped locally when CGO_ENABLED=0.
set -euo pipefail
cd "$(dirname "$0")/../.."
args=(-shuffle=on -count=1)
if [ "$(go env CGO_ENABLED)" = "1" ]; then
  args+=(-race)
fi
go test "${args[@]}" ./...
