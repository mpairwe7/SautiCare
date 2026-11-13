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
- Incidents stay due for longer than the poll interval: the backlog query (Diagnosis step 2) returns a non-zero `overdue`.
- `incident.escalated` events with reason `dispatch_error`. When a pass for an incident fails (each pass has a 20-second deadline), the worker escalates that incident. This does **not** cover every failure:
  - if claiming due incidents fails (the database is unreachable, for example), no incident is claimed, so none is escalated: the log shows `dispatch pass failed` with a zero incident ID;
  - if recording the `dispatch_error` escalation itself fails, the failure is only logged.

  So the absence of `dispatch_error` events proves nothing. Use the backlog query.
- `incident.escalated` with reason `dispatch_stalled`: intake-gw's watchdog, a separate process, found an incident with no alert and no dispatch escalation two minutes after T2.
- `/readyz` check `dispatch_loop` failing on the leader (no successful pass for two minutes); metrics `sauti_dispatch_last_pass_age_seconds` and `sauti_dispatch_oldest_overdue_seconds` growing, `sauti_dispatch_pass_failures_total` rising.

## Impact
**The dispatch path is affected.** Callers still get their incident number (intake is separate), but no VHT or transporter is alerted, no health centre is called, and the caller is not told help is on the way. Escalations already raised stay visible to coordinators.

## Diagnosis
1. Are the processes running? `systemctl status sauti-dispatch-worker@*` on both sites.
2. The backlog, as `op_readonly`. It counts every wake more than 30 seconds overdue, **leased or not**: an incident whose pass keeps failing stays leased between retries, so a query that skipped leased wakes could read 0 while dispatch is failing. `leased` says how many of them a worker holds now.

   ```sql
   SELECT count(*) AS overdue,
          count(*) FILTER (WHERE lease_until > now()) AS leased,
          coalesce(max(extract(epoch FROM now() - due_at)), 0) AS oldest_seconds
     FROM op.dispatch_timers WHERE due_at < now() - interval '30 seconds';
   ```

   List them, oldest first, to hand to the coordinators: `SELECT incident_id, due_at, lease_until FROM op.dispatch_timers WHERE due_at < now() - interval '30 seconds' ORDER BY due_at;`. The metric `sauti_dispatch_oldest_overdue_seconds` also counts leased wakes.
3. Is there a leader? As `op_readonly`: `SELECT pid, granted FROM pg_locks WHERE locktype = 'advisory' AND objid = 7347004;`
4. Read the latest errors: `journalctl -u sauti-dispatch-worker@* --since -15min | grep -E 'dispatch pass failed|stopped'`.
5. Startup refusals:
   - configuration or ranking order invalid (a stopping-rule rate must be a number strictly between 0 and 1);
   - no gazetteer loaded (`sautictl gazetteer load`);
   - in production, a gazetteer that is not `approved`;
   - in production, "no responder channel yet". This is expected until M2 Part B ships the SMS adapter.
6. Is dispatch suspended rather than down? Run `sautictl dispatch status --dsn <op_readonly DSN>`. If it is suspended, use [suspension-and-resumption.md](suspension-and-resumption.md).
7. Deliveries a worker claimed but never finished (it stopped between claiming the attempt and calling the provider). As `op_readonly`:

   ```sql
   SELECT a.incident_id, x.alert_id, x.purpose, x.claimed_at
     FROM op.alert_attempts x JOIN op.alerts a ON a.id = x.alert_id
    WHERE x.status = 'claimed' AND x.claimed_at < now() - interval '1 minute';
   ```

## Mitigation
1. **First, tell the coordinators** (by voice, ISP §5.6) that automatic dispatch is down. They alert responders by phone from the console's incident list until it is back.
2. Restart the worker: `systemctl restart sauti-dispatch-worker@<n>`. The new leader resumes from the store: every recorded fact and every wake is kept. Delivery is at most once ([ADR-0025](../adr/ADR-0025-dispatch-store-and-first-accept-wins.md)): a claimed delivery attempt is never sent again, because the worker cannot know whether the provider took it. So a worker that stopped after claiming an attempt and before calling the provider leaves that responder **without the alert**. The alert lapses at the cascade timeout (90 s) and the next responder is alerted. For each incident in Diagnosis step 7, the coordinators phone the responder or cover the incident by hand until an acceptance (T4) is recorded.
3. If a configuration change caused a startup refusal, roll the configuration back through the amendment gate. Never hand-edit gated files on a host.
4. If the operational database failed over, the pool reconnects by itself. Leases expire after 30 s and the work is retried.
5. Never insert or update `op.alerts`, `op.assignments` or `op.dispatch_timers` by hand.

## Escalation
The technical operator lead, by voice, within 15 minutes for SEV-1. The Medical Director is told if any red incident waited more than 3 minutes without an alert.

## Verification
- `/readyz` is 200 on every instance and one instance holds lock 7347004.
- The backlog query (Diagnosis step 2) returns `overdue = 0` within a minute and stays at 0.
- No unfinished claim is left (Diagnosis step 7), or the coordinators have covered each one.
- **Simulated delivery proves nothing about phones.** Outside production the worker only logs `simulated delivery`, and production refuses to start until the M2 Part B channel exists. A dev or test incident (`TestEngineYellowCascadeDepthThree` describes the expected sequence) shows that the planner and the store work, not that a responder received an alert. Keep manual dispatch in place until a real alert is confirmed: on a live incident or a drill, the responder's acceptance (T4) is recorded.

## References
[ADR-0024](../adr/ADR-0024-dispatch-planners-and-postgresql-scheduler.md), [ADR-0025](../adr/ADR-0025-dispatch-store-and-first-accept-wins.md), [no-eligible-responders.md](no-eligible-responders.md), Build Prompt §3 rule 14, ISP §5.6.
