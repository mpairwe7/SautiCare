# Runbook: Outbox relay lag

| Field | Value |
|---|---|
| Owner | Platform engineering on call |
| Severity | SEV-3 (SEV-2 above 30 minutes) |
| Alert / trigger | `sauti_outbox_lag_seconds` > 60 for 5 minutes on the active outbox-relay |
| Last reviewed: | 2026-11-12 |
| Last drilled | Not yet drilled (M0, development only) |

## Summary

Audit outbox rows are not reaching the ledger quickly. The outbox keeps every row (ADR-0004), so nothing is lost; audit becomes current again once the relay catches up.

## Triggers and detection

* Metric `sauti_outbox_lag_seconds` (outbox-relay `/metrics`), alert at > 60 s for 5 min.
* outbox-relay logs `audit relay retrying` with backoff.

## Impact

* **Dispatch is not affected** (rule 3.14).
* Audit entries and checkpoints lag; a checkpoint only covers what has already been relayed.

## Diagnosis

1. `curl -s 127.0.0.1:9101/readyz` on each relay host: one instance should log `outbox-relay is the active leader`.
2. Check the audit database: `pg_isready` and the Patroni leader.
3. Count pending rows (role `op_readonly`): `SELECT count(*), min(created_at) FROM op.outbox WHERE delivered_at IS NULL;`
4. If `sauti_outbox_poison_total` increased, follow `docs/runbooks/outbox-poison-row.md` instead.

## Mitigation

1. Restore audit database availability (Patroni failover is automatic inside Site A).
2. Leadership heals itself. If the lock connection is lost (switchover, restart, network blip), the relay campaigns again and resumes without exiting. Restart a unit (`systemctl restart outbox-relay`) only if no instance logs `outbox-relay is the active leader` within a minute.
3. Do not delete or edit outbox rows.

## Escalation

Platform lead if lag exceeds 30 minutes. DPO if it exceeds 24 hours (audit completeness, ISP §5.5).

## Verification

`sauti_outbox_lag_seconds` returns to under 5 s, and the pending count in step 3 of Diagnosis falls to 0.

## References

ADR-0004; `cmd/outbox-relay/main.go`; `internal/audit/outbox.go`.
