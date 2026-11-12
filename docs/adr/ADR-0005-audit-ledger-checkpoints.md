---
status: accepted
date: 2026-11-12
decision-makers: Sauti engineering (repository owner)
consulted: Engineering Guide V2 §15 (external daily witness)
informed: Principal Investigator, Data Protection Officer
---
# ADR-0005: Hash-chained audit ledger with ML-DSA-65 checkpoints and a witness

## Context and Problem Statement

Rule 3.8 requires a hash-chained audit ledger with signed checkpoints recording every classification, dispatch decision, alert, acceptance, payment event and override, carrying only incident and staff IDs. The Guide adds an external daily witness. Research data must stay confidential and verifiable past 2030 (NIST IR 8547).

## Decision Drivers

* Tamper evidence even against a database administrator
* Post-quantum signatures for a five-year retention
* No personal data in a five-year store

## Considered Options

* SHA-256 chain in PostgreSQL + ML-DSA-65 signed checkpoints + write-once witness
* Trillian Tessera transparency log now (no PostgreSQL driver; Phase 2 in the stack doc)
* Ed25519 checkpoints

## Decision Outcome

Chosen option: **SHA-256 chain in PostgreSQL with ML-DSA-65 checkpoints**. `audit.append` serialises appends with an advisory lock, assigns a dense `bigint` sequence and computes `hash = sha256(prev_hash ‖ canonical(entry))` in SQL; Go re-computes it independently (`docs/reference/audit-canonical-encoding.md`). Entries have typed columns only. Every 5 minutes the head is signed with ML-DSA-65 (Go 1.27 `crypto/mldsa`, context `sauti-audit-checkpoint/v1`), stored, and written through the `Witness` port. Append-only triggers block UPDATE/DELETE/TRUNCATE even for the owner.

## Consequences

* Good: rewriting, deleting or truncating history is detected; signatures are quantum-resistant.
* Bad: a superuser can still disable triggers — detection, not prevention, is the guarantee; witnesses must live outside the database host.

## Confirmation

`internal/platform/pg/auditstore/auditstore_integration_test.go::TestSQLHashMatchesGoCanonical`, `::TestConcurrentAppendsKeepValidChain`, `::TestOwnerUpdateBlockedByTrigger`; `internal/audit/verify_test.go::TestVerifyDetectsTruncationBeyondCheckpoint`; `tests/e2e/m0_fixture_test.go::TestM0FixtureIncident` (one-byte tamper detected at its sequence).
