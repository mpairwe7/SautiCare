# tests/drills

**Planned, M7.** Nothing is built here yet.

Failover, switchover and restore drill scripts. Each drill proves a requirement mapped in `docs/traceability.md`, and its runbook records the last drill date.

- **Milestone:** M7 Resilience writes the scripts (`docs/superpowers/plans/2026-11-12-phased-roadmap.md`, phase 8: "failover and switchover drill scripts"); the real-hardware runs wait for P0 (phase 9).
- **Owner:** the repository owner (`*` in `.github/CODEOWNERS`).
- **Constraints:** every restore drill runs `verify-ledger` and `verify-projections` (spec §7.10).

Where this sits in the reference architecture: [`docs/reference/repository-layout.md`](../../docs/reference/repository-layout.md).
