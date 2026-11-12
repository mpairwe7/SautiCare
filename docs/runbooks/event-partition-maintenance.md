# Runbook: Event partition maintenance

| Field | Value |
|---|---|
| Owner | Platform engineering |
| Severity | SEV-3 |
| Alert / trigger | `SELECT op.default_partition_rows()` > 0, or fewer than 2 future monthly partitions exist |
| Last reviewed: | 2026-11-12 |
| Last drilled | Not yet drilled (M0, development only) |

## Summary

`op.incident_events` is partitioned by month (ADR-0003). A DEFAULT partition catches rows if a monthly partition is missing, so a dispatch write never fails, but rows there break the "one period, whole partitions" retention mapping.

## Triggers and detection

* Monitoring query (role `op_readonly`): `SELECT op.default_partition_rows();` (alert when > 0).
* Daily maintenance runs `op.ensure_event_partitions(3)` as the owner (retention-agent, M6).

## Impact

* **Dispatch is not affected**: writes succeed in the default partition.
* Retention by partition and the key period is blocked for the affected rows until they are moved.

## Diagnosis

1. List partitions: `SELECT inhrelid::regclass FROM pg_inherits WHERE inhparent = 'op.incident_events'::regclass;`
2. Find the months of the stray rows: `SELECT date_trunc('month', recorded_at), count(*) FROM op.incident_events_default GROUP BY 1;`

## Mitigation

1. As owner, in one transaction, during low traffic:
   `BEGIN; ALTER TABLE op.incident_events DETACH PARTITION op.incident_events_default;`
   `SELECT op.ensure_event_partitions(3);`
   `INSERT INTO op.incident_events SELECT * FROM op.incident_events_default;`
   `TRUNCATE op.incident_events_default;`
   `ALTER TABLE op.incident_events ATTACH PARTITION op.incident_events_default DEFAULT; COMMIT;`
2. This moves rows without changing them, so their IDs, timestamps and hashes are preserved. Record it as a maintenance deviation.

## Escalation

Platform lead.

## Verification

`SELECT op.default_partition_rows();` returns 0, and `sautictl verify-projections` passes.

## References

ADR-0003; `db/operational/migrations/0001_core.sql`; `internal/platform/pg/opstore/opstore_integration_test.go::TestEventLandsInDefaultPartitionAndIsReported`.
