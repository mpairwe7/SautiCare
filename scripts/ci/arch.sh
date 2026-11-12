#!/usr/bin/env bash
# Module boundary rules R1-R4 (ADR-0002).
set -euo pipefail
cd "$(dirname "$0")/../.."
go run ./tools/archcheck
