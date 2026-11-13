---
status: accepted
date: 2026-11-13
decision-makers: Sauti engineering (repository owner)
consulted: Build Prompt §3 rules 5, 8, 12, 14; §10.4 (surge, after acceptance); §10.5 (masked contact); §16 scenarios 1, 2, 14
informed: technical operator, coordinators, PI, Data Protection Officer
---
# ADR-0028: The masked bridge, surge, and what follows acceptance

## Context and Problem Statement

Once a responder accepts, they and the caller often need to talk: directions, the patient's state, the caller's call-back. Rule 5 forbids revealing the caller's number to a responder, and the responder's number must not reach the caller either. Build Prompt §10.5 asks for a masked contact:
- The responder calls a toll-free bridge number.
- Sauti recognises the responder and their active assignment, and asks for the last 4 digits of the incident number when they hold several.
- Sauti bridges them to the caller, showing its own number.
- The caller can call back the same way.
- Each call is logged with its time, duration and result, and nothing is recorded.

Two more needs come with acceptance and load:
- **After acceptance:** the alert channel repeats the landmark with the bridge instructions, and the caller is told help is on the way.
- **Surge:** with more emergencies than responders, incidents queue by severity and nothing is dropped.

ADR-0023 kept the ARI client minimal and deferred the bridge to this decision.

## Decision Drivers

* Rule 5, in both directions: a number is opened only to dial it. It never appears in the other party's caller ID, a log line, a payload or the bridge log.
* Rule 8: the bridge log carries identifiers only.
* Rule 14: if the bridge cannot be reached, a coordinator answers; a call nobody answers ends and is logged.
* One process per placed call and its answer (Asterisk gives an application one websocket; ADR-0027).

## Considered Options

* **The bridge in ivr-controller, on the outbound application, through a mixing bridge.** Recognition by keyed hash, legs placed showing Sauti's number.
* A provider number-masking service: refused. The caller's number would leave Sauti for a third party (rules 1 and 5), and the hosting policy has no such provider.
* Sending the responder the caller's number by SMS: refused by rule 5.

## Decision Outcome

Chosen option: **the masked bridge in ivr-controller**.

**The masked bridge** (`ivr.Bridge`, migration 0025):
- **Routing.** The bridge number's dialplan context sends calls to the outbound application (`Stasis(sauti-alerts,bridge)`), served by the outbound leader. If the application is unreachable, a coordinator answers.
- **A responder calling.** They are recognised by their number's keyed hash, then by their open assignments. With one assignment they are connected at once. With several they key the last 4 digits of the incident number (three tries).
- **A caller calling.** They are recognised by the hash of a number any of their open incident's sessions came from. With both roles assigned they choose 1 (health worker) or 2 (transport).
- **An unknown number**, or one with nothing open, hears the same "nothing to connect" message. The bridge never reveals who is registered.
- **The legs.** Sauti places the other party's leg showing its own number (`SAUTI_ARI_CALLER_ID`) and joins both legs in a mixing bridge. When either party hangs up, the other is hung up and the bridge destroyed.
- **The log.** `op.bridge_calls` records the incident, who began the call (`vht`, `transporter`, `caller`), when it started, was answered and ended, and the result (`connected`, `no_answer` after the ring time, `failed`).
- **What the client can do.** The ARI client gains plain mixing bridges (create, add a channel, destroy). It still has no way to record, snoop or stream media out.

**After acceptance:**
- An accept by voice speaks the incident number, ward and landmark, then the bridge instructions.
- The menu's accepted and assigned screens give the location and name the bridge line.
- The caller's "helper on the way" SMS goes out once, at the first acceptance (Part B).

**Surge** (migration 0026):
- The worker claims due incidents most severe first, then by the oldest T2, then the earliest wake. An incident with a role still open wakes every ring interval until someone accepts, so nothing is dropped.
- `op.dispatch_surge` compares the calls waiting for a first acceptance with the responders free now (eligible, holding no open assignment). `sautictl dispatch status` prints it, and the console (M5) will show it.

### Consequences

* Good: a responder and a caller can talk without either learning the other's number. Every bridge call is logged. Scenarios 1 and 2 run through the channels and the store, and scenario 1 ends with the bridge.
* Good: in a surge the longest-waiting call of each severity is served first, and the surge is visible.
* Bad: the bridge number, the caller ID shown and the trunk are provisioned in P0. The bridge prompts are PENDING recordings.
* Bad: a responder who accepted and then went silent is not followed up automatically until M3. Build Prompt §10.4's "the caller is asked at set intervals whether help has arrived" needs field status (§10.6) to define silence, and the verification record (§10.7) to keep the answer, both in M3 (rule 12). Until then, the console list and the caller's call-back through the bridge cover it.
* Neutral: ADR-0023's client gains three bridge calls. Its rule that nothing is recorded stands, and `TestNoRecordingEndpointsUsed` enforces it.

### Confirmation

- `internal/platform/pg/opstore/bridge_integration_test.go::TestBridgeTargetsAndLog`
- `internal/platform/ari/client_test.go::TestClientMixingBridge`, `::TestNoRecordingEndpointsUsed`
- `internal/platform/ivr/bridge_test.go`:
  - `TestBridgeResponderToCaller`
  - `TestBridgeSeveralAssignmentsAskTheLastFourDigits`
  - `TestBridgeCallerCallsBack`
  - `TestBridgeUnknownNumber`
  - `TestBridgeNoAnswerAndFailure`
  - `TestOutboundRoutesBridgeEventsOnce`
- `internal/platform/wiring/scenarios_integration_test.go::TestScenario1RedTraumaThroughTheChannels` (a mutant that logs the dialled number fails it), `::TestScenario2YellowObstetricThroughTheChannels`
- `internal/platform/pg/opstore/surge_integration_test.go::TestSurgeQueuesBySeverityThenT2`
- `cmd/sautictl/dispatch_integration_test.go::TestDispatchSuspendStatusResume`
- `internal/platform/ivr/alerts_test.go::TestAlertCallAccept`
- `internal/respond/ussd_test.go::TestMenuShowsTheActiveAssignment`
