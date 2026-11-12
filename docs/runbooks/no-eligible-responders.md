# Runbook: no eligible responder for an incident

| Field | Value |
|---|---|
| Owner | coordinator on duty |
| Severity | SEV-2 per incident; SEV-1 if it repeats in one area within an hour |
| Alert / trigger | `incident.escalated` with reason `no_eligible_responder`, `no_responder_nearby`, `red_no_acceptance`, `forwarded_to_facility`, `no_facility_acknowledged` or `dispatch_stalled` |
| Last reviewed: | 2026-11-13 |
| Last drilled | not yet drilled |

## Summary
Dispatch never ends silently (review focus 3). When nobody can be alerted for a role, the coordinator is alerted at once. This happens when the registry is empty, ring 1 holds nobody available, or everyone has declined or not answered. Red incidents also alert the coordinator at 3 minutes without an acceptance. In amended mode (decision 2), the search widens to 2.5 km and then forwards to the nearest health centre, and the coordinator is alerted at that moment ([ADR-0024](../adr/ADR-0024-dispatch-planners-and-postgresql-scheduler.md)).

## Triggers and detection
The escalation appears on the coordinator's incident list with its reason and, where there is one, the role (`vht` or `transporter`).

| Reason | Meaning |
|---|---|
| `no_eligible_responder` | nobody left to try for a role within the search limit (ring 1 in protocol mode, 2.5 km amended) and nobody on the way; once per role |
| `no_responder_nearby` | amended mode: nobody in ring 1 for a role at T2; the search is widening, so help may still come from further away |
| `red_no_acceptance` | a red incident with a role still unaccepted at 3 minutes (decision 2, both modes); once per open role |
| `forwarded_to_facility` | amended mode: 2.5 km reached with a role unaccepted; the nearest health centre is being called |
| `no_facility_acknowledged` | every forwarding health centre was tried and none acknowledged |
| `dispatch_suspended` | dispatch is suspended: see [suspension-and-resumption.md](suspension-and-resumption.md) |
| `dispatch_error` | the worker could not process the incident: see [dispatch-worker-down.md](dispatch-worker-down.md) |
| `dispatch_stalled` | intake-gw's watchdog: two minutes after T2 dispatch has neither alerted anyone nor escalated — dispatch-worker is down or stuck: see [dispatch-worker-down.md](dispatch-worker-down.md) |

## Impact
The incident is recorded and the search goes on: a responder who switches availability on is alerted at the next ring step (60 s). Until someone accepts, the coordinator is the safety net.

## Diagnosis
1. Open the incident on the console (M5); before then, use `op_readonly` queries on `op.alerts` for the incident. Who was alerted, on which ring, and with what outcome (`declined`, `no_response`, `stood_down`)?
2. Is anybody available nearby? Use the console's availability view (M5). In development: `SELECT role, count(*) FROM op.responders r JOIN op.availability a ON a.responder_id = r.id WHERE a.period @> now() GROUP BY role;`
3. Was the location ward-level (no landmark)? The search then runs from every zone of the ward, and the location call-back task also applies.

## Mitigation
1. Call the caller back with the incident number. Confirm the location and the patient's condition.
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
