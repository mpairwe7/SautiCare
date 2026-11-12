# Runbook: Voice line not answering (ivr-controller or ARI down)

| Field | Value |
|---|---|
| Owner | On-call engineer |
| Severity | SEV-1 (a basic-phone intake channel is down) |
| Alert / trigger | synthetic IVR call fails (M7); ivr-controller `/readyz` failing; no `ivr-controller running` after a restart; repeated `ivr answer failed` or `ivr intake failed` log lines |
| Last reviewed: | 2026-11-12 |
| Last drilled | not yet drilled |

## Summary
The toll-free line is answered by Asterisk, which hands each call to the `sauti-ivr` Stasis application that ivr-controller serves over ARI. If the controller is down or cannot reach ARI, calls ring into a Stasis application with nobody serving it.

## Triggers and detection
- The synthetic monitor's IVR call fails (M7).
- `curl -s 127.0.0.1:9101/readyz` is not `ok`.
- Error lines `ivr answer failed`, `ivr play failed` or `ivr intake failed`. These lines carry the channel ID and never a number.

## Impact
Voice intake is down. **USSD is unaffected** and is the fallback for callers. Calls already in progress keep every answer given so far, and a redial within 10 minutes resumes the incident on either channel.

## Diagnosis
1. Process: `systemctl status ivr-controller` (Quadlet unit, P0).
2. Database: the readiness check `operational_db`.
3. ARI: `curl -s -u sauti:<password> http://127.0.0.1:8088/ari/asterisk/info`, run on the Asterisk host. A 401 means the credential is out of sync with OpenBao.
4. Start-up refusals: a triage bundle, gazetteer or screen check failing is the same as for intake-gw (runbooks `triage-rule-error.md` and `gazetteer-update.md`).

## Mitigation
1. Restart ivr-controller. It reconnects to ARI by itself after Asterisk restarts.
2. If Asterisk is down, fail over to the second SIP carrier and IVR at Site B (M7 switchover runbook).
3. If the outage lasts, the carrier forwards the IVR number to the on-call coordinators (Build Prompt §14). Staff log on paper with pre-issued incident IDs, entered later with the `manual_entry` flag.

## Escalation
Coordinator on duty at once, by voice. Technical operator if Asterisk or the SIP trunk is down.

## Verification
A test call from a known number reaches the language prompt, and the incident appears with channel `ivr`.

## References
ADR-0023; `docs/reference/tool1-flow.md`; `telephony/asterisk/`; Build Prompt §10.2, §14.
