#!/usr/bin/env bash
# CycloneDX SBOM of the shipped module (Build Prompt §17). Spec 1.6 is
# ECMA-424; 1.7 is available and noted in ADR-0015.
set -euo pipefail
cd "$(dirname "$0")/../.."
mkdir -p dist
go tool -modfile=build/tools.mod cyclonedx-gomod mod -licenses -json \
  -output-version 1.6 -output dist/sbom.cdx.json .
echo "SBOM: dist/sbom.cdx.json ($(wc -c < dist/sbom.cdx.json) bytes)"
