# Runbook: alerting pipeline down (the Watchdog stops arriving)

| Field | Value |
|---|---|
| Owner | On-call engineer |
| Severity | SEV-1 (no alert reaches a person while this lasts) |
| Alert / trigger | `Watchdog` (`build/prometheus/sauti-rules.yml`) always fires. The dead man's switch off the monitoring host pages when it stops arriving (P0, R-16). |
| Last reviewed: | 2026-11-14 |
| Last drilled | not yet drilled |

## Summary
The `Watchdog` alert fires at every evaluation, so its arrival proves the path from Prometheus through Alertmanager to the receiver. It cannot report an outage of Prometheus or Alertmanager themselves. The receiver is therefore a dead man's switch off the monitoring host, which pages when the Watchdog stops arriving: no other alert, tier 3 included, can then reach a person.

## Triggers and detection
- The dead man's switch pages because no `Watchdog` arrived within its window. P0 provisions the switch and its window; until then nothing pages, and this runbook applies only to a drill.
- A `Watchdog` that arrives is healthy. Route it to the switch, never to a person.

## Impact
Every alert in `build/prometheus/sauti-rules.yml` is silent: an escalation unacknowledged for 10 minutes, a stale escalation signal, no dispatch leader, an unreadable overdue age. Sauti itself is unaffected: intake, dispatch and the coordinator line keep working. **The dispatch path is not affected**; only the paging of its failures is lost.

## Diagnosis
1. Check Prometheus on the monitoring host: `systemctl status prometheus`, then its `/-/healthy` and `/-/ready` endpoints.
2. Check Alertmanager: `systemctl status alertmanager`, then `/-/healthy`.
3. Check the rules are loaded: Prometheus lists the rule group `sauti-dead-man`, with `Watchdog` firing.
4. Check the route to the switch: Alertmanager's log shows any failed notification.

## Mitigation
1. **Until the pipeline is back, people watch by hand.** The coordinator on duty runs `sautictl escalations` every minute ([no-eligible-responders.md](no-eligible-responders.md)). The on-call engineer runs the dispatch backlog query ([dispatch-worker-down.md](dispatch-worker-down.md), Diagnosis step 2) at least every 15 minutes.
2. Restart whichever of Prometheus and Alertmanager is down.
3. If the rules failed to load, roll back to the last rules file whose CI job "alert rules (promtool)" passed.

## Escalation
Call the technical operator lead by voice within 15 minutes.

## Verification
The switch receives the `Watchdog` again and stops paging, and the `sauti-dead-man` group evaluates without errors.

## References
[ADR-0041](../adr/ADR-0041-evaluation-strategy-and-quality-gates.md) rule 8; `build/prometheus/sauti-rules.yml`; the [evaluation audit](../explanation/evaluation-audit-2026-11.md), V-01; prometheus-operator's Watchdog runbook: https://runbooks.prometheus-operator.dev/runbooks/general/watchdog
