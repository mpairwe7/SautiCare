#!/usr/bin/env bash
# Known vulnerabilities (govulncheck: reachable code; OSV-Scanner: every
# dependency) and the licence allowlist of Build Prompt §17.
set -euo pipefail
cd "$(dirname "$0")/../.."
go tool -modfile=build/tools.mod govulncheck ./...
scripts/tools/install-scanners.sh >/dev/null
bin/osv-scanner scan source --lockfile=go.mod \
  --licenses="MIT,BSD-2-Clause,BSD-3-Clause,Apache-2.0,MPL-2.0,ISC"
