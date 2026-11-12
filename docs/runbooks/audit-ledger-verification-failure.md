# Runbook: Audit ledger verification failure

| Field | Value |
|---|---|
| Owner | Platform engineering on call, with the Data Protection Officer |
| Severity | SEV-1 (possible integrity breach) |
| Alert / trigger | `sautictl verify-ledger` exits non-zero (nightly job, restore drill, or manual run) |
| Last reviewed: | 2026-11-12 |
| Last drilled | Not yet drilled (M0, development only) |

## Summary

The hash-chained audit ledger (ADR-0005) no longer verifies: an entry's hash does not match its canonical encoding, the sequence has a gap, a checkpoint signature is invalid, or a checkpoint vouches for entries that are gone (truncation). Treat it as a suspected personal-data or integrity breach until proven otherwise.

## Triggers and detection

* Nightly `sautictl verify-ledger --dsn <audit_reader> --pubkey-file <published key> --witness-dir <witness copy>` (P0 schedule).
* `sauti_audit_chain_diverged` = 1 on outbox-relay: the checkpointer refused to sign.
* Every restore drill and every analytic restore.
* Output names `first_bad_seq` and the first problem.

## Impact

* **Dispatch is not affected**: the relay keeps appending and the operational store is unaffected.
* The audit trail's evidential value for fidelity, deviations and payments (ISP §5.5) is in question from `first_bad_seq` onwards.

## Diagnosis

1. Run `sautictl verify-ledger --dsn <audit_reader> --pubkey-file <published key> --witness-dir <witness copy>` and record the full output. **Always pass the witness.** A database owner who rewrites an entry, recomputes the chain and deletes the database checkpoints fools chain-only verification; only `witnesses_bad > 0` reveals it.
2. Each witness file `checkpoint-<seq>-<time>.txt` carries the signed head at that sequence. The last witness that still verifies proves the ledger was intact up to that sequence.
3. `sauti_audit_chain_diverged = 1`, or the outbox-relay log `AUDIT LEDGER DIVERGED`, means the relay refused to sign a new checkpoint because history before its witnessed anchor changed. Treat it exactly like a verification failure.
4. Check `pg_stat_activity` and the PostgreSQL logs for superuser sessions, `ALTER TABLE … DISABLE TRIGGER`, and `UPDATE`/`DELETE` on `audit.entries` or `audit.checkpoints`.
5. Cross-check: for the incidents involved, `op.incident_events` payload hashes must match `audit.entries.payload_sha256`.

## Mitigation

1. **Preserve evidence first**: take a ZFS snapshot of the audit database host and copy the witness directory read-only. Do not "repair" the ledger.
2. Revoke any credential that could have been used and rotate the `audit_owner` password through OpenBao.
3. Restore the audit store from the most recent backup whose ledger verifies up to the last good checkpoint into a side instance, for comparison only.
4. Record the event in the breach register and open a breach case (ISP §7).

## Escalation

* Immediately: the DPO and the Company Secretary, by voice.
* Within 24 hours: internal notice to the CEO, DPO, Company Secretary and PI (ISP §7).
* Within 48 hours: the assessment. Within 72 hours: PDPO notification if the threshold is met. Within 7 days: the ethics committee, for study data.

## Verification

`sautictl verify-ledger` reports `ledger OK` on the restored or confirmed ledger, and the breach case records the root cause.

## References

ADR-0005; `docs/reference/audit-canonical-encoding.md`; `internal/audit/verify.go`; ISP §5.5, §7.
