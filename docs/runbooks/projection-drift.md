# Runbook: Projection drift

| Field | Value |
|---|---|
| Owner | Platform engineering, with the Principal Investigator |
| Severity | SEV-2 |
| Alert / trigger | `sautictl verify-projections` exits non-zero (nightly and in every restore drill) |
| Last reviewed: | 2026-11-13 |
| Last drilled | Not yet drilled (M0, development only) |

## Summary

An incident's row in `op.incidents` no longer equals the fold of its `op.incident_events` (ADR-0003). The events are the source of truth; the projection is derived from them.

## Triggers and detection

* Nightly `sautictl verify-projections --dsn <op_readonly DSN>` prints `DRIFT <incident> <field>: <db> != <fold>`.

## Impact

* The console and dispatch read the projection, so live decisions for the affected incidents may use wrong state.
* Study timestamps T1–T7 for those incidents are suspect until reconciled.

## Diagnosis

1. Note the incident IDs and fields from the output.
2. Look for manual edits in the PostgreSQL logs, and for a migration that changed `op.append_incident_event` without updating `internal/incident/fold.go`, or the reverse.
3. A difference in the T-fields only after a restore points to a restore or replica problem.

## Mitigation

1. If code diverged: fix whichever side is wrong (SQL or Go) in a pull request, with a test that reproduces the drift first.
2. To repair a projection: as owner, recompute that incident's projection from its events in a forward migration or an operator script, under a deviation record. Never edit events. **Serialise the repair with writes to that incident.** `op.append_incident_event` locks the incident's `op.incidents` row (`SELECT … FOR UPDATE`) before it reads or writes anything. The repair must take the same lock **before** it reads the events, and keep it until it has written the projection. Otherwise an event appended between the read and the write is overwritten by a stale fold.

   ```sql
   BEGIN;
   SET LOCAL lock_timeout = '5s';
   -- 1. Lock first: appends to this incident now wait until COMMIT.
   SELECT version FROM op.incidents WHERE id = '<incident id>' FOR UPDATE;
   -- 2. Only now read the events, and fold them as internal/incident/fold.go does.
   SELECT seq, type, recorded_at, actor_kind, staff_id, payload, logic_version
     FROM op.incident_events WHERE incident_id = '<incident id>' ORDER BY seq;
   -- 3. Write the fold of exactly those events.
   UPDATE op.incidents SET <field> = <folded value>, ... WHERE id = '<incident id>';
   COMMIT;
   ```

   - Fold exactly the events the script reads after taking the lock. The `<fold>` values in an earlier `DRIFT` line come from an older snapshot and are stale if an event arrived since. To reuse them, re-run `sautictl verify-projections` while this transaction holds the lock: no event for this incident can commit before `COMMIT`.
   - Keep the transaction short: intake writes to that incident wait on the lock and give up after their 5 s `statement_timeout`.
   - A forward migration runs in one transaction, so it follows the same order: lock, read, write.
3. Tell the coordinator about any affected active incident so they can follow it by phone.

## Escalation

PI, for the study record. DPO, if a manual edit by an unauthorised person is suspected.

## Verification

`sautictl verify-projections` reports `drift=0`.

## References

ADR-0003; `internal/incident/fold.go`; `internal/platform/pg/opstore/verify.go`.
