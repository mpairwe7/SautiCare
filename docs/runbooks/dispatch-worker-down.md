# Runbook: dispatch-worker down or not dispatching

| Field | Value |
|---|---|
| Owner | technical operator on call |
| Severity | SEV-1 (responders are not being alerted) |
| Alert / trigger | `/readyz` on dispatch-worker (127.0.0.1:9102) failing; no process holding advisory lock 7347004; incidents in `dispatching` with no alert more than 60 s after T2; repeated `dispatch pass failed` log lines; `dispatch_error` escalations |
| Last reviewed: | 2026-11-13 |
| Last drilled | not yet drilled |

## Summary
dispatch-worker decides who is alerted and records it ([ADR-0024](../adr/ADR-0024-dispatch-planners-and-postgresql-scheduler.md)). If no instance holds the leader lock, or every pass fails, incidents are recorded but nobody is alerted. Coordinators must dispatch by hand until it is back.

## Triggers and detection
- `/readyz` returns 503 on every instance, or no instance logs `dispatch-worker starting`.
- Incidents stay due for longer than the poll interval: run `SELECT count(*) FROM op.dispatch_timers WHERE due_at < now() - interval '30 seconds' AND (lease_until IS NULL OR lease_until < now());` as `op_readonly`.
- `incident.escalated` events with reason `dispatch_error`. The worker escalates any incident it cannot process (each pass has a 20-second deadline).
- `incident.escalated` with reason `dispatch_stalled`: intake-gw's watchdog, a separate process, found an incident with no alert and no dispatch escalation two minutes after T2.
- `/readyz` check `dispatch_loop` failing on the leader (no successful pass for two minutes); metrics `sauti_dispatch_last_pass_age_seconds` and `sauti_dispatch_oldest_overdue_seconds` growing, `sauti_dispatch_pass_failures_total` rising.

## Impact
**The dispatch path is affected.** Callers still get their incident number (intake is separate), but no VHT or transporter is alerted, no health centre is called, and the caller is not told help is on the way. Escalations already raised stay visible to coordinators.

## Diagnosis
1. Are the processes running? `systemctl status sauti-dispatch-worker@*` on both sites.
2. Is there a leader? As `op_readonly`: `SELECT pid, granted FROM pg_locks WHERE locktype = 'advisory' AND objid = 7347004;`
3. Read the latest errors: `journalctl -u sauti-dispatch-worker@* --since -15min | grep -E 'dispatch pass failed|stopped'`.
4. Startup refusals:
   - configuration or ranking order invalid;
   - no gazetteer loaded (`sautictl gazetteer load`);
   - in production, "no responder channel yet". This is expected until M2 Part B ships the SMS adapter.
5. Is dispatch suspended rather than down? Run `sautictl dispatch status --dsn <op_readonly DSN>`. If it is suspended, use [suspension-and-resumption.md](suspension-and-resumption.md).

## Mitigation
1. **First, tell the coordinators** (by voice, ISP §5.6) that automatic dispatch is down. They alert responders by phone from the console's incident list until it is back.
2. Restart the worker: `systemctl restart sauti-dispatch-worker@<n>`. The new leader resumes from the store; nothing is lost, and no claimed delivery attempt is sent twice ([ADR-0025](../adr/ADR-0025-dispatch-store-and-first-accept-wins.md)).
3. If a configuration change caused a startup refusal, roll the configuration back through the amendment gate. Never hand-edit gated files on a host.
4. If the operational database failed over, the pool reconnects by itself. Leases expire after 30 s and the work is retried.
5. Never insert or update `op.alerts`, `op.assignments` or `op.dispatch_timers` by hand.

## Escalation
The technical operator lead, by voice, within 15 minutes for SEV-1. The Medical Director is told if any red incident waited more than 3 minutes without an alert.

## Verification
- `/readyz` is 200 on every instance and one instance holds lock 7347004.
- The count of overdue timers (Diagnosis step 2) returns 0 within a minute.
- A dev or test incident is alerted end to end (`TestEngineYellowCascadeDepthThree` describes the expected sequence).

## References
[ADR-0024](../adr/ADR-0024-dispatch-planners-and-postgresql-scheduler.md), [ADR-0025](../adr/ADR-0025-dispatch-store-and-first-accept-wins.md), [no-eligible-responders.md](no-eligible-responders.md), Build Prompt §3 rule 14, ISP §5.6.
