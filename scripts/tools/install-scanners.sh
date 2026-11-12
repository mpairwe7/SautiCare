#!/usr/bin/env bash
# Pinned scanner binaries (versions and sha256 verified 2026-11-12).
set -euo pipefail
cd "$(dirname "$0")"
./fetch.sh gitleaks v8.30.1 \
  https://github.com/gitleaks/gitleaks/releases/download/v8.30.1/gitleaks_8.30.1_linux_x64.tar.gz \
  551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb gitleaks
./fetch.sh osv-scanner v2.7.1 \
  https://github.com/google/osv-scanner/releases/download/v2.7.1/osv-scanner_linux_amd64 \
  fb7dab19a8bd9bbe5c3189f09d4b7ee1820c9553a23bac202648a56efd2f6ee8
