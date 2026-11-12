# Runbook: Split a wrongly merged call

| Field | Value |
|---|---|
| Owner | Coordinator on duty (decision) with the on-call engineer (until the console, M5) |
| Severity | SEV-2 (a separate emergency may be waiting without its own dispatch) |
| Alert / trigger | a coordinator or responder reports that a merged call is a different emergency |
| Last reviewed: | 2026-11-12 |
| Last drilled | not yet drilled |

## Summary
Calls with the same category, within one zone and 15 minutes, are merged at T2 into the incident already dispatching (ADR-0022); calls located only to their ward are never merged. If two emergencies were merged, the coordinator splits the call off: it returns to dispatching with its own record, and the survivor's T1 is recomputed.

## Triggers and detection
A call-back or a responder on scene reveals a second emergency. Merged calls are listed by their survivor's `absorbed` set.

## Impact
Until the split, the second emergency has no dispatch of its own. The dispatch path is otherwise unaffected.

## Diagnosis
1. Find the survivor and its merged calls (role `op_readonly`): `SELECT id, reference, t1, absorbed FROM op.incidents WHERE absorbed <> '{}' ORDER BY t2 DESC;`
2. Confirm the member: `SELECT id, reference, merged_into, category FROM op.incidents WHERE merged_into = '<survivor id>';`

## Mitigation
1. Split, as the console role, with the coordinator's identity-provider subject:
   `sautictl incident split --dsn <op_console DSN> --survivor <survivor id> --member <member id> --staff <coordinator subject UUID>`
2. The member returns to `dispatching` and is picked up for its own dispatch (M2). The survivor's T1 is recomputed from the calls that remain. If the member had raised the survivor's severity or escalated it, that stays: severity never falls. Reassess the survivor by voice.
3. Never edit `op.incidents` by hand. The split is two events, logged in the audit ledger. Only the console role can record them: the database refuses a split, an unmerge or any staff action from intake, IVR or the read-only role.

## Escalation
Medical Director by voice if the separate emergency is red.

## Verification
`sautictl verify-projections` reports no drift; the member shows `state = dispatching` and `merged_into` is empty.

## References
ADR-0022; `docs/reference/events.md`; Build Prompt §10.3.
