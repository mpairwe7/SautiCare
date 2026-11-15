# Runbook: no eligible responder for an incident

| Field | Value |
|---|---|
| Owner | coordinator on duty |
| Severity | SEV-2 per incident; SEV-1 if it repeats in one area within an hour |
| Alert / trigger | the coordinator line rings about an `incident.escalated` event (every reason in the table below); `sautictl escalations`; the page `SautiEscalationUnacknowledged` (intake-gw's `sauti_escalation_oldest_unacknowledged_seconds` over 600 s for 1 minute) |
| Last reviewed: | 2026-11-14 |
| Last drilled | not yet drilled |

## Summary
Dispatch never ends silently (review focus 3). When nobody can be alerted for a role, dispatch records an escalation for the coordinator at once. This happens when the registry is empty, ring 1 holds nobody available, or everyone has declined or not answered. Red incidents are also escalated at 3 minutes without an acceptance. In amended mode (decision 2), the search widens to 2.5 km and then forwards to the nearest health centre, and an escalation is recorded at that moment ([ADR-0024](../adr/ADR-0024-dispatch-planners-and-postgresql-scheduler.md)).

**Every escalation rings the coordinator line** until a coordinator presses 1 ([ADR-0027](../adr/ADR-0027-channels-and-the-alert-ladder.md), [ADR-0034](../adr/ADR-0034-every-failure-path-ends-with-a-person.md)). One call announces every reason on the incident that nobody has acknowledged, most urgent first, each with its role (health worker or transport) where it has one, and pressing 1 acknowledges exactly those; a reason recorded later rings again. The line is rung every 2 minutes. After `escalation.backup_after_calls` unanswered rings (3, PENDING the PI), the backup line (`SAUTI_COORDINATOR_BACKUP_DIAL`) rings as well, and the first 1 on either stops both. intake-gw exports the age of the oldest unacknowledged escalation for the on-call engineer's page (tier 3), and two rules in `build/prometheus/sauti-rules.yml` read it: `SautiEscalationUnacknowledged` pages when `max(sauti_escalation_oldest_unacknowledged_seconds) > 600` for 1 minute, and `SautiEscalationSignalStale` pages when `max(sauti_escalation_gauges_leader) < 1 or absent(sauti_escalation_gauges_leader)` holds for 2 minutes. That second rule means no instance exports fresh values: a former leader resets its gauges to 0, a failed read sets the leader gauge to 0, and `absent` covers every intake-gw target gone ([escalation-signal-stale.md](escalation-signal-stale.md)).

## Triggers and detection
The escalation is an `incident.escalated` event with its reason and, where there is one, the role (`vht` or `transporter`). The coordinator line rings about it. When ivr-controller is down the line does not ring ([ivr-controller-down.md](ivr-controller-down.md)): the coordinator on duty then lists the open escalations at least every minute, on Sauti's servers, as `op_readonly`:

```sh
sautictl escalations --dsn "$SAUTI_DSN"   # add --json for a machine-readable list
```

It prints each open incident with unacknowledged escalations (reference, severity, reasons most urgent first, age, rings) and the open location call-back tasks. It prints references only, never a number.

**The page `SautiEscalationUnacknowledged`** reaches the on-call engineer when an escalation has waited over 10 minutes. It means the coordinator line did not get the escalation acknowledged.
1. Call the coordinator on duty by voice, run `sautictl escalations` together, and act on each line as the table below says.
2. If the coordinator line did not ring, ivr-controller is down or cannot ring out: follow [ivr-controller-down.md](ivr-controller-down.md), with the coordinator listing escalations every minute.
3. If it rang and nobody answered, call the coordinator lead (Escalation).

| Reason | Meaning |
|---|---|
| `triage_rule_error` | the triage rules failed and the call was classified red as a fail-safe: see [triage-rule-error.md](triage-rule-error.md) |
| `red_no_acceptance` | a red incident with a role still unaccepted at 3 minutes (decision 2, both modes); once per open role |
| `dispatch_stalled` | intake-gw's watchdog: two minutes after T2 dispatch has neither alerted anyone nor escalated, or a dispatch timer is overdue — dispatch-worker is down or stuck: see [dispatch-worker-down.md](dispatch-worker-down.md) |
| `dispatch_error` | the worker could not process the incident: see [dispatch-worker-down.md](dispatch-worker-down.md) |
| `dispatch_not_started` | the abandonment sweeper: the caller gave a ward but the call went idle before dispatch started (no T2); nobody has been alerted |
| `voice_handed_over` | the toll-free voice line handed the caller to the coordinator line (wrong keys three times, silence after the caller answered, a failed prompt or an intake error); if nobody answered the transfer, the caller is waiting for a call: see [ivr-controller-down.md](ivr-controller-down.md) |
| `abandoned_after_danger_yes` | the caller answered danger yes and the call went idle before dispatch: see [abandoned-danger-callback.md](abandoned-danger-callback.md) |
| `abandoned_red_before_dispatch` | a red call went idle before dispatch: see [abandoned-danger-callback.md](abandoned-danger-callback.md) |
| `out_of_area_after_danger` | only with the `out_of_area_escalate_after_danger` flag on: the caller chose "somewhere else" after danger yes or a red classification; the incident stays open for a coordinator |
| `no_eligible_responder` | nobody left to try for a role within the search limit (ring 1 in protocol mode, 2.5 km amended) and nobody on the way; once per role |
| `no_responder_nearby` | amended mode: nobody in ring 1 for a role at T2; the search is widening, so help may still come from further away |
| `bridge_unanswered` | a caller rang the bridge and reached nobody. Their responder did not answer or was unreachable: the caller was sent to the coordinator line. Or their report is open with nobody assigned yet: the caller heard the same "nothing to connect" as an unknown number and reaches the coordinator line only by pressing 0 or after three silent tries; a caller who hangs up first is not transferred, but the incident is escalated all the same (at most once per 10 minutes): see [bridge-failing.md](bridge-failing.md) |
| `merged_call_escalated` | recorded by merges before PR #14: a call merged into this incident held an unacknowledged escalation. A merge now records the absorbed call's own reasons and roles on this incident instead: see [split-wrong-merge.md](split-wrong-merge.md) |
| `forwarded_to_facility` | amended mode: 2.5 km reached with a role unaccepted; the nearest health centre is being called |
| `no_facility_acknowledged` | every forwarding health centre was tried and none acknowledged |
| `dispatch_suspended` | dispatch is suspended: see [suspension-and-resumption.md](suspension-and-resumption.md) |
| `assigned_follow_up` | a responder accepted, but the incident is still open after the follow-up time: check with the responder whether help arrived |

## Impact
The incident is recorded and the search goes on: a responder who switches availability on is alerted at the next ring step (60 s). Until someone accepts, the coordinator is the safety net.

## Diagnosis
1. Open the incident on the console (M5); before then, use `op_readonly` queries on `op.alerts` for the incident. Who was alerted, on which ring, and with what outcome (`declined`, `no_response`, `stood_down`)?
2. Is anybody available nearby? Use the console's availability view (M5). In development: `SELECT role, count(*) FROM op.responders r JOIN op.availability a ON a.responder_id = r.id WHERE a.period @> now() GROUP BY role;`
3. Was the location ward-level (no landmark)? The search then runs from every zone of the ward, and the location call-back task also applies.

## Mitigation
1. Not possible before M5: calling the caller back needs the console's audited reveal. Until then, act on what the incident shows (steps 2 to 4), and if the caller rings again, confirm the location and the patient's condition then.
2. Phone responders known to be near, even those who are not marked available. A responder whose alert lapsed can still accept it while the role is open.
3. For a red incident with no transport, call the nearest health centre or ambulance service directly.
4. Record what you did on the incident (console, M5). Do not edit alerts or assignments by hand.
5. If one area keeps escalating, tell the coordinator lead: the area may need more responders, or the stopping rule may be approaching.

## Escalation
Coordinator lead by voice for repeated escalations in one area within an hour. The Medical Director for any red incident without help at 10 minutes.

## Verification
The incident shows an accepted responder (T4) or a coordinator's closure with its reason.

## References
[ADR-0024](../adr/ADR-0024-dispatch-planners-and-postgresql-scheduler.md), [ADR-0025](../adr/ADR-0025-dispatch-store-and-first-accept-wins.md), [events.md](../reference/events.md), Build Prompt §10.4, §19 decision 2.
