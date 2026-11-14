# internal/consent

**Planned, M5.** Nothing is built here yet.

The consent bounded context: the consent states that order the Tool 4 follow-up and gate the analytic export (spec §4.3).

- **Milestone:** M5 Console (`docs/superpowers/plans/2026-11-12-phased-roadmap.md`, phase 6: "follow-up (Tool 4) with consent-first ordering"); M6 uses it for the consent-gated export (phase 7).
- **Owner:** the repository owner (`*` in `.github/CODEOWNERS`).
- **Constraints:** consent flags gate the analytic export (hard constraint 10); application roles cannot write consent rows directly, only through the database's `SECURITY DEFINER` functions (spec §6.2). The context never imports `internal/platform` (archcheck R1), and its first Go file needs its own entry in `Allowed` in `tools/archcheck/rules.go` (R2).

Where this sits in the reference architecture: [`docs/reference/repository-layout.md`](../../docs/reference/repository-layout.md).
