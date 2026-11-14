# cmd/payments

**Planned, M4.** Nothing is built here yet.

Pays responders by mobile money (MTN MoMo and Airtel Money): payouts, retries and nightly reconciliation, as the operational role `op_payments` (spec §4.2). The domain logic lives in `internal/payment`.

- **Milestone:** M4 Payments (`docs/superpowers/plans/2026-11-12-phased-roadmap.md`, phase 5; spec §11, M4).
- **Owner:** the repository owner (`*` in `.github/CODEOWNERS`). The payment amounts come from the executive team.
- **Constraints:** each payment reference is stored before the first provider call (roadmap phase 5); provider hosts are reached only through the egress allowlist (spec §7.8); payments being down never fails readiness (spec §7.6); payment payloads stay minimal (requirement R15).

Where this sits in the reference architecture: [`docs/reference/repository-layout.md`](../../docs/reference/repository-layout.md).
