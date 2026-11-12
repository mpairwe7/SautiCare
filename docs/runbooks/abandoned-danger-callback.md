# Runbook: Abandoned session after a danger "yes"

| Field | Value |
|---|---|
| Owner | Coordinator on duty (clinical call-back) with the Medical Director |
| Severity | SEV-1 (a person may be in danger with no location) |
| Alert / trigger | `incident.escalated` with reason `abandoned_after_danger_yes` |
| Last reviewed: | 2026-11-12 |
| Last drilled | not yet drilled |

## Summary
A caller answered yes at screen 2 (heavy bleeding, unconscious or not breathing normally), then the session went silent for `abandon_after` before a location was given. The Guide (§4) calls this the most dangerous partial record. The coordinator calls the caller back at once.

## Triggers and detection
intake-gw's abandonment sweeper (one leader across instances) flags the incident `abandoned` and escalates it. Until the console (M5) and the escalation voice call (M2) exist, the on-call engineer watches for the event.

## Impact
No dispatch has started, because no location was given. Every minute counts.

## Diagnosis
1. Find the incident (read-only role): `SELECT id, reference, t1 FROM op.incidents WHERE escalated AND 'abandoned' = ANY (flags) AND t2 IS NULL ORDER BY t1 DESC;`
2. If the caller redialled within the resume window, the same incident continues and reaches dispatch on its own. Check whether `last_screen` has advanced.

## Mitigation
1. Call back through the audited reveal (M5). Never copy a number into chat or a messaging app (rule 3.6).
2. Get the location by voice and record it on the incident through the console (M5), so dispatch starts.
3. If the caller is unreachable, record the attempts; the incident stays flagged.

## Escalation
Medical Director by voice if the caller is unreachable within 5 minutes.

## Verification
The incident reaches `dispatching` (T2 set), or the attempts are recorded.

## References
ADR-0018; `docs/reference/tool1-flow.md`; Build Prompt §10.3; Engineering Guide §4.
