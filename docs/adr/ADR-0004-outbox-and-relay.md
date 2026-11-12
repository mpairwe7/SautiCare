---
status: accepted
date: 2026-11-12
decision-makers: Sauti engineering (repository owner)
consulted: —
informed: Principal Investigator, Data Protection Officer
---
# ADR-0004: Transactional outbox and a dedicated outbox-relay process

## Context and Problem Statement

The audit ledger is a separate store with its own credentials (rule 3.9). A dual write from every service would be non-atomic, and dispatch must never wait on audit (rule 3.14).

## Decision Drivers

* Atomic "event happened ⇔ audit will record it"
* Audit outage must not stop dispatch
* Fewest processes holding audit credentials

## Considered Options

* Transactional outbox in the operational store + one relay process
* Each service appends to the audit store directly
* Logical replication / CDC from the operational store

## Decision Outcome

Chosen option: **transactional outbox + `cmd/outbox-relay`**. The write functions insert the audit outbox row in the event's transaction; the relay claims rows `FOR UPDATE SKIP LOCKED`, appends them (idempotent on `entry_id`), and marks them delivered. Exactly one relay is active (advisory-lock leadership); standbys take over automatically. A row violating the payload contract halts the relay (poison) — it is never skipped.

## Consequences

* Good: dispatch is unaffected by audit outages; the relay is the only holder of audit credentials.
* Bad: audit is eventually consistent (seconds); lag is monitored (`sauti_outbox_lag_seconds`).

## Confirmation

`internal/audit/relay_test.go::TestRelayCatchesUpAfterLedgerOutage`, `::TestRelayAbortsOnPoisonRow`; `internal/platform/pg/relay_integration_test.go::TestSingleActiveRelayLeader`, `::TestRelayEndToEndAgainstPostgres`.
