#!/usr/bin/env bash
# Pinned promtool from the Prometheus 3.13 LTS release (supported to
# 2027-07-31). The sha256 is the release's own sha256sums.txt entry for the
# linux-amd64 tarball, which matches GitHub's digest of the asset (verified
# 2026-11-14).
set -euo pipefail
cd "$(dirname "$0")"
v=3.13.5
./fetch.sh promtool "v$v" \
  "https://github.com/prometheus/prometheus/releases/download/v$v/prometheus-$v.linux-amd64.tar.gz" \
  0388822ace519c5a3a8ef556a541ce1b536e21d872f82deded465bd27a5a0699 "prometheus-$v.linux-amd64/promtool"
