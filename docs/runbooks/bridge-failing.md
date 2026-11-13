# Runbook: the masked bridge failing

| Field | Value |
|---|---|
| Owner | technical operator on call |
| Severity | SEV-2 (responders and callers cannot talk; dispatch itself is unaffected) |
| Alert / trigger | `ivr-controller` log "bridge call failed" or "bridge call not begun"; `op.bridge_calls` results `failed` or `no_answer` rising; responders or callers reporting the bridge number does not connect |
| Last reviewed: | 2026-11-13 |
| Last drilled | not yet drilled |

## Summary
Responders and callers reach each other through the bridge number without learning each other's number ([ADR-0028](../adr/ADR-0028-masked-bridge-and-surge.md)). The bridge number's calls go to the outbound application (`Stasis(sauti-alerts,bridge)`), served by the outbound leader (advisory lock 7347005). Sauti places the other party's leg showing its own number (`SAUTI_ARI_CALLER_ID`) and joins both legs in a mixing bridge. Nothing is recorded.

## Triggers and detection
- As `op_readonly`: `SELECT result, initiated_by, count(*) FROM op.bridge_calls WHERE started_at > now() - interval '1 hour' GROUP BY 1, 2;`
- Startup warnings: "SAUTI_ARI_DIAL is not set" means no bridge; "SAUTI_ARI_CALLER_ID is not set: no masked bridge" means the same.
- `/readyz` on the outbound leader reports `outbound_events` while its event stream is down. Bridge calls then fall to the dialplan fallback, where a coordinator answers.

## Impact
Responders cannot ask callers for directions, and callers cannot reach their responder. Dispatch, alerts and acceptances are unaffected. **Never give a responder the caller's number, or a caller the responder's, by any other means (rule 5).** Coordinators relay messages by calling each party from the console (M5).

## Diagnosis
1. `failed` results: the trunk refused the leg (`SAUTI_ARI_DIAL`, registration, credit), or a sealed number could not be opened (key provider). The log names the bridge call ID, never a number.
2. `no_answer` results: the other party did not answer within the ring time. Normal in moderation.
3. Nobody recognised ("nothing to connect" for everyone): as `op_readonly`, check that the responder is not retired (`op.responders.status`) and that the assignment is open (`op.assignments.released_at IS NULL`, the incident not closed). A registered number that changed must be re-registered.
4. The bridge number goes straight to a coordinator: the outbound application is unreachable. Follow [voice-alerts-failing.md](voice-alerts-failing.md).

## Mitigation
1. Tell the coordinators the bridge is down; they relay between the parties.
2. Fix the trunk, caller ID or key provider; restart ivr-controller if no leader holds lock 7347005.
3. Never edit `op.bridge_calls`: it is the log.

## Escalation
Technical operator lead within 30 minutes; the telephony provider for trunk faults.

## Verification
A test call from a test responder's phone with an open test assignment reaches the test caller's phone, which shows Sauti's number. The call appears in `op.bridge_calls` as `connected`.

## References
[ADR-0028](../adr/ADR-0028-masked-bridge-and-surge.md), [ADR-0027](../adr/ADR-0027-channels-and-the-alert-ladder.md), [voice-alerts-failing.md](voice-alerts-failing.md), Build Prompt §10.5, rule 5.
