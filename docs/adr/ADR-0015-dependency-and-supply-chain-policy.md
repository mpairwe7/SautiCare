---
status: accepted
date: 2026-11-12
decision-makers: Sauti engineering (repository owner)
consulted: —
informed: Principal Investigator, Data Protection Officer
---
# ADR-0015: Dependency policy and supply-chain pinning

## Context and Problem Statement

Build Prompt §17: about twenty well-governed, permissively licensed packages; pin everything; scan every build; sign and record releases. The March 2026 trivy-action tag hijack (CVE-2026-33634) showed tag pinning is not enough.

## Decision Drivers

* Small, auditable dependency graph
* Reproducible builds while international links are down (mirrors at P0)

## Considered Options

* Standard library first; dependency notes; SHA-pinned actions; checksum-verified tool binaries; separate tool module
* Free choice of libraries with Dependabot

## Decision Outcome

Chosen: runtime dependencies `pgx/v5` (MIT) and `go.yaml.in/yaml/v4` (Apache-2.0/MIT); test-only `embedded-postgres/v2` (MIT). Each has a note in `docs/reference/dependencies.md`. GitHub Actions are pinned by commit SHA and the database image by digest. Scanners (gitleaks, osv-scanner, golangci-lint, sqlc) are release binaries verified by sha256 (`scripts/tools/fetch.sh`). govulncheck and cyclonedx-gomod live in `build/tools.mod` so their dependencies never enter the shipped graph. Licence allowlist MIT, BSD, Apache-2.0, MPL-2.0, ISC. SBOM: CycloneDX 1.6 (ECMA-424); 1.7 is available and will be adopted with the release workflow.

## Consequences

* Good: every executable in CI is identified by hash.
* Bad: version bumps are manual until self-hosted Renovate (P0).

## Confirmation

CI jobs `vuln` (govulncheck, OSV-Scanner with licence allowlist), `secrets`, `sbom`.
