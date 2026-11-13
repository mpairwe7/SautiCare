# Runbook: voice alerts, forwarding calls or coordinator calls failing

| Field | Value |
|---|---|
| Owner | technical operator on call |
| Severity | SEV-1 when coordinator calls fail (escalations do not reach a person by voice); SEV-2 otherwise |
| Alert / trigger | `ivr-controller` log "voice alert not placed", "forwarding call not placed", "coordinator call not placed" or "outbound calls failed"; attempts with `channel = 'voice'` settling `unknown`/`failed`; `op.coordinator_calls.calls` growing without acknowledgement |
| Last reviewed: | 2026-11-14 |
| Last drilled | not yet drilled |

## Summary
ivr-controller places three kinds of outbound call on the `sauti-alerts` Stasis application ([ADR-0027](../adr/ADR-0027-channels-and-the-alert-ladder.md)):
- **Responder alerts:** an alert still pending at 20 s. An answered alert call with no key for 10 s after the options hears the alert once more, then a short closing prompt (PENDING copy), and is hung up ("alert call closed after no input"); nothing is recorded as an answer, and the alert stays pending for the rest of the ladder ([ADR-0034](../adr/ADR-0034-every-failure-path-ends-with-a-person.md)).
- **Forwarding calls:** to a health centre.
- **Coordinator calls:** every escalation, retried every 2 minutes until a coordinator presses 1. One call announces every reason not yet acknowledged, most urgent first. The line is never rung twice at once (`SAUTI_COORDINATOR_LINES`, default 1); the most severe escalation goes first, and a closed incident is not rung about. After `escalation.backup_after_calls` unanswered rings (3), the backup line (`SAUTI_COORDINATOR_BACKUP_DIAL`) rings as well; the first 1 on either acknowledges and the other call is hung up. Without a backup line, ivr-controller warns at start-up.
- A prompt Asterisk reports as `failed` (a missing recording) ends the outbound call as unanswered: nobody heard what to do.
- An answered forwarding or coordinator call with no key pressed for `SAUTI_OUTBOUND_NO_INPUT` (default 2 minutes) is hung up ("coordinator call hung up after no input", "forwarding call hung up after no input"), so an unattended answer cannot hold the line; the escalation rings again at its interval.

One leader (advisory lock 7347005) places the calls and alone holds the `sauti-alerts` application's websocket. Asterisk gives an application one websocket, and a newer connection takes it over. A failover therefore moves both; a call answered on the old leader during the switch loses its keys and is rung again by its next rung.

## Triggers and detection
- As `op_readonly`: `SELECT channel, status, count(*) FROM op.alert_attempts WHERE channel = 'voice' AND claimed_at > now() - interval '30 minutes' GROUP BY 1, 2;` (also `op.forward_attempts`).
- Unacknowledged escalations: `SELECT incident_id, calls, last_call_at FROM op.coordinator_calls WHERE calls > 3 ORDER BY last_call_at;`.
- Startup warnings "SAUTI_ARI_DIAL is not set" (no voice alerts or forwarding calls) or "SAUTI_COORDINATOR_DIAL is not set" (no coordinator calls). Each works without the other.
- No coordinator call for a long time while escalations wait, with `SAUTI_COORDINATOR_LINES=1`: a line held by a call that ended unheard. Each new leadership term and each reconnection of the event stream asks Asterisk about every line held and frees those whose channel is gone; the log "coordinator line not checked against Asterisk; kept" means ARI could not answer, and the line stays held until it can.
- `/readyz` on the leader reports `outbound_events` failing while its `sauti-alerts` event stream is down. No outbound call is placed until it reconnects: an answer Sauti could not hear would waste a one-shot attempt. Every store and Asterisk operation of the call workers has its own deadline (3 s; 10 s to place a call), so a hung database costs one deadline, not the leadership term.
- The voice line's own runbook covers Asterisk or ARI being down: [ivr-controller-down.md](ivr-controller-down.md).

## Impact
- Without alert calls, responders still get the SMS at 0 s and can use the USSD menu; the cascade moves on at 90 s.
- Without forwarding calls, health centres still get the SMS. Coordinators call the centre and record its acknowledgement on the console (M5).
- **Without coordinator calls, escalations are only on the coordinator's list:** someone must watch it continuously until calls are restored.

## Diagnosis
1. Does a leader hold lock 7347005? As `op_readonly`: `SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND objid = 7347005;`
2. Can ARI originate? Check the Asterisk logs for the trunk in `SAUTI_ARI_DIAL` (registration, credit, the operator rejecting the caller ID).
3. Is the coordinator line reachable? Dial the `SAUTI_COORDINATOR_DIAL` endpoint from the Asterisk CLI with a test call.
4. Do the prompts play? A missing recording plays silence. `telephony/prompts/catalogue.yaml` lists every sound the calls need.

## Mitigation
1. **Coordinator calls failing:** tell the coordinator lead by voice. Assign someone to watch the escalation list until calls are restored.
2. Restart ivr-controller if no leader holds the lock, or after a trunk or ARI fix. A claimed attempt is never re-placed (no duplicate call); the next rung covers it. Coordinator calls retry on their own every 2 minutes. A restart also frees every coordinator line the old process held; check first that the coordinator line is not in a call (the Asterisk CLI `core show channels`), or a second call may ring it.
3. Never reset attempts or `op.coordinator_calls` by hand.

## Escalation
Technical operator lead by voice within 15 minutes for coordinator calls, 30 minutes otherwise. The telephony provider for trunk faults.

## Verification
- A test alert (development or test incident) rings at 20 s.
- A test escalation rings the coordinator line, and pressing 1 stops the retries (`op.coordinator_calls.calls` back to 0).

## References
[ADR-0027](../adr/ADR-0027-channels-and-the-alert-ladder.md), [ADR-0023](../adr/ADR-0023-ivr-through-in-house-ari-client.md), [ivr-controller-down.md](ivr-controller-down.md), [no-eligible-responders.md](no-eligible-responders.md), Build Prompt §10.4, rule 3.14.
