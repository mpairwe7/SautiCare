# Repository layout

**Type:** Reference (Diátaxis) · **Decision:** [ADR-0040](../adr/ADR-0040-repository-layout-follows-the-reference-architecture.md) · **Checked by:** `governance/tests/test_repository_layout.py`

The reference architecture is the Scalable MVP Stack, §Repository structure. That document is confidential, so it is cited by title and section only ([ADR-0008](../adr/ADR-0008-interim-hosting-and-confidentiality.md)). Spec §5 records the same layout.

In short, the reference keeps everything in one repository and one Go module (`sautihealth.com/sauti`), split into separately deployed programs, each with its own directory under `cmd/`. Private domain modules sit under `internal/`, with their adapters in `internal/platform/`. Beside the code sit the console contracts and app, the triage rules, the per-store database files, telephony, analytics, deployment, tests and docs.

**Status values:**
- **present:** the directory exists and holds what the reference puts there.
- **planned (milestone):** the directory holds only a README, and the named milestone builds it. The milestone comes from the phased roadmap (`docs/superpowers/plans/2026-11-12-phased-roadmap.md`, phase *n*) and spec §11.
- **deviation:** the repository differs from the reference. The row gives the reason.

The test fails when a present or planned path is not a directory, or when a top-level directory in Git is not in this table.

## Layout

