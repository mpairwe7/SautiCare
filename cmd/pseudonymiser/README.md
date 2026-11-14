# cmd/pseudonymiser

**Planned, M6.** Nothing is built here yet.

The one-way export of closed incidents: it reads the operational store, writes pseudonymised records to the analytic store, and writes the study-ID and incident-ID pairs to the linkage vault only (spec §4.2).

- **Milestone:** M6 Research data (`docs/superpowers/plans/2026-11-12-phased-roadmap.md`, phase 7: "Pseudonymiser, consent-gated export, linkage vault"; spec §11, M6).
- **Owner:** the repository owner (`*` in `.github/CODEOWNERS`). The stores it writes, `db/analytic/` and `db/linkage/`, are meant for the Principal Investigator.
- **Constraints:** it is the only process that may link both the operational and the analytic store adapters (archcheck rule R4, `tools/archcheck/rules.go`); consent flags gate the export (hard constraint 10).

Where this sits in the reference architecture: [`docs/reference/repository-layout.md`](../../docs/reference/repository-layout.md).
