---
status: accepted
date: 2026-11-12
decision-makers: Sauti engineering (repository owner)
consulted: —
informed: Principal Investigator, Data Protection Officer
---
# ADR-0011: sqlc v1.31.1 with PostgreSQL 18-only DDL

## Context and Problem Statement

The stack requires pgx v5 with sqlc. On 2026-11-12 sqlc v1.31.1 (latest release) parses PostgreSQL 17 grammar; PG18 syntax (`WITHOUT OVERLAPS`, `RETURNING OLD/NEW`) exists only on unreleased sqlc main. sqlc needs cgo to build, which the development machine lacks.

## Decision Drivers

* Pinned, reproducible code generation
* Use PG18 features (temporal keys for availability in M2)

## Considered Options

* Pinned release binary; PG18-only constraints added inside `DO $$ … EXECUTE … $$` blocks sqlc does not parse
* Pin sqlc to an unreleased main commit
* Hand-written pgx for the affected tables

## Decision Outcome

Chosen option: **pinned release binary** fetched and checksum-verified by `scripts/tools/install-sqlc.sh`; generated code is committed and `scripts/ci/sqlc-diff.sh` fails CI when stale. The spike showed v1.31.1 handles `uuidv7()` defaults, partitioned tables and plpgsql functions; functions returning `TABLE` are queried with column aliases.

## Consequences

* Good: released tool only; PG18 features still usable.
* Bad: dynamic-SQL DDL is less readable; revisit when sqlc releases PG18 support.

## Confirmation

CI job `fmt-arch-sqlc` runs `scripts/ci/sqlc-diff.sh`.
