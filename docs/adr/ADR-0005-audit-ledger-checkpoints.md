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

Chosen option: **SHA-256 chain in PostgreSQL with ML-DSA-65 checkpoints**. `audit.append` serialises appends with an advisory lock, assigns a dense `bigint` sequence and computes `hash = sha256(prev_hash ‖ canonical(entry))` in SQL; Go re-computes it independently (`docs/reference/audit-canonical-encoding.md`). Entries have typed columns only, and the payload is represented by `sha256(payload_salt ‖ payload)`, where the random per-event salt stays in the operational store, so the hash cannot be reversed by enumerating phone numbers. Every 5 minutes the head is signed with ML-DSA-65 (Go 1.27 `crypto/mldsa`, context `sauti-audit-checkpoint/v1`), stored, and written through the `Witness` port. Append-only triggers block UPDATE/DELETE/TRUNCATE even for the owner.

## Consequences

* Good: rewriting, deleting or truncating history is detected **when verification includes the write-once witness**; signatures are quantum-resistant.
* Bad: a superuser can still disable triggers, rewrite an entry, recompute every later hash with `audit.canonical()` and delete the database checkpoints. Database-only verification is then fooled (review finding C1). Detection, not prevention, is the guarantee, and it rests on the witness living outside the database host's control.

**Witness anchoring (amendment after review C1).**
- `sautictl verify-ledger` requires `--witness-dir`, unless `--no-witness` explicitly accepts the weaker check. Every witnessed checkpoint must carry a valid signature, lie within the chain, match the chain hash at its sequence, and still be present among the database checkpoints.
- outbox-relay anchors its checkpointer on the last witnessed checkpoint, re-read and verified at the start of **every leadership term** (`audit.NewAnchoredCheckpointer`; CodeRabbit PR #4). A standby taking over therefore never signs with a stale anchor. A witness store that does not verify makes the relay refuse to checkpoint.
- `sautictl checkpoint` requires `--witness-dir` and anchors the same way, so a manual checkpoint can never sign a rewritten chain. `sautictl verify-ledger` warns, just as for `--no-witness`, when the witness directory holds no notes (an unmounted volume or a mistyped path).
- It refuses to sign a new head if the ledger no longer holds the anchor's hash at the anchor's sequence. It reads that hash through `audit.hash_at`, which is EXECUTE-only for `audit_appender`. A refusal raises `sauti_audit_chain_diverged`; the next successful checkpoint (after the witnessed history is restored) sets it back to 0.

## Confirmation

`internal/platform/pg/auditstore/auditstore_integration_test.go::TestSQLHashMatchesGoCanonical`, `::TestConcurrentAppendsKeepValidChain`, `::TestOwnerUpdateBlockedByTrigger`; `internal/audit/verify_test.go::TestVerifyDetectsTruncationBeyondCheckpoint`; `internal/audit/witness_test.go::TestVerifyWithWitnessDetectsRewrittenChain`, `::TestCheckpointerRefusesDivergedChain`; `internal/platform/pg/tamper_integration_test.go::TestOwnerRewriteWithRecomputedChainIsCaughtByWitness`; `tests/e2e/m0_fixture_test.go::TestM0FixtureIncident` (steps 7 and 7b).
