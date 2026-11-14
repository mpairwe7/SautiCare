# Runbook: SMS gateway down or failing

| Field | Value |
|---|---|
| Owner | technical operator on call |
| Severity | SEV-2 (voice and the USSD menu still reach responders); SEV-1 if voice is down too |
| Alert / trigger | alert attempts with `channel = 'sms'` settling `unknown` or `failed`; final delivery reports `Failed`/`Rejected`/`Expired` rising; `dispatch-worker` log "simulated delivery" in production (never expected) |
| Last reviewed: | 2026-11-14 |
| Last drilled | not yet drilled |

## Summary
dispatch-worker sends alerts, stand-downs, caller notices, facility SMS and expiry reminders through Africa's Talking ([ADR-0027](../adr/ADR-0027-channels-and-the-alert-ladder.md)). When SMS fails, the ladder still rings responders at 20 s by voice, and responders can always use the USSD menu. Callers and health centres lose their SMS until it is back.

## Triggers and detection
- As `op_readonly`: `SELECT status, count(*) FROM op.alert_attempts WHERE channel = 'sms' AND claimed_at > now() - interval '30 minutes' GROUP BY status;` (also `op.notice_attempts` and `op.forward_attempts`).
- Delivery reports: `SELECT delivery_status, delivery_failure, count(*) FROM op.alert_attempts WHERE delivery_at > now() - interval '30 minutes' GROUP BY 1, 2;` (also `op.notice_attempts` and `op.forward_attempts`).
- Callers whose SMS did not go out, for the coordinators' call-backs (as `op_readonly`; the console shows the caller's number to a coordinator):

  ```sql
  SELECT i.reference, n.kind, n.status, n.delivery_status, n.claimed_at
    FROM op.notice_attempts n JOIN op.incidents i ON i.id = n.incident_id
   WHERE n.claimed_at > now() - interval '2 hours'
     AND (n.status IN ('failed', 'unknown') OR n.delivery_status IN ('Failed', 'Rejected', 'Expired'))
   ORDER BY n.claimed_at;
  ```
- intake-gw's delivery-report callback answers 503 when it cannot store a report; the provider then retries.

## Impact
**The dispatch path degrades but does not stop.** Alerts are still issued on time: planning never waits for a send, and an incident's messages go out as soon as it is planned, whatever the incidents after it are doing. A provider that hangs costs each message at most 5 s, with 16 sent at once. An alert attempt that failed is not re-sent. The responder's phone rings at 20 s, but **only if voice alerts are on** (`SAUTI_ARI_DIAL` set, no "SAUTI_ARI_DIAL is not set" warning at ivr-controller start, and `outbound_events` ready on the outbound leader). Otherwise the responder has only the USSD menu, and the coordinators must phone them. Either way the cascade moves on at 90 s. Callers miss "help is on the way" and the forwarding SMS, so coordinators call them back. Health centres still get the forwarding call.

## Diagnosis
1. Read the provider's answer. Africa's Talking answers on two levels, and the same number means different things on each.
   - **Where to see it:** the attempt tables record only `sent`, `failed` or `unknown`. The provider's answer appears in dispatch-worker's log as `HTTP <code>` or `status <code> <name>` on an "availability reminder failed" line (reminders use the same client); delivery reports carry `delivery_failure` (query above).
   - **HTTP status of the request** (`HTTP <code>`):

     | HTTP status | Meaning | Sauti settles | Action |
     |---|---|---|---|
     | 2xx | the request was taken; read the per-recipient code below | — | — |
     | 401, other 4xx | the request was refused, usually the credentials ("The supplied authentication is invalid": API key or application username) | `failed` | Mitigation step 2 (credentials) |
     | 5xx, timeout, no answer | the provider is down or unreachable | `unknown` | wait; step 3 below |

   - **Per-recipient `statusCode`** in a 2xx answer (`status <code> <name>`), from Africa's Talking's "Messaging error codes" help article:

     | statusCode | Name | Sauti settles | Action |
     |---|---|---|---|
     | 100, 101, 102 | Processed, Sent, Queued | `sent` | none: the delivery report follows |
     | 401 | RiskHold | `failed` | Africa's Talking holds the message for risk review: contact their support. **Not** a credential problem |
     | 402 | InvalidSenderId | `failed` | `SAUTI_AT_SENDER_ID` is not approved for this account: correct it, or unset it until it is registered |
     | 403 | InvalidPhoneNumber | `failed` | the stored number is not a valid mobile number: the coordinators check the responder's registration or call the caller back through the console (M5; numbers are seen only there) |
     | 404 | UnsupportedNumberType | `failed` | the number's type cannot receive this SMS: contact support |
     | 405 | InsufficientBalance | `failed` | top up (Mitigation step 2) |
     | 406 | UserInBlacklist | `failed` | the recipient blocked the sender: coordinators reach them by phone |
     | 407 | CouldNotRoute | `failed` | the provider cannot reach that network: contact support |
     | 409 | DoNotDisturbRejection | `failed` | the recipient is on a do-not-disturb list. Not on the help article's list: name unverified |
     | 500, 501 | InternalServerError, GatewayError | `unknown` | provider fault: wait, contact support if it lasts |
     | 502 | RejectedByGateway | `failed` | the sender ID or short code is not mapped to the account: correct `SAUTI_AT_SENDER_ID` or contact support |

     Sauti also settles an undocumented 408 as `failed`, and any other code as `unknown`.
2. Egress: dispatch-worker reaches only the host of `SAUTI_AT_SMS_URL`. Check `egress: host not in allowlist` in the logs after a URL change.
3. Is the outage general? Check the provider's status page from the operations network.

## Mitigation
1. Tell the coordinators by voice that SMS is down. If voice alerts are on (see Impact), responders are still rung at 20 s; otherwise coordinators phone the alerted responders. Coordinators call callers back from the list above, using their incident number.
2. Credentials (HTTP 401 or another 4xx only): rotate the API key in OpenBao (P0) or the environment, and restart dispatch-worker. Balance (per-recipient 405): top up through the account owner. Sender ID (402, 502): correct or unset `SAUTI_AT_SENDER_ID` and restart. Risk hold (401) and routing (404, 407): Africa's Talking support, through the account owner.
3. Never re-send attempts by hand or reset their status: an `unknown` attempt may have been delivered. The ladder covers it.
4. Once fixed, new attempts go out on the next pass. Nothing is replayed.

## Escalation
Technical operator lead by voice within 30 minutes. The account owner for balance, credentials, sender ID, or a risk hold to raise with Africa's Talking support. The PI if callers missed SMS for more than an hour (reportable for the study).

## Verification
New SMS attempts settle `sent` and final delivery reports read `Success`.

## References
[ADR-0027](../adr/ADR-0027-channels-and-the-alert-ladder.md), [ADR-0025](../adr/ADR-0025-dispatch-store-and-first-accept-wins.md), [environment.md](../reference/environment.md), Build Prompt §10.4.
