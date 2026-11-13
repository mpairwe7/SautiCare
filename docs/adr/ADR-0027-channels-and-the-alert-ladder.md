---
status: accepted
date: 2026-11-13
decision-makers: Sauti engineering (repository owner)
consulted: Build Prompt §3 rules 5, 6, 14; §10.4–10.5; §16 scenarios 7–11; §19 decisions 1, 6, 8; spec O2
informed: technical operator, coordinators, PI, Data Protection Officer
---
# ADR-0027: Responder, caller and facility channels, and the alert ladder

## Context and Problem Statement

Dispatch decides who is alerted (ADR-0024) and records it (ADR-0025). The decision then has to reach people.
- **Responders** have basic phones. They receive alerts, answer them, and switch availability on and off.
- **Callers** are told help is on the way, or which health centre has their request.
- **Health centres** acknowledge forwarded requests.
- **Coordinators** take every escalation.

Decision 8's alert ladder is: USSD push and SMS at once, voice after 20 s, and the USSD menu at any time. Spec O2 records that Africa's Talking documents no network-initiated USSD API. Decisions 1 and 6 (location in SMS, decline reasons) sit behind the amendment gate. Rule 6 forbids patient data on messaging. Rule 5 forbids revealing the caller's number to responders. How are the channels built so that every message is compliant, every attempt is logged per channel, nothing is sent twice, and every escalation reaches a person?

## Decision Drivers

* No duplicate or lost message across crashes (scenario 14): every attempt is claimed before the provider call.
* Hard constraint 6: what a message may say must be fixed and checkable, not left to call sites.
* One answer path: a responder accepting by USSD and by voice at the same instant still yields one assignment.
* Fail safe to a human: an escalation keeps ringing until a coordinator acknowledges it.

## Considered Options

* **Ports in the domain, adapters per channel, attempts in the store:**
  - SMS through Africa's Talking (dispatch-worker);
  - voice through Asterisk (ivr-controller);
  - the responder USSD menu (intake-gw).
* A messaging platform (WhatsApp or similar): refused by rule 6.
* USSD push now: there is no documented API to build against (O2).

## Decision Outcome

Chosen option: **ports, adapters, attempts claimed once**.

