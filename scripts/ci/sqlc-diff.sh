#!/usr/bin/env bash
# Fail if committed sqlc output is stale relative to schema and queries.
set -euo pipefail
cd "$(dirname "$0")/../.."
scripts/tools/install-sqlc.sh >/dev/null
bin/sqlc diff
