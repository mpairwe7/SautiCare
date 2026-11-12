#!/usr/bin/env bash
# Secret scanning of the full git history (no credential ever enters Git).
set -euo pipefail
cd "$(dirname "$0")/../.."
scripts/tools/install-scanners.sh >/dev/null
bin/gitleaks git --no-banner --redact --exit-code 1 .
