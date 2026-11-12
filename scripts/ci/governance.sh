#!/usr/bin/env bash
# Docs-as-code and governance gates (global engineering rules; spec §10).
set -euo pipefail
cd "$(dirname "$0")/../.."
PY=${PYTHON:-python3}
"$PY" -m tools.freshness --check
"$PY" governance/compliance_check.py
"$PY" threat-model/validate_threats.py
"$PY" -m unittest discover -s governance/tests -t . -q
go run ./tools/amendgate -base "${AMENDGATE_BASE:-origin/main}"
