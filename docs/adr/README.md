# Architecture Decision Records

**Type:** Reference (Diátaxis) · Format: [MADR 4.0](https://adr.github.io/madr/) · Owners: platform engineering

Each record has the mandatory sections *Context and Problem Statement*, *Decision Outcome*, *Consequences* and *Confirmation*. Records are never edited after acceptance except to mark them superseded; a new record supersedes an old one. `python3 -m tools.freshness --check` fails CI if a record is missing here or lacks a mandatory section.

| ADR | Title | Status | Date |
|---|---|---|---|
| [ADR-0001](ADR-0001-record-architecture-decisions.md) | Record architecture decisions with MADR 4.0 | Accepted | 2026-11-12 |
| [ADR-0002](ADR-0002-hexagonal-modular-monolith.md) | Hexagonal modular monolith with enforced boundaries | Accepted | 2026-11-12 |
| [ADR-0003](ADR-0003-single-write-path.md) | Single write path enforced by the database | Accepted | 2026-11-12 |
| [ADR-0004](ADR-0004-outbox-and-relay.md) | Transactional outbox and a dedicated outbox-relay process | Accepted | 2026-11-12 |
| [ADR-0005](ADR-0005-audit-ledger-checkpoints.md) | Hash-chained audit ledger with ML-DSA-65 checkpoints and a witness | Accepted | 2026-11-12 |
| [ADR-0006](ADR-0006-five-stores-and-roles.md) | Five stores with per-store roles | Accepted | 2026-11-12 |
| [ADR-0007](ADR-0007-incident-reference.md) | Incident reference: nine random digits plus a Damm check digit, read 3-3-4 | Accepted | 2026-11-12 |
| [ADR-0008](ADR-0008-interim-hosting-and-confidentiality.md) | Interim GitHub hosting, confidential documents excluded, Forgejo-ready CI | Accepted | 2026-11-12 |
| [ADR-0009](ADR-0009-governing-document-discrepancies.md) | Resolution of discrepancies between governing documents | Accepted | 2026-11-12 |
| [ADR-0010](ADR-0010-forward-only-migrator.md) | In-house forward-only, checksummed migrator | Accepted | 2026-11-12 |
| [ADR-0011](ADR-0011-sqlc-and-postgresql-18.md) | sqlc v1.31.1 with PostgreSQL 18-only DDL | Accepted | 2026-11-12 |
| [ADR-0012](ADR-0012-test-database-environments.md) | Test database environments | Accepted | 2026-11-12 |
| [ADR-0013](ADR-0013-envelope-encryption-and-dev-keys.md) | Envelope encryption, row-bound AAD, keyed lookup hash and development keys | Accepted | 2026-11-12 |
| [ADR-0014](ADR-0014-configuration-and-amendment-gate.md) | Versioned configuration, fingerprints and the amendment gate | Accepted | 2026-11-12 |
| [ADR-0015](ADR-0015-dependency-and-supply-chain-policy.md) | Dependency policy and supply-chain pinning | Accepted | 2026-11-12 |
| [ADR-0016](ADR-0016-living-documentation-mapping.md) | Living-documentation mapping of the global engineering rules | Accepted | 2026-11-12 |
| [ADR-0017](ADR-0017-metrics-exposition.md) | In-house Prometheus metrics until the OpenTelemetry SDK | Accepted | 2026-11-12 |
| [ADR-0018](ADR-0018-session-state-in-postgresql.md) | Tool 1 session state in PostgreSQL; Valkey deferred | Accepted | 2026-11-12 |
| [ADR-0019](ADR-0019-signed-triage-decision-table.md) | Triage as a signed CEL decision table | Accepted | 2026-11-12 |
| [ADR-0020](ADR-0020-location-grid-and-gazetteer.md) | UTM 36N grid in Go and a frozen gazetteer with pg_trgm matching | Accepted | 2026-11-12 |
| [ADR-0021](ADR-0021-sealed-answers-and-keyed-step-keys.md) | Screen-answer identifiers sealed; USSD step keys keyed hashes | Accepted | 2026-11-12 |
