# Runbook: surge — more emergencies than responders

| Field | Value |
|---|---|
| Owner | coordinator lead, with the technical operator on call |
| Severity | SEV-2 (calls wait longer for a responder); SEV-1 if red calls wait past the red escalation time |
| Alert / trigger | `sautictl dispatch status` shows `surge: YES`; many `no_eligible_responder` or `red_no_acceptance` escalations; the console's surge view (M5) |
| Last reviewed: | 2026-11-13 |
| Last drilled | not yet drilled |

## Summary
In a surge, more calls are waiting for a first acceptance than there are responders free (eligible, holding no open assignment). Nothing is dropped ([ADR-0028](../adr/ADR-0028-masked-bridge-and-surge.md)):
- Every call with a role still open wakes every ring interval and alerts whoever has become free.
- Calls are served most severe first, then the oldest call (T2) first.
- Coordinators are escalated to as usual.

## Triggers and detection
- `sautictl dispatch status --dsn <op_readonly DSN>` prints `surge: YES (N waiting, M free responders)`.
- As `op_readonly`, the queue in the order dispatch serves it:

  ```sql
  SELECT i.reference, i.severity, i.t2
    FROM op.incidents i
   WHERE i.state = 'dispatching'
   ORDER BY CASE i.severity WHEN 'red' THEN 0 WHEN 'yellow' THEN 1 ELSE 2 END, i.t2;
  ```

## Impact
Calls wait longer for a responder; red calls may pass the 3-minute red escalation (decision 2), which escalates them to a coordinator.

## Diagnosis
1. Is it real demand (a mass-casualty event, a road crash) or lost capacity? Check `free responders` against the registry: many responders switched off, an availability period ended at a shift change, or gates lapsed (insurance, certification).
2. Are responders stuck on assignments that should have been released (incidents closed late)?

## Mitigation
1. Coordinators work the escalation list, most severe first, and hand calls to health centres or other services as the protocol allows.
2. Ask off-duty responders to switch on through the USSD menu (option 3). Each switch-on is picked up within one ring interval.
3. Close incidents that are over, so their responders are free again.
4. Do **not** suspend dispatch to "clear the queue". Suspension stops alerts to responders altogether (O6). It is for an unsafe system, not for load.

## Escalation
Coordinator lead immediately for red calls waiting past 3 minutes; the PI if the surge lasts more than an hour (a reportable event for the study).

## Verification
`surge: no`, and the queue query returns no red calls older than the red escalation time.

## References
[ADR-0028](../adr/ADR-0028-masked-bridge-and-surge.md), [ADR-0024](../adr/ADR-0024-dispatch-planners-and-postgresql-scheduler.md), [no-eligible-responders.md](no-eligible-responders.md), [suspension-and-resumption.md](suspension-and-resumption.md), Build Prompt §10.4.
