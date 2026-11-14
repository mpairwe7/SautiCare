# internal/dsr

**Planned, M5.** Nothing is built here yet.

The data-subject request context: the clock on each request, and the withdrawal that removes a person's analytic record (spec §4.3, requirement R13).

- **Milestone:** M5 Console (`docs/superpowers/plans/2026-11-12-phased-roadmap.md`, phase 6: "DSR and breach workflows").
- **Owner:** the repository owner (`*` in `.github/CODEOWNERS`).
- **Constraints:** a request is answered within 14 days (requirement R13); no new kind of record without the DPO (hard constraint 12). The context never imports `internal/platform` (archcheck R1), and its first Go file needs its own entry in `Allowed` in `tools/archcheck/rules.go` (R2).

Where this sits in the reference architecture: [`docs/reference/repository-layout.md`](../../docs/reference/repository-layout.md).