**SMS** (`africastalking.SMSClient`, `channels.SMS`):
- The client posts the documented form through the egress allowlist. Accepted (100–102) returns the message ID. A provider refusal is `dispatch.ErrRejected`, settled as failed. Anything else is unknown and never re-sent.
- The worker plans the due incidents one by one, most urgent first (each incident's alerts issued and T3 recorded), and an incident's messages start going out as soon as it is planned, while the next incident plans. All the batch's messages share one pool of at most 16 provider calls at once, taken in incident order, each bounded at 5 s. Each result is recorded on a context of its own, so a pass that ends mid-send never leaves an attempt claimed. A provider that hangs therefore costs one send timeout, not other incidents' alerts, and a plan that is slow or fails (up to its 20 s pass timeout) never holds back an alert already issued (CodeRabbit PR #11). An incident's next wake is stored only after its sends: a store failure while sending leaves it leased, and the next pass retries it.
- Delivery reports arrive at intake-gw on a secret path (`/v1/sms/delivery/at/<token>`). Only codes are kept, and a final report is never overwritten.
- Templates are gated config (`config/gated/sms.yaml`). Each template may carry only its own placeholders, and the value type has no field for a name, a clinical answer or the caller's number.
  - Protocol-mode alerts carry the incident number, colour and category.
  - Ward and landmark appear only in `alert_amended` and `facility_forward`.
  - Each message is GSM-7 and fits one segment.
  - All copy is PENDING, and production refuses it.
- Caller notices go to the caller's number only: the callback given at screen 8, otherwise the number called from. `op.caller_contact` returns it sealed and bound to its row. Responder messages go to the responder's number only.

**Responder menu** (`respond`, intake-gw, `SAUTI_USSD_RESPONDER_CODES`):
- A registered responder is recognised by the keyed hash of their number.
- They can accept their most recent alert, then hear where to go. A late accept hears "already taken".
- They can decline it. Once decision 6's amendment allows, they choose from the role's closed list, whose last option is Other (Build Prompt §10.5, verbatim); the code is stored (scenario 10).
- They can switch availability on for 8 hours or off.
- The menu is stateless over USSD: a retried callback gets the same answer (a decline repeated after it was committed, or after the alert closed, reads the stored outcome from `op.decline_alert` without writing). Every screen, both availability states included, is checked against the screen limit and GSM-7 at start.

**Voice** (ivr-controller, a separate Stasis application `sauti-alerts`):
- **Alert calls:** an alert still pending 20 s after it was sent (`voice_fallback_after`) rings the responder (scenario 9), unless the responder has switched off or a gate has lapsed since (`op.responder_eligible_now`, checked again when the attempt is claimed). Press 1 to accept, 2 to decline (with reasons once allowed), 3 to repeat. Answers go through the same `op.accept_alert` and `op.decline_alert` as the menu.
- **Forwarding calls:** a forwarded health centre is called (press 1 to acknowledge) and also gets its SMS (scenarios 7, 8), each only while the forward is open and the incident is neither closed nor merged (a terminal incident could not record the acknowledgement).
- **Coordinator calls:** every escalation rings the coordinator line (`SAUTI_COORDINATOR_DIAL`) every 2 minutes until a coordinator presses 1. One acknowledgement covers the incident's escalations so far (scenario 11). The line is never rung twice at once (`SAUTI_COORDINATOR_LINES`, default 1), the most severe escalation goes first, and a closed incident is not rung about. A line is held from the ring until the call ends: an answer handled before the call is stored still marks it answered; an answered forwarding or coordinator call with no key for `SAUTI_OUTBOUND_NO_INPUT` (default 2 minutes) is hung up, and the escalation rings again at its interval; and each leadership term, or reconnection of the event stream, first asks Asterisk about every line it holds and frees those whose channel is gone (a call's end event reaches only the process holding the websocket). A line Asterisk cannot be asked about stays held (CodeRabbit PR #11).
- **Results:** a call ends only once its result prompt has been heard. A key the menu being heard offers cuts its prompt; any other key is ignored, so the prompt plays on rather than leaving silence. An accept by voice speaks the incident number, ward and landmark.
- **One leader:** the leader places the calls and alone holds the outbound application's websocket. Asterisk gives an application one websocket, so a placed call and the events of its answer meet in one process.
- All voice uses ARI `originate` with no recording, snooping or bridging, and recorded prompts from the catalogue.

**The ladder:**
- USSD push is not built: its channel name is reserved (`dispatch.ChannelUSSDPush`) and an adapter is added only on written confirmation from MTN, Airtel or Africa's Talking that the network offers it (O2).
- SMS goes at 0 s and voice at 20 s; the USSD menu works at any time.
- Every attempt sits in `op.alert_attempts`, `op.notice_attempts` or `op.forward_attempts`, claimed once per channel, with its result and delivery report.
- The 8-hour availability-expiry reminder is claimed once per period.

**Production:** dispatch-worker and ivr-controller refuse production until the key provider exists (P0). Without Africa's Talking credentials or a dial template, development simulates delivery or does without voice, logging identifiers only.

### Consequences

* Good: hard constraints 5 and 6 hold by construction and are tested. Every message and call is recorded per channel. Scenarios 7–11 run end to end against the real store and a fake Asterisk.
* Good: the coordinator is never left unaware; escalations ring until acknowledged.
* Bad: all wording is PENDING (the PI, plus Luganda translation). Voice needs recordings for every catalogue prompt. Telephony (trunk, caller ID, coordinator line) is provisioned in P0.
* Neutral: an accepted responder dialling the menu sees their active assignment (incident number, ward, landmark), ahead of any alert still pending, which they could not take (`already_assigned`); without an assignment, the newest pending alert of the hour. A menu session is pinned to the alert of its first screen, so a newer alert cannot redirect an answer under way (`op.responder_menu_alert`, migration 0020; CodeRabbit PR #11).

### Confirmation

`internal/platform/africastalking/sms_test.go` (`TestSMSSendsTheDocumentedRequest`, `TestSMSStatusMapping`, `TestSMSTimeoutIsUnknown`, `TestSMSErrorsCarryNoNumberOrKey`); `internal/platform/africastalking/delivery_test.go::TestDeliveryReportsAreRecordedWithoutTheNumber`; `internal/config/sms_test.go::TestSMSTemplatesAreCompliant`; `internal/config/responder_test.go::TestResponderMenuAndDeclineReasons`; `internal/platform/channels/sms_test.go` (`TestAlertSMS`, `TestCallerNotices`, `TestFacilitySMS`, `TestAvailabilityReminderSMS`); `internal/respond/ussd_test.go` (`TestAcceptFromTheMenu`, `TestDeclineWithAndWithoutReasons`, `TestAvailabilityFromTheMenu`); `internal/platform/ivr/alerts_test.go` (`TestAlertCallsDialDueAlertsOnce`, `TestAlertCallAccept`, `TestAlertCallDecline`); `internal/platform/ivr/outbound_test.go` (`TestForwardCalls`, `TestCoordinatorCallsRingAndAcknowledge`, `TestOutboundRoutesByKind`); `internal/platform/pg/opstore/sms_integration_test.go`, `respond_integration_test.go`, `voice_integration_test.go`, `forward_integration_test.go`; `internal/platform/wiring/voice_integration_test.go::TestScenario9VoiceAt20Seconds`; `internal/platform/wiring/respond_integration_test.go::TestScenario10DeclineThroughTheMenu`, `::TestConcurrentAcceptsThroughTheMenu`; `internal/platform/wiring/forward_integration_test.go::TestScenario7ForwardAcknowledged`, `::TestScenario8NextCentre`, `::TestScenario11RuleErrorRingsTheCoordinator`.
