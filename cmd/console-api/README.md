# cmd/console-api

**Planned, M5.** Nothing is built here yet.

The coordinators' API process: Connect RPC v2 with server streaming for the live board, OIDC middleware and the audited reveal. It serves the contracts in `api/proto/sauti/v1` and runs as the operational role `op_console` (spec §4.2).

- **Milestone:** M5 Console (`docs/superpowers/plans/2026-11-12-phased-roadmap.md`, phase 6; spec §11, M5).
- **Owner:** the repository owner (`*` in `.github/CODEOWNERS`).
- **Constraints:** no coordinator override outside the listed exceptions (hard constraint 7); no access from outside Uganda (hard constraint 13); audit entries carry incident and staff IDs only (hard constraint 8).

Where this sits in the reference architecture: [`docs/reference/repository-layout.md`](../../docs/reference/repository-layout.md).
