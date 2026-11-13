# Runbook: Abandoned session after a danger "yes" or a red classification

| Field | Value |
|---|---|
| Owner | Coordinator on duty (clinical call-back) with the Medical Director |
| Severity | SEV-1 (a person may be in danger with no location) |
| Alert / trigger | `incident.escalated` with reason `abandoned_after_danger_yes` or `abandoned_red_before_dispatch` |
| Last reviewed: | 2026-11-14 |
| Last drilled | not yet drilled |

## Summary
A caller answered yes at screen 2 (heavy bleeding, unconscious or not breathing normally), or was classified red by the category's danger question at screen 5 whatever they said at screen 2, then the session went silent for `abandon_after` before dispatch began (no T2). The Guide (§4) calls this the most dangerous partial record. The coordinator calls the caller back at once.

## Triggers and detection
intake-gw's abandonment sweeper (one leader across instances) flags the incident `abandoned` and escalates it, in one transaction. A caller who resumed an incident flagged `abandoned` earlier, then gave a danger `yes` (or was classified red) and dropped again before T2, is swept again and escalated, unless the incident is already escalated. The escalation rings the coordinator line until a coordinator presses 1. A call located to its ward whose dispatch never started is escalated `dispatch_not_started` the same way ([ADR-0034](../adr/ADR-0034-every-failure-path-ends-with-a-person.md)). If the line does not ring (ivr-controller down), list the open escalations with `sautictl escalations`.

## Impact
No dispatch has started: no location was given, or (rarely) the request that would have recorded T2 failed and the caller did not redial. Every minute counts.

## Diagnosis
1. Find the incident (read-only role): `SELECT id, reference, t1 FROM op.incidents WHERE escalated AND 'abandoned' = ANY (flags) AND t2 IS NULL ORDER BY t1 DESC;`
2. If the caller redialled within the resume window, the same incident continues and reaches dispatch on its own (a redial first records any T2 a failed request missed). Check whether `last_screen` has advanced or `t2` is set.
3. `last_screen = 8` with `t2` empty means Tool 1 finished but T2 was not recorded: the ward and landmark are on the incident; dispatch from them.

## Mitigation
**Deployment restriction until M5.** Calling back needs the caller's number, which only the console's audited reveal (M5) may show. No interim tool opens a sealed identifier, and none may be improvised: that would bypass the identifier access controls. Sauti therefore must not take live calls before M5. Production already refuses to start while clinical copy is PENDING and the key provider (P0) is absent. In development and test, record the escalation and the incident reference, then follow the steps below once M5 is in place.

1. Call back through the audited reveal (M5). Never copy a number into chat or a messaging app (rule 3.6).
2. Get the location by voice and record it on the incident through the console (M5), so dispatch starts.
3. If the caller is unreachable, record the attempts; the incident stays flagged.

## Escalation
Medical Director by voice if the caller is unreachable within 5 minutes.

## Verification
The incident reaches `dispatching` (T2 set), or the attempts are recorded.

## References
ADR-0018; `docs/reference/tool1-flow.md`; `internal/intake/sweeper.go`; Build Prompt §10.3; Engineering Guide §4.
