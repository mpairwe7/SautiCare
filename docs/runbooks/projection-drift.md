# Runbook: Projection drift

| Field | Value |
|---|---|
| Owner | Platform engineering, with the Principal Investigator |
| Severity | SEV-2 |
| Alert / trigger | `sautictl verify-projections` exits non-zero (nightly and in every restore drill) |
| Last reviewed: | 2026-11-12 |
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
2. To repair a projection: as owner, recompute that incident's projection from its events in a forward migration or an operator script, under a deviation record. Never edit events.
3. Tell the coordinator about any affected active incident so they can follow it by phone.

## Escalation

PI, for the study record. DPO, if a manual edit by an unauthorised person is suspected.

## Verification

`sautictl verify-projections` reports `drift=0`.

## References

ADR-0003; `internal/incident/fold.go`; `internal/platform/pg/opstore/verify.go`.
