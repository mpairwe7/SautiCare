# Runbook: Voice line not served (ivr-controller or ARI down)

| Field | Value |
|---|---|
| Owner | On-call engineer |
| Severity | SEV-1 (a basic-phone intake channel is down; callers reach a coordinator instead) |
| Alert / trigger | synthetic IVR call fails (M7); ivr-controller `/readyz` failing (`operational_db` or `ari_events`); no `ivr-controller running` after a restart; `ivr call handed to the coordinator fallback`, `ivr answer failed`, `ivr intake failed`, `ivr play failed` or `ivr prompt failed to play` log lines; `voice_handed_over` escalations; `sauti_escalation_oldest_unacknowledged_seconds` rising (intake-gw) |
| Last reviewed: | 2026-11-14 |
| Last drilled | not yet drilled |

## Summary
The toll-free line is answered by Asterisk, which hands each call to the `sauti-ivr` Stasis application that ivr-controller serves over ARI. A call is never simply hung up because Sauti cannot serve it (hard constraint 14):
- If the Stasis application is not registered (ivr-controller down, ARI event stream disconnected), `Stasis()` fails at once and the dialplan sends the call to the `sauti-ivr-fallback` context (`telephony/asterisk/extensions.conf`): the bilingual recording `sauti/fallback`, then a dial to the on-call coordinators (`${SAUTI_COORDINATOR_DIAL}`, provisioned in P0).
- If ivr-controller is up but cannot serve a call, it hands that call to the same context with ARI `continue`: Asterisk refuses to answer it, intake fails or does not answer within its 3 s deadline, a question cannot be played or its playback fails, the caller presses three wrong keys on one question, or the caller goes silent after answering a screen ([ADR-0034](../adr/ADR-0034-every-failure-path-ends-with-a-person.md)). A call silent from the very first prompt is still hung up, as a pocket dial. Other calls are unaffected: each call runs on its own worker with its own deadlines.
- Every hand-over of a call that belongs to an incident first records an escalation (`voice_handed_over`), so the coordinator line keeps ringing about the incident even if the transfer fails. A refused escalation is retried every 5 s for up to 10 minutes (log `ivr hand-over escalation failed; retrying`, then `ivr hand-over escalation lost`); the caller is put through at once.
- The fallback masks the caller ID before it answers or dials, on every path into it (a hand-over, the bridge, a Stasis failure): the coordinator's phone shows Sauti's number (`SAUTI_CALLER_ID` in the dialplan's globals) and "SAUTI" with the incident number (`SAUTI_REF`, set by ivr-controller), never the caller's number. With `SAUTI_CALLER_ID` empty (before P0) it shows no number.
- The fallback tries the coordinators up to 3 times, 30 s each, with a hold message between tries, then plays a closing message and hangs up. The closing message promises no call-back (R-3; its wording is PENDING the PI).
- When ivr-controller stops, it hands every live call over before it exits; when it restarts or its event stream reconnects, it hands over every call left in the application with no worker, escalating it under its call ID.
- If even the hand-over fails, the call is hung up; the incident and every answer given so far are persisted, the escalation still rings, and a redial within 10 minutes resumes it on either channel.

## Triggers and detection
- The synthetic monitor's IVR call fails, or reaches the coordinator line instead of the language prompt (M7).
- `curl -s 127.0.0.1:9101/readyz` is not `ok`. `ari_events` fails while the WebSocket to Asterisk is down; `operational_db` fails while the database is unreachable.
- Log lines `ivr call handed to the coordinator fallback` (one per call handed over), `ivr hand-over to the fallback failed; hanging up`, `ivr answer failed`, `ivr intake failed`, `ivr play failed`, `ivr prompt failed to play`, `ivr call found without a worker; handing over`, `ivr hand-over reference not set`, `ivr hand-over escalation failed; retrying`, `ivr hand-over escalation lost`. These lines carry the channel ID and never a number.
- Coordinators report calls arriving on the fallback line.

## Impact
Voice intake falls back to people: callers hear the fallback message and are put through to an on-call coordinator, who takes the details by voice. **USSD intake is unaffected** and remains the primary channel. Calls already in progress keep every answer given so far, and a redial within 10 minutes resumes the incident on either channel (a redial also records any classification or T2 a failed request missed).

Also stopped: coordinator escalation calls, responder voice alerts (SMS still goes), forwarding calls and the masked bridge. ivr-controller places all of them. **The dispatch path continues** (dispatch-worker issues alerts by SMS), but nobody is rung about an escalation: escalations are still recorded, and intake-gw still exports the age of the oldest unacknowledged one. A call that reaches the fallback while ivr-controller is down is not recorded and not escalated; only the live dial reaches a coordinator, and the caller's open incident stays covered by intake-gw's sweeper, watchdog and dead-man gauge.

## Diagnosis
1. Process: `systemctl status ivr-controller` (Quadlet unit, P0).
2. Readiness: `curl -s 127.0.0.1:9101/readyz` names the failing check (`operational_db` or `ari_events`).
3. ARI: `curl -s -u sauti:<password> http://127.0.0.1:8088/ari/asterisk/info`, run on the Asterisk host. A 401 means the credential is out of sync with OpenBao.
4. Dialplan: `asterisk -rx "dialplan show sauti-ivr-fallback"` shows the fallback; `asterisk -rx "dialplan show globals"` shows `SAUTI_COORDINATOR_DIAL` (it must not be the placeholder in production).
5. Start-up refusals: a triage bundle, gazetteer or screen check failing is the same as for intake-gw (runbooks `triage-rule-error.md` and `gazetteer-update.md`).

## Mitigation
1. Tell the coordinator lead by voice that escalation calls are not ringing. Until ivr-controller is back, run `sautictl escalations` (as `op_readonly`, on Sauti's servers) every minute and act on each line as [no-eligible-responders.md](no-eligible-responders.md) says.
2. Restart ivr-controller. It reconnects to ARI by itself after Asterisk restarts; `/readyz` turns `ok` when the event stream is back.
3. If intake keeps timing out, follow the database runbooks; until then every voice call is handed to the coordinators.
4. If Asterisk itself is down, fail over to the second SIP carrier and IVR at Site B (M7 switchover runbook).
5. If the outage lasts, the carrier forwards the IVR number to the on-call coordinators (Build Prompt §14). The carrier must present Sauti's number, not the caller's, on the forwarded call (rule 5): confirm it with the carrier in P0 before relying on this step. Staff log on paper with pre-issued incident IDs, entered later with the `manual_entry` flag.

## Escalation
Coordinator on duty at once, by voice: they are taking the voice calls. Technical operator if Asterisk or the SIP trunk is down.

## Verification
A test call from a known number reaches the language prompt, the incident appears with channel `ivr`, and `/readyz` is `ok` with `ari_events` `ok`. With ivr-controller stopped, a test call hears the fallback message and rings the coordinator line.

## References
ADR-0023; `docs/reference/tool1-flow.md`; `telephony/asterisk/extensions.conf`; `internal/platform/ivr/controller.go`; Build Prompt §10.2, §14.
