---
status: accepted
date: 2026-11-12
decision-makers: Sauti engineering (repository owner)
consulted: —
informed: Principal Investigator, Data Protection Officer
---
# ADR-0010: In-house forward-only, checksummed migrator

## Context and Problem Statement

Five stores need versioned migrations that an auditor can trust: what ran in production must equal what is in Git.

## Decision Drivers

* Standard library first, about twenty dependencies (Build Prompt §17)
* Detect edits to applied migrations
* Safe under concurrent deploys

## Considered Options

* About 150 lines of in-house code using `embed.FS`
* goose
* golang-migrate / Atlas

## Decision Outcome

Chosen option: **in-house migrator** (`internal/platform/pg/migrate`): files `NNNN_name.sql`, one transaction each, recorded with their sha256; a changed applied file stops the migrator; a session advisory lock serialises runners. No down migrations: problems are fixed forward.

## Consequences

* Good: no dependency; checksum enforcement is explicit.
* Bad: no rollback tooling — recovery is a new migration or a restore.

## Confirmation

`internal/platform/pg/migrate/migrate_integration_test.go::TestMigrateRefusesChangedChecksum`, `::TestMigrateSerializesConcurrentRunners`, `::TestMigrateRollsBackFailedFile`.
