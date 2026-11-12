---
status: accepted
date: 2026-11-12
decision-makers: Sauti engineering (repository owner)
consulted: —
informed: Principal Investigator, Data Protection Officer
---
# ADR-0003: Single write path enforced by the database

## Context and Problem Statement

Build Prompt §7 requires one write path: each state change adds an `incident_events` row in the same transaction, and `incident.state` is derived from those rows. Application roles must not UPDATE or DELETE event, audit or consent rows. Timestamps T1–T7 must come from the database clock.

## Decision Drivers

* Correctness that does not depend on every service being bug-free
* Idempotent retries (USSD and voice callbacks are retried)
* Database-set protocol timestamps

## Considered Options

* SECURITY DEFINER functions as the only writers, application roles without table write privileges
* Repository code in Go writing tables directly, with conventions
* Database triggers deriving the projection

## Decision Outcome

Chosen option: **SECURITY DEFINER functions** `op.open_incident`, `op.append_incident_event` and `op.mark_outbox_delivered`, each pinning `search_path`. They check idempotency (`op.event_keys`), optimistic versions, the lifecycle table (identical to the Go reducer's), the severity ratchet; set T-times with `clock_timestamp()`; update the projection; and write the outbox row — in one transaction. The pure Go reducer `incident.Fold` is the logic of record; `sautictl verify-projections` proves the projection equals the fold of its events.

Only `incident_events` is partitioned by month; uniqueness that must span partitions lives in the unpartitioned `op.event_keys`. A DEFAULT partition guarantees a missing monthly partition never fails a dispatch write; it is monitored.

## Consequences

* Good: a bug in a service cannot bypass the lifecycle or forge a timestamp.
* Bad: projection rules exist twice (SQL and Go); the drift check is mandatory.

## Confirmation

`internal/platform/pg/opstore/opstore_integration_test.go::TestRuntimeRolesCannotWriteDirectly`, `::TestProjectionMatchesFold`, `::TestOpenIncidentSetsT1FromDatabase`, `::TestTransitionsTableMatchesGo`, and `internal/platform/pg/opstore/verify_integration_test.go::TestVerifyProjectionsDetectsDrift`.
