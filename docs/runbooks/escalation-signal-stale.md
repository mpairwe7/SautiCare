# Runbook: escalation signal stale (intake-gw down or leaderless)

| Field | Value |
|---|---|
| Owner | On-call engineer |
| Severity | SEV-1 (tier 3 cannot page while this lasts) |
| Alert / trigger | `SautiEscalationSignalStale`: `max(sauti_escalation_gauges_leader) < 1 or absent(sauti_escalation_gauges_leader)` for 2 minutes (`build/prometheus/sauti-rules.yml`) |
| Last reviewed: | 2026-11-14 |
| Last drilled | not yet drilled |

## Summary
intake-gw's leader exports the age of the oldest unacknowledged escalation: the dead-man signal, tier 3 ([ADR-0029](../adr/ADR-0029-interaction-standards-for-voice-ussd-sms.md) rule 8). This alert means no instance exports a fresh value, so `SautiEscalationUnacknowledged` cannot fire. Until it clears, a person looks at the escalations by hand.

## Triggers and detection
`SautiEscalationSignalStale` pages the on-call engineer after 2 minutes of any of these:
- No intake-gw target is scraped, because every instance is down or Prometheus cannot reach them. The series is absent.
- No instance leads: no intake-gw holds advisory lock 7347003, and every instance exports `sauti_escalation_gauges_leader 0`.
- The leader's read of the escalations fails. The leader gauge is 0 until a read succeeds, and the log shows `escalation gauges not updated`.

A failover between instances takes about 5 seconds and does not page.

## Impact
Tier 3 is blind: an escalation that no coordinator acknowledges pages nobody. Tiers 1 and 2 (the coordinator line, rung by ivr-controller) still work. If ivr-controller is down too, nothing automated reaches a person about an escalation.

If every intake-gw is down, USSD intake is down as well ([intake-gateway-errors.md](intake-gateway-errors.md)), and so are the abandonment sweeper and the dispatch watchdog. **The dispatch path continues**, because dispatch-worker is separate. But an incident that dispatch never touches is no longer escalated by the watchdog.

## Diagnosis
1. First, list the escalations on Sauti's servers, as `op_readonly`, and hand every line to the coordinator on duty (Mitigation step 1): `sautictl escalations --dsn "$SAUTI_DSN"`.
2. Check the processes on both sites: `systemctl status sauti-intake-gw@*`.
3. Check readiness on each host: `curl -s 127.0.0.1:9100/readyz`. `operational_db: fail` means the database is unreachable.
4. Find the leader. On each host, run `curl -s 127.0.0.1:9100/metrics | grep -E '^sauti_escalation'`: one host shows `sauti_escalation_gauges_leader 1`. Then, as `op_readonly`: `SELECT pid, granted FROM pg_locks WHERE locktype = 'advisory' AND objid = 7347003;`
5. Read the latest errors: `journalctl -u sauti-intake-gw@* --since -15min | grep -E 'escalation gauges not updated|abandonment sweep failed|dispatch watchdog failed|stopped'`.
6. If an instance is up and shows `sauti_escalation_gauges_leader 1`, Prometheus cannot scrape it. Check `up{job="intake-gw"}` in Prometheus.

## Mitigation
1. **Tell the coordinator on duty by voice** that unacknowledged escalations will not page. Until this alert clears, the coordinator runs `sautictl escalations` every minute and acts on each line as [no-eligible-responders.md](no-eligible-responders.md) says.
2. If `/readyz` reports `operational_db: fail`, restore the database first (database runbooks). intake-gw cannot read or lead without it.
3. Restart intake-gw one instance at a time with `systemctl restart sauti-intake-gw@<n>`. Wait for `/readyz` to return `ok` before you restart the next. The first instance to take lock 7347003 leads; the other stands by.
4. If an instance holds the lock but its leader gauge stays at 0, restart that instance. The lock passes to the other.
5. Never edit escalation or event rows by hand.

## Escalation
Call the technical operator lead by voice within 15 minutes (SEV-1). If ivr-controller is also down, call the coordinator lead by voice at once: nothing automated is ringing anyone.

## Verification
- In Prometheus, `max(sauti_escalation_gauges_leader)` is 1 and the alert has resolved.
- On the leader, `sauti_escalation_oldest_unacknowledged_seconds` matches the oldest age that `sautictl escalations` prints, to within the 15-second read interval.

## References
[ADR-0029](../adr/ADR-0029-interaction-standards-for-voice-ussd-sms.md) rule 8, [ADR-0034](../adr/ADR-0034-every-failure-path-ends-with-a-person.md), [ADR-0041](../adr/ADR-0041-evaluation-strategy-and-quality-gates.md) rule 8; `build/prometheus/sauti-rules.yml` and its tests; [no-eligible-responders.md](no-eligible-responders.md); [ivr-controller-down.md](ivr-controller-down.md); Build Prompt §3 rule 14.
