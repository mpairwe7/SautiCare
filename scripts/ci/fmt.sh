#!/usr/bin/env bash
# Formatting and vet, for both the default and the integration build.
set -euo pipefail
cd "$(dirname "$0")/../.."
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
  echo "gofmt needed:"; echo "$unformatted"; exit 1
fi
go vet ./...
go vet -tags integration ./...
