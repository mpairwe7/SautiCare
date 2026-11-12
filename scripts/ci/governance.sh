#!/usr/bin/env bash
# Docs-as-code and governance gates (global engineering rules; spec §10).
set -euo pipefail
cd "$(dirname "$0")/../.."
PY=${PYTHON:-python3}
"$PY" -m tools.freshness --check
"$PY" governance/compliance_check.py
"$PY" threat-model/validate_threats.py
"$PY" -m unittest discover -s governance/tests -t . -q
base=${AMENDGATE_BASE:-origin/main}
# A push that creates the branch has no pre-push commit (all zeros), so there
# is no baseline: refuse rather than compare nothing (ADR-0014).
if [[ $base =~ ^0+$ ]]; then
  echo "amendgate: no baseline commit (the push created the branch); refusing to pass unchecked" >&2
  exit 1
fi
go run ./tools/amendgate -base "$base"
