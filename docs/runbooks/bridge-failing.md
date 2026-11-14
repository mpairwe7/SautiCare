# Runbook: the masked bridge failing

| Field | Value |
|---|---|
| Owner | technical operator on call |
| Severity | SEV-2 (responders and callers cannot talk; dispatch itself is unaffected) |
| Alert / trigger | `ivr-controller` log "bridge call failed" or "bridge call not begun"; `op.bridge_calls` results `failed` or `no_answer` rising; responders or callers reporting the bridge number does not connect |
| Last reviewed: | 2026-11-14 |
| Last drilled | not yet drilled |

## Summary
Responders and callers reach each other through the bridge number without learning each other's number ([ADR-0028](../adr/ADR-0028-masked-bridge-and-surge.md)). The bridge number's calls go to the outbound application (`Stasis(sauti-alerts,bridge)`), served by the outbound leader (advisory lock 7347005). Sauti places the other party's leg showing its own number (`SAUTI_ARI_CALLER_ID`) and joins both legs in a mixing bridge. Nothing is recorded.

Every caller the bridge hands to a coordinator goes through the fallback dialplan, which masks the caller ID before it dials: the coordinator sees Sauti's number and "SAUTI", never the caller's (rule 5). A caller who reaches nobody is not left with a closing message ([ADR-0034](../adr/ADR-0034-every-failure-path-ends-with-a-person.md)): when the responder's phone rings out or is unreachable, the caller goes to the coordinator line and the incident is escalated (`bridge_unanswered`). Any number with nothing to connect hears "nothing to connect" and can press 0 for a coordinator (three silent tries also reach one). A caller whose report is open with nobody assigned yet hears exactly the same, so the bridge never reveals who has reported; their incident is escalated quietly (`bridge_unanswered`, at most once per incident per 10 minutes). A responder whose other party does not answer still hears so.

## Triggers and detection
- As `op_readonly`: `SELECT result, initiated_by, count(*) FROM op.bridge_calls WHERE started_at > now() - interval '1 hour' GROUP BY 1, 2;`
- Startup warnings: "SAUTI_ARI_DIAL is not set" means no bridge; "SAUTI_ARI_CALLER_ID is not set: no masked bridge" means the same.
- `/readyz` on the outbound leader reports `outbound_events` while its event stream is down. Bridge calls then fall to the dialplan fallback, where a coordinator answers.
- A call the bridge cannot serve goes to a coordinator, through the same fallback. That covers a database error while recognising the number, a leg that cannot be placed, a choice prompt Asterisk cannot play (log "bridge prompt not played; to a coordinator"), three wrong or silent choices, or no bridge in the serving process. Coordinators receiving many bridge calls is itself a sign of this runbook's fault.

## Impact
Responders cannot ask callers for directions, and callers cannot reach their responder. Dispatch, alerts and acceptances are unaffected. **Never give a responder the caller's number, or a caller the responder's, by any other means (rule 5).** Coordinators relay messages by calling each party from the console (M5).

## Diagnosis
1. `failed` results: the trunk refused the leg (`SAUTI_ARI_DIAL`, registration, credit), or a sealed number could not be opened (key provider). The log names the bridge call ID, never a number.
2. `no_answer` results: the other party did not answer within the ring time. Normal in moderation. A caller's `no_answer` also records a `bridge_unanswered` escalation ("bridge escalation not recorded" in the log if that write failed).
3. Nobody recognised ("nothing to connect" for everyone): as `op_readonly`, check that the responder is not retired (`op.responders.status`) and that the assignment is open (`op.assignments.released_at IS NULL`, the incident not closed). A registered number that changed must be re-registered. A caller is recognised through their own report, or through the incident it was merged into until a coordinator splits the merge.
4. The bridge number goes straight to a coordinator: the outbound application is unreachable. Follow [voice-alerts-failing.md](voice-alerts-failing.md).
5. Calls handed to a coordinator with the log line `bridge prompt not played; to a coordinator`: Asterisk refused the playback, or reported it `failed` (the recording is missing on the Asterisk host). Check the prompt catalogue's files for the call's language. The toll-free line logs `ivr prompt failed to play` for the same fault.
6. A "bridge not destroyed" warning: a mixing bridge may remain in Asterisk. `bridge show all` on the Asterisk console lists them; a `bridge-<bridge call ID>` bridge with no channels can be removed with `bridge destroy <id>`.

## Mitigation
1. Tell the coordinators the bridge is down; they relay between the parties.
2. Fix the trunk, caller ID or key provider; restart ivr-controller if no leader holds lock 7347005.
3. Never edit `op.bridge_calls`: it is the log.

## Escalation
Technical operator lead within 30 minutes; the telephony provider for trunk faults.

## Verification
A test call from a test responder's phone with an open test assignment reaches the test caller's phone. Both phones show Sauti's number while ringing, once connected, and in their call logs afterwards; a phone showing the other party's number means a connected-line update leaked (set `send_connected_line=no` on the trunk). The call appears in `op.bridge_calls` as `connected`.

**Known limit:** a caller who gave a different call-back number at screen 8 is not recognised when calling the bridge from that number. Only the numbers they called Sauti from are hashed for lookup; adding a lookup hash for call-back numbers needs the Data Protection Officer (rule 12). Their responder can still reach them, and the coordinators can relay.

## References
[ADR-0028](../adr/ADR-0028-masked-bridge-and-surge.md), [ADR-0027](../adr/ADR-0027-channels-and-the-alert-ladder.md), [voice-alerts-failing.md](voice-alerts-failing.md), Build Prompt §10.5, rule 5.
