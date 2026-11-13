#!/usr/bin/env bash
# Integration tests against PostgreSQL 18. CI sets SAUTI_TEST_PG_DSN to the
# pinned postgres:18.6 service; locally an embedded PostgreSQL 18.6 starts.
set -euo pipefail
cd "$(dirname "$0")/../.."
args=(-tags integration -count=1 -timeout 20m)
if [ "$(go env CGO_ENABLED)" = "1" ]; then
  args+=(-race)
fi
go test "${args[@]}" ./...