| Reference location | Repository path | Status | Notes |
|---|---|---|---|
| Console API contracts | `api/proto/sauti/v1/` | planned (M5) | Roadmap phase 6 |
| `cmd/` intake gateway | `cmd/intake-gw/` | present | Receives the USSD and voice gateways' requests, opens sessions and assigns incident references |
| `cmd/` dispatch worker | `cmd/dispatch-worker/` | present | Alert, re-dispatch and escalation workflows; follow-up timers arrive with M5 |
| `cmd/` console API | `cmd/console-api/` | planned (M5) | Roadmap phase 6 |
| `cmd/` IVR controller | `cmd/ivr-controller/` | present | Asterisk ARI: prompts and bridging. The reference also gives it consent recordings; here it cannot record by design ([ADR-0023](../adr/ADR-0023-ivr-through-in-house-ari-client.md)), and where consented interview audio is captured is not yet scheduled |
| `cmd/` payments | `cmd/payments/` | planned (M4) | Roadmap phase 5 |
| `cmd/` pseudonymiser | `cmd/pseudonymiser/` | planned (M6) | Roadmap phase 7. The only process allowed to link the operational and analytic stores (archcheck R4) |
| `cmd/` retention agent | `cmd/retention-agent/` | planned (M6) | Roadmap phase 7 |
| `internal/` incident | `internal/incident/` | present | State machine and Damm-checked reference ([ADR-0007](../adr/ADR-0007-incident-reference.md)) |
| `internal/` triage | `internal/triage/` | present | Rule engine and logic versions ([ADR-0019](../adr/ADR-0019-signed-triage-decision-table.md)) |
| `internal/` responder | `internal/responder/` | present | |
| `internal/` dispatch | `internal/dispatch/` | present | |
| `internal/` payment | `internal/payment/` | planned (M4) | Roadmap phase 5 |
| `internal/` consent | `internal/consent/` | planned (M5) | Roadmap phase 6 (consent-first follow-up); M6 gates the export on it |
| `internal/` data-subject requests | `internal/dsr/` | planned (M5) | Roadmap phase 6 |
| `internal/` audit | `internal/audit/` | present | Hash-chained ledger and signed checkpoints ([ADR-0005](../adr/ADR-0005-audit-ledger-checkpoints.md)) |
| `internal/` crypto | `internal/crypto/` | present | Envelope encryption ([ADR-0013](../adr/ADR-0013-envelope-encryption-and-dev-keys.md)) |
| `internal/` platform adapters | `internal/platform/` | present | Database (`pg`), telemetry (`metrics`, `logging`), environment, telco and voice adapters; the authentication adapter (OIDC) arrives with M5 |
| Triage rules | `triage-rules/` | present | The triage rule tables (signed) with the scenarios that test them. Gated by the amendment gate |
| `db/` operational | `db/operational/` | present | Migrations and sqlc queries |
| `db/` analytic | `db/analytic/` | present | Meant for the Principal Investigator in CODEOWNERS |
| `db/` linkage | `db/linkage/` | present | Meant for the Principal Investigator in CODEOWNERS |
| `db/` audit | `db/audit/` | present | |
| Console app | `web/console/` | planned (M5) | Roadmap phase 6 |
| Telephony | `telephony/asterisk/`, `telephony/prompts/` | present | Asterisk dialplan; English and Luganda prompt catalogue |
| Analytics | `analytics/` | planned (M6) | Roadmap phase 7 |
| Deployment | `deploy/` | planned (P0) | Roadmap phase 9. Its README lists the planned `ansible/`, `quadlet/`, `haproxy/` and `patroni/` homes, which do not exist yet |
| `tests/` end-to-end | `tests/e2e/` | present | |
| `tests/` USSD simulator | `internal/platform/sim/` | deviation | Moves to `tests/` in Phase B. Open pull requests import it, so it stays until they merge |
| `tests/` load | `tests/load/` | planned (M7) | Roadmap phase 8 (k6) |
| `tests/` drills | `tests/drills/` | planned (M7) | Roadmap phase 8 writes the scripts; real-hardware runs wait for P0 (phase 9) |
| Docs | `docs/adr/`, `docs/runbooks/` | present | Decision records and runbooks; the requirement-to-test map is [`docs/traceability.md`](../traceability.md) |
| Not in the reference | `governance/`, `threat-model/` | deviation | Required paths under the owner's engineering rules: the AI risk register and AIBOM, amendment records, the threat model and their gates ([ADR-0016](../adr/ADR-0016-living-documentation-mapping.md)) |
| Not in the reference | `config/` | deviation | Runtime and gated configuration that the services read and the amendment gate watches ([ADR-0014](../adr/ADR-0014-configuration-and-amendment-gate.md)) |
| Not in the reference | `tools/`, `scripts/`, `build/` | deviation | The CI pipeline's tools: archcheck, amendgate and freshness; the CI steps; the pinned tool module |
| Not in the reference | `.github/` | deviation | GitHub hosts the repository and CI instead of Forgejo, by the owner's decision ([ADR-0008](../adr/ADR-0008-interim-hosting-and-confidentiality.md)) |
| Not in the reference | `db/media/` | deviation | The fifth store: consented interview audio ([ADR-0006](../adr/ADR-0006-five-stores-and-roles.md)) |
| Not in the reference | `internal/config/`, `internal/intake/`, `internal/location/`, `internal/pii/`, `internal/respond/` | deviation | Additional domain modules (spec §4.3) |
| Not in the reference | `cmd/outbox-relay/`, `cmd/sautictl/` | deviation | Additional processes (spec §4.2, [ADR-0004](../adr/ADR-0004-outbox-and-relay.md)). The `demo` process arrives with the unmerged demo branch |

## Rules

| Rule | Where |
|---|---|
| Module boundaries are enforced by a CI import check | `tools/archcheck/`, run by `scripts/ci/arch.sh`. The allowlist of context-to-context imports is [`tools/archcheck/rules.go`](../../tools/archcheck/rules.go); a new context needs an entry there before its first Go file ([ADR-0002](../adr/ADR-0002-hexagonal-modular-monolith.md)) |
| Ports and adapters | Contexts declare ports; `internal/platform/` implements them; `cmd/` wires them (spec §4.1) |
| Code owners | [`.github/CODEOWNERS`](../../.github/CODEOWNERS). Interim: the repository owner holds every path; the comments name the intended owners, among them the emergency-medicine co-investigator for `triage-rules/` and the Principal Investigator for `db/analytic/` |
| Every requirement maps to a test or drill | [`docs/traceability.md`](../traceability.md), checked by `python3 -m tools.freshness --check` |
