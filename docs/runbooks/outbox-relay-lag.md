# Runbook: Outbox relay lag

| Field | Value |
|---|---|
| Owner | Platform engineering on call |
| Severity | SEV-3 (SEV-2 above 30 minutes) |
| Alert / trigger | Any of: `sauti_outbox_lag_seconds` > 60 for 5 minutes on the active outbox-relay; the oldest pending audit outbox row, measured in the database, older than 60 s for 5 minutes; no session holding the relay leader lock for 1 minute |
| Last reviewed: | 2026-11-13 |
| Last drilled | Not yet drilled (M0, development only) |

## Summary

Audit outbox rows are not reaching the ledger quickly. The outbox keeps every row (ADR-0004), so nothing is lost; audit becomes current again once the relay catches up.

## Triggers and detection

* Metric `sauti_outbox_lag_seconds` (outbox-relay `/metrics`), alert at > 60 s for 5 min. Only a running leader sets it, after a successful claim. It keeps its last value while claims fail, and it vanishes when the relay exits, so on its own it misses a stopped, leaderless or disconnected relay. The next two checks run against the operational database and do not depend on the relay.
* **Pending-row age** (role `op_readonly`); alert when above 60 for 5 min:
  `SELECT coalesce(extract(epoch FROM clock_timestamp() - min(created_at)), 0) AS oldest_pending_seconds FROM op.outbox WHERE topic = 'audit' AND delivered_at IS NULL;`
* **Leader present** (role `op_readonly`). The active relay holds the session advisory lock `7347002` (`leaderKey` in `cmd/outbox-relay/main.go`) on the operational database. Alert when this returns 0 for 1 min:
  `SELECT count(*) AS leaders FROM pg_locks WHERE locktype = 'advisory' AND database = (SELECT oid FROM pg_database WHERE datname = current_database()) AND classid = 0 AND objid = 7347002 AND objsubid = 1 AND granted;`
* outbox-relay logs `audit relay retrying` with backoff. An error ending in `context deadline exceeded` means a call to the operational or audit store waited more than 10 s for a pool connection and its statement (ADR-0004): look for an exhausted pool or a stalled database.

## Impact

* **Dispatch is not affected** (rule 3.14).
* Audit entries and checkpoints lag; a checkpoint only covers what has already been relayed.

## Diagnosis

1. Find the current leader from the lock itself, not from the logs (role `op_readonly`):
   `SELECT l.pid, a.application_name FROM pg_locks l JOIN pg_stat_activity a USING (pid) WHERE l.locktype = 'advisory' AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database()) AND l.classid = 0 AND l.objid = 7347002 AND l.objsubid = 1 AND l.granted;`
   One row with `application_name` `outbox-relay` means an instance is leading. No row means no instance leads. To see which host leads, add `a.client_addr` and run it as a role with `pg_read_all_stats`; for other roles' sessions PostgreSQL shows it as NULL. `curl -s 127.0.0.1:9101/readyz` on a relay host shows only that the process is up and reaches the operational store, not that it leads.
2. Check the audit database: `pg_isready` and the Patroni leader.
3. Count pending rows (role `op_readonly`): `SELECT count(*), min(created_at) FROM op.outbox WHERE delivered_at IS NULL;`
4. If `sauti_outbox_poison_total` increased, follow `docs/runbooks/outbox-poison-row.md` instead.

## Mitigation

1. Restore audit database availability (Patroni failover is automatic inside Site A).
2. Leadership heals itself. If the lock connection is lost (switchover, restart, network blip), the relay campaigns again and resumes without exiting. A relay that stops with an error of its own (a poison row, a witness that fails verification) exits instead of campaigning again: follow the runbook its log line names. Judge leadership by the lock query in Diagnosis step 1, never by a recent log line: a healthy leader logs `outbox-relay is the active leader` only when it takes the lock, possibly days ago. Restart a unit (`systemctl restart outbox-relay`) only if:
   - the query returns no row for a minute while relay processes are running; or
   - it returns a leader while the pending-row age keeps rising, the audit database is healthy and that leader logs no `audit relay retrying`. Restart the unit on that leader's host, so a standby takes the lock.
3. Do not delete or edit outbox rows.

## Escalation

Platform lead if lag exceeds 30 minutes. DPO if it exceeds 24 hours (audit completeness, ISP §5.5).

## Verification

`sauti_outbox_lag_seconds` returns to under 5 s, the pending-row age query returns under 5, the pending count in step 3 of Diagnosis falls to 0, and the leader query returns exactly one row.

## References

ADR-0004; `cmd/outbox-relay/main.go`; `internal/audit/outbox.go`.
