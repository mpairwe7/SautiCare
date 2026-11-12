# Sauti Dispatch documentation

Documentation follows [Diátaxis](https://diataxis.fr/) and is checked as code in CI by `python3 -m tools.freshness --check`. Confidential governing documents are cited by title and section only and are never committed ([ADR-0008](adr/ADR-0008-interim-hosting-and-confidentiality.md)).

| Kind | Start here |
|---|---|
| **Tutorials** (learning) | [Run Sauti Dispatch locally](tutorials/local-development.md) |
| **How-to guides** (tasks) | [Run the M0 demo](how-to/run-the-m0-demo.md) · [Add a migration](how-to/add-a-migration.md) |
| **Runbooks** (operations) | [Audit ledger verification failure](runbooks/audit-ledger-verification-failure.md) · [Outbox relay lag](runbooks/outbox-relay-lag.md) · [Outbox poison row](runbooks/outbox-poison-row.md) · [Database migrations](runbooks/database-migrations.md) · [Event partitions](runbooks/event-partition-maintenance.md) · [Projection drift](runbooks/projection-drift.md) · [Intake gateway errors](runbooks/intake-gateway-errors.md) · [Template](runbooks/_template.md) |
| **Reference** (facts) | [Configuration](reference/configuration.md) · [Environment](reference/environment.md) · [Events](reference/events.md) · [Stores and privileges](reference/stores.md) · [Audit encoding](reference/audit-canonical-encoding.md) · [USSD adapter](reference/ussd-at-adapter.md) · [sautictl](reference/cli-sautictl.md) · [Dependencies](reference/dependencies.md) |
| **Explanation** (understanding) | [Architecture](explanation/architecture.md) · [Security and privacy model](explanation/security-model.md) |
| **Decisions** | [Architecture Decision Records](adr/README.md) |
| **Governance** | [Traceability matrix](traceability.md) · [Model Card](MODEL_CARD.md) · `governance/ai_risk_manifest.yaml` · `threat-model/threats.yaml` |
| **Design history** | `docs/superpowers/specs/2026-11-12-sauti-dispatch-design.md` · `docs/superpowers/plans/2026-11-12-phased-roadmap.md` |
