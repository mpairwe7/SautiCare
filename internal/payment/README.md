# internal/payment

**Planned, M4.** Nothing is built here yet.

The payment bounded context: the payout state machine, the provider response taxonomy (including *unknown*), reconciliation and the cost ledger (spec §4.3). `cmd/payments` runs it.

- **Milestone:** M4 Payments (`docs/superpowers/plans/2026-11-12-phased-roadmap.md`, phase 5; spec §11, M4).
- **Owner:** the repository owner (`*` in `.github/CODEOWNERS`).
- **Constraints:** an unknown provider response is settled by a status query by reference, and retries run at 5, 30 and 60 minutes before the manual queue (roadmap phase 5). The context never imports `internal/platform` (archcheck R1), and its first Go file needs its own entry in `Allowed` in `tools/archcheck/rules.go` (R2).

Where this sits in the reference architecture: [`docs/reference/repository-layout.md`](../../docs/reference/repository-layout.md).
