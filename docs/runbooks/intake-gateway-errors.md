# Runbook: Intake gateway errors

| Field | Value |
|---|---|
| Owner | Platform engineering on call, with the study coordinator |
| Severity | SEV-1 if callers cannot report; otherwise SEV-2 |
| Alert / trigger | intake-gw logs `ussd intake failed`; `/readyz` fails; synthetic USSD monitor fails (M7); HTTP 404/403 spikes on the callback route |
| Last reviewed: | 2026-11-12 |
| Last drilled | Not yet drilled (M0, development only) |

## Summary

intake-gw answers Africa's Talking USSD callbacks. On any internal error it still answers the caller with an END screen that gives the free voice line (rule 3.14). A burst of 404s means a wrong path token or probing; 403s mean an unknown service code.

## Triggers and detection

* Log lines `ussd intake failed` (session ID only, never the number or the input).
* `curl -s 127.0.0.1:9100/readyz` reports `operational_db: fail`.
* Synthetic monitors on MTN and Airtel every 5 minutes (M7).

## Impact

* Callers on USSD receive the voice-line message instead of the flow. The IVR line keeps answering.
* Incidents may not be created until the operational store is back.

## Diagnosis

1. `/readyz` on each intake-gw instance, then the operational database and Patroni leader.
2. Logs for `ussd intake failed`: database errors, timeouts (the 800 ms budget), or `reference collisions`.
3. For 404s: confirm the callback URL registered with Africa's Talking uses the current `SAUTI_AT_PATH_TOKEN`. For 403s: compare `SAUTI_USSD_SERVICE_CODES` with the code in use.

## Mitigation

1. Restore the operational store (Patroni failover is automatic).
2. Token mismatch: update the callback URL at Africa's Talking, or roll back the token change. Rotate the token if it may have leaked.
3. If USSD stays down, tell the coordinator to rely on the IVR line and the fallback procedure.

## Escalation

Study coordinator immediately, by voice. Platform lead for SEV-1. Both-sites-down procedure: carrier forwarding to on-call coordinators (Build Prompt §14).

## Verification

A simulated dial returns the expected screen. `/readyz` reports ok. No `ussd intake failed` lines for 15 minutes.

## References

`internal/platform/africastalking/ussd.go`; `docs/reference/ussd-at-adapter.md`; Build Prompt §14 degradation table.
