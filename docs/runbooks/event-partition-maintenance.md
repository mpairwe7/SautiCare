# Runbook: Event partition maintenance

| Field | Value |
|---|---|
| Owner | Platform engineering |
| Severity | SEV-3 |
| Alert / trigger | `SELECT op.default_partition_rows()` > 0, or fewer than 2 future monthly partitions exist |
| Last reviewed: | 2026-11-13 |
| Last drilled | Not yet drilled (M0, development only) |

## Summary

`op.incident_events` is partitioned by month (ADR-0003). A DEFAULT partition catches rows if a monthly partition is missing, so a dispatch write never fails, but rows there break the "one period, whole partitions" retention mapping.

## Triggers and detection

* Monitoring query (role `op_readonly`): `SELECT op.default_partition_rows();` (alert when > 0).
* The active outbox-relay (leader) runs `op.ensure_event_partitions(3)` at start and every 24 h as `op_relay`, and logs `event partitions ensured` with the number created. A logged failure retries at the next interval.

## Impact

* **Dispatch is not affected**: writes succeed in the default partition.
* Retention by partition and the key period is blocked for the affected rows until they are moved.

## Diagnosis

1. List partitions: `SELECT inhrelid::regclass FROM pg_inherits WHERE inhparent = 'op.incident_events'::regclass;`
2. Find the UTC months of the stray rows (role `op_readonly`): `SELECT date_trunc('month', recorded_at AT TIME ZONE 'UTC') AS month_utc, count(*) FROM op.incident_events WHERE tableoid = 'op.incident_events_default'::regclass GROUP BY 1 ORDER BY 1;`
3. Note the total for Verification: `SELECT count(*) FROM op.incident_events;`

## Mitigation

1. As the store owner, run this script during low traffic. It is one transaction; save it to a file and run it with `psql -v ON_ERROR_STOP=1 -f <file>`, so any error stops it and the transaction rolls back.
   - The DEFAULT partition is detached first, because PostgreSQL refuses to create a partition for a range that rows in the DEFAULT partition already hold.
   - Once detached, a row whose month has no partition has nowhere to go. The script therefore creates a partition for **every month that holds stray rows** (Diagnosis step 2), including past months and months beyond the three-month window of `op.ensure_event_partitions`. It uses the same name pattern and UTC month bounds as that function.
   - `DETACH PARTITION` blocks every write to `op.incident_events` until `COMMIT`, and intake connections give up after their 5 s `statement_timeout`, so keep the transaction short.

   ```sql
   BEGIN;
   ALTER TABLE op.incident_events DETACH PARTITION op.incident_events_default;
   SELECT op.ensure_event_partitions(3);
   DO $$
   DECLARE
       v_month date;
       v_name  text;
   BEGIN
       FOR v_month IN
           SELECT DISTINCT date_trunc('month', recorded_at AT TIME ZONE 'UTC')::date
           FROM op.incident_events_default ORDER BY 1
       LOOP
           v_name := 'incident_events_y' || to_char(v_month, 'YYYY') || 'm' || to_char(v_month, 'MM');
           IF to_regclass('op.' || v_name) IS NULL THEN
               EXECUTE format('CREATE TABLE op.%I PARTITION OF op.incident_events FOR VALUES FROM (%L) TO (%L)',
                              v_name, v_month::timestamp AT TIME ZONE 'UTC',
                              (v_month + interval '1 month')::timestamp AT TIME ZONE 'UTC');
               RAISE NOTICE 'created op.%', v_name;
           END IF;
       END LOOP;
   END
   $$;
   INSERT INTO op.incident_events SELECT * FROM op.incident_events_default;
   TRUNCATE op.incident_events_default;
   ALTER TABLE op.incident_events ATTACH PARTITION op.incident_events_default DEFAULT;
   COMMIT;
   ```
2. This moves rows without changing them, so their IDs, timestamps and hashes are preserved. Record it as a maintenance deviation, listing the partitions the script created.

## Escalation

Platform lead.

## Verification

`SELECT op.default_partition_rows();` returns 0, `SELECT count(*) FROM op.incident_events;` is no lower than the total from Diagnosis step 3 (new events may have arrived since), and `sautictl verify-projections` passes.

## References

ADR-0003; `db/operational/migrations/0001_core.sql`; `internal/platform/pg/opstore/opstore_integration_test.go::TestEventLandsInDefaultPartitionAndIsReported`.
