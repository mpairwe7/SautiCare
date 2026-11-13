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
- `sautictl verify-ledger` requires `--witness-dir`, unless `--no-witness` explicitly accepts the weaker check; the two flags are mutually exclusive. Every witnessed checkpoint must carry a valid signature, lie within the chain, match the chain hash at its sequence, and still be present among the database checkpoints.
- outbox-relay anchors its checkpointer on the last witnessed checkpoint, re-read and verified at the start of **every leadership term** (`audit.NewAnchoredCheckpointer`; CodeRabbit PR #4). A standby taking over therefore never signs with a stale anchor. A witness store that does not verify makes the relay refuse to checkpoint.
- **Two writers, coordinated through the witness (CodeRabbit PR #4).** The relay's leader keeps its checkpointer for the whole term while `sautictl checkpoint` may sign at any time, so neither can rely on the anchor it read earlier. With the relay anchored at seq 3 and a manual checkpoint witnessed at seq 7, a rewrite of entry 5 leaves seq 3 intact; an anchor read once per term would sign it. Every `audit.Checkpointer` therefore reads the witness again **before every signature**. Each note it has not seen before is signature-checked (a note that does not parse or verify stops signing) and checked against the chain through `audit.hash_at`, the anchor moves to the latest note, and the checkpointer refuses unless the ledger still holds that note's head. The coordination is refresh-and-verify, not a lock. A lock would serialise the writers, but it would live in the database whose owner is the adversary and tie the command to the relay's leadership; detection does not need it. The one interleaving refresh-and-verify leaves open (a manual note witnessed between the relay's read and its signature, with a rewrite inside that window) still leaves the honest note in the write-once witness: the next checkpoint checks every new note, not only the latest, and refuses, and `verify-ledger` fails. This requires both writers to use the **same** witness directory (`SAUTI_WITNESS_DIR` of the relay, `--witness-dir` of the command). Each note is verified once (remembered by SHA-256), so the per-checkpoint cost is reading the directory plus one `hash_at` per new note. The checkpointer does not re-hash the entries after the anchor: `audit_appender` cannot read `audit.entries`, and `verify-ledger` (as `audit_reader`) does that.
- `sautictl checkpoint` requires `--witness-dir` and anchors the same way, on the latest verified note. Once the witness holds a note, a manual checkpoint cannot sign a rewritten chain. With no note there is no anchor, and the command (like outbox-relay) signs the current head. That is how a new ledger gets its first checkpoint, and the code cannot tell it apart from an emptied or unmounted witness, so the operator confirms the witness path before a first checkpoint.
- `sautictl verify-ledger` with a `--witness-dir` that holds no notes (an unmounted volume or a mistyped path) prints the same warning as `--no-witness` and then **fails** (CodeRabbit PR #5). A nightly job must never report `ledger OK` while its witness copy is missing, because chain-only verification is exactly what a recomputed chain fools. Until a new deployment's first checkpoint is witnessed, verification runs with `--no-witness`.
- It refuses to sign a new head if the ledger no longer holds the anchor's hash at the anchor's sequence. It reads that hash through `audit.hash_at`, which is EXECUTE-only for `audit_appender`. A refusal raises `sauti_audit_chain_diverged`; the next successful checkpoint (after the witnessed history is restored) sets it back to 0.

## Confirmation

`internal/platform/pg/auditstore/auditstore_integration_test.go::TestSQLHashMatchesGoCanonical`, `::TestConcurrentAppendsKeepValidChain`, `::TestOwnerUpdateBlockedByTrigger`; `internal/audit/verify_test.go::TestVerifyDetectsTruncationBeyondCheckpoint`; `internal/audit/witness_test.go::TestVerifyWithWitnessDetectsRewrittenChain`, `::TestCheckpointerRefusesDivergedChain`, `::TestRelayCheckpointRefusesARewriteBehindAManualCheckpoint`, `::TestCheckpointerChecksEveryNewWitnessNote`, `::TestCheckpointerFailsClosedOnACorruptNoteMidTerm`; `cmd/sautictl/checkpoint_integration_test.go::TestRelayCheckpointRefusesARewriteBehindTheCommandsCheckpoint`; `cmd/sautictl/ledger_test.go::TestVerifyLedgerFailsWhenTheWitnessDirHoldsNoNotes`, `::TestVerifyLedgerRejectsWitnessDirWithNoWitness`; `internal/platform/pg/tamper_integration_test.go::TestOwnerRewriteWithRecomputedChainIsCaughtByWitness`; `tests/e2e/m0_fixture_test.go::TestM0FixtureIncident` (steps 7 and 7b).
