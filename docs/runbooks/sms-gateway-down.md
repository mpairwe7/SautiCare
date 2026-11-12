# Runbook: SMS gateway down or failing

| Field | Value |
|---|---|
| Owner | technical operator on call |
| Severity | SEV-2 (voice and the USSD menu still reach responders); SEV-1 if voice is down too |
| Alert / trigger | alert attempts with `channel = 'sms'` settling `unknown` or `failed`; final delivery reports `Failed`/`Rejected`/`Expired` rising; `dispatch-worker` log "simulated delivery" in production (never expected) |
| Last reviewed: | 2026-11-13 |
| Last drilled | not yet drilled |

## Summary
dispatch-worker sends alerts, stand-downs, caller notices, facility SMS and expiry reminders through Africa's Talking ([ADR-0027](../adr/ADR-0027-channels-and-the-alert-ladder.md)). When SMS fails, the ladder still rings responders at 20 s by voice, and responders can always use the USSD menu. Callers and health centres lose their SMS until it is back.

## Triggers and detection
- As `op_readonly`: `SELECT status, count(*) FROM op.alert_attempts WHERE channel = 'sms' AND claimed_at > now() - interval '30 minutes' GROUP BY status;` (also `op.notice_attempts` and `op.forward_attempts`).
- Delivery reports: `SELECT delivery_status, delivery_failure, count(*) FROM op.alert_attempts WHERE delivery_at > now() - interval '30 minutes' GROUP BY 1, 2;`
- intake-gw's delivery-report callback answers 503 when it cannot store a report; the provider then retries.

## Impact
**The dispatch path degrades but does not stop.** An alert attempt that failed is not re-sent. The responder's phone rings at 20 s (voice), and the cascade moves on at 90 s. Callers miss "help is on the way" and the forwarding SMS, so coordinators call them back. Health centres still get the forwarding call.

## Diagnosis
1. Status or failure codes: `401`/HTTP 4xx means credentials (key rotated, application username); `405` means insufficient balance; `406` means the number is blacklisted; `5xx` or timeouts mean the provider is down or unreachable.
2. Egress: dispatch-worker reaches only the host of `SAUTI_AT_SMS_URL`. Check `egress: host not in allowlist` in the logs after a URL change.
3. Is the outage general? Check the provider's status page from the operations network.

## Mitigation
1. Tell the coordinators by voice that SMS is down. Responders are still rung at 20 s; coordinators call callers back with their incident number.
2. Credentials: rotate the API key in OpenBao (P0) or the environment, and restart dispatch-worker. Balance: top up through the account owner.
3. Never re-send attempts by hand or reset their status: an `unknown` attempt may have been delivered. The ladder covers it.
4. Once fixed, new attempts go out on the next pass. Nothing is replayed.

## Escalation
Technical operator lead by voice within 30 minutes. The account owner for balance or credentials. The PI if callers missed SMS for more than an hour (reportable for the study).

## Verification
New SMS attempts settle `sent` and final delivery reports read `Success`.

## References
[ADR-0027](../adr/ADR-0027-channels-and-the-alert-ladder.md), [ADR-0025](../adr/ADR-0025-dispatch-store-and-first-accept-wins.md), [environment.md](../reference/environment.md), Build Prompt §10.4.
