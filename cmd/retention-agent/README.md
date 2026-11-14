# cmd/retention-agent

**Planned, M6.** Nothing is built here yet.

Applies the retention schedule: key periods, holds, crypto-shredding and the destruction log (spec §4.2, requirement R11). It holds operational credentials, and media credentials for key operations only.

- **Milestone:** M6 Research data (`docs/superpowers/plans/2026-11-12-phased-roadmap.md`, phase 7: "retention agent and crypto-shredding"; spec §6.4).
- **Owner:** the repository owner (`*` in `.github/CODEOWNERS`).
- **Constraints:** destroying a key needs an OpenBao control-group approval from the technical operator and the DPO, and the PI signs the destruction of the study stores (spec §6.4).

Where this sits in the reference architecture: [`docs/reference/repository-layout.md`](../../docs/reference/repository-layout.md).
