---
status: accepted
date: 2026-11-12
decision-makers: Sauti engineering (repository owner)
consulted: Build Prompt §4, §10.2; Engineering Guide §4; spec §13 (CyCoreSystems/ari risk)
informed: technical operator, Data Protection Officer
---
# ADR-0023: The voice line runs Tool 1 through an in-house ARI client

## Context and Problem Statement

The toll-free line must ask the same Tool 1 questions as USSD, in recorded Luganda or English, answered on the keypad, with nothing recorded (Build Prompt §10.2). A caller who abandons one channel must be able to finish on the other (Guide §4). Asterisk 22 LTS is controlled through ARI: REST calls plus a WebSocket event stream. Go's standard library has no WebSocket client. Which client, and how does the voice channel stay functionally identical to USSD?

## Decision Drivers

* Supply chain: the spec flagged `github.com/CyCoreSystems/ari` v6 as having no valid tag; it is an untagged pseudo-version that pulls in the unmaintained `gogo/protobuf` and the deprecated `golang.org/x/net/websocket`
* The IVR must be unable to record: a client with no recording call is a stronger guarantee than a policy
* One state machine for both channels, so they cannot drift

## Considered Options

* An in-house client for the few calls Sauti needs, over `github.com/coder/websocket` v1.8.15 (ISC, no dependencies)
* `CyCoreSystems/ari` v6 pseudo-version
* AGI/dialplan scripting without ARI

## Decision Outcome

Chosen option: **in-house ARI client** (`internal/platform/ari`).
- The client answers, plays (one playback per prompt), stops a playback (barge-in), hands a call back to the dialplan (`continue`) and hangs up, and receives events over one WebSocket that reconnects with backoff and reports whether it is up (`EventsConnected`, part of `/readyz` as `ari_events`). It has no record, snoop, bridge or external-media method (`TestNoRecordingEndpointsUsed`).
- REST and the handshake go through the egress allowlist, which holds the configured Asterisk host only.
- The controller (`internal/platform/ivr/controller.go`) maps events to `intake.Service.StartIVR` and `AnswerIVR`. These use the **same** `intake.Machine`, write path, triage, location, deduplication and resume as USSD.
- The voice differences are small:
  - option 6 at screen 7 is "another landmark" without free text (ward-level location, `needs_location_callback`);
  - a withheld caller ID is asked for a callback number, typed and ended with `#`.
- Prompts follow Asterisk's playback queue (final review I2). The first key pressed during a prompt stops it; after an answer, keys are ignored until the next prompt's `PlaybackStarted`, so a double press or a key meant for the old menu never answers a question the caller has not heard; the no-input timer starts at the prompt's `PlaybackFinished`, so a long prompt is never replayed while it plays.
- Each call runs on its own worker goroutine, its events in order; the shared lock guards only the map of calls, timers queue an event to their call's worker, and every intake or ARI operation has its own deadline (3 s). One slow call never holds up another (final review I3).
- Fail safe to a human (hard constraint 14; final review I4). If the Stasis application is not there (ivr-controller down, ARI disconnected), the dialplan goes to `sauti-ivr-fallback`: the bilingual recording `sauti/fallback`, then a dial to the on-call coordinators (`${SAUTI_COORDINATOR_DIAL}`, a placeholder until P0 provisions it). The controller hands a call it cannot serve (answer refused, intake error or deadline, a question that cannot be played) to the same context with `continue`; only if that fails is the call hung up. A closing prompt that cannot be played is followed by an immediate hang-up (the incident is complete).
- Prompts are recorded sound IDs derived from the prompt, so `telephony/prompts/catalogue.yaml` lists every recording the line needs and CI checks it against the code.
- `op_ivr` holds exactly the intake write path and reads; it never touches the USSD step store.

## Consequences

* Good: about 250 lines that are auditable and fuzzed; no recording capability in the client; resume across channels works because both channels open sessions the same way; a voice failure ends with a coordinator, not a hang-up.
* Bad: Sauti maintains the client; any new ARI call (alerting in M2, the masked bridge, which bridges two calls without recording) is added deliberately and reviewed. ADR-0027 added outbound calls, and [ADR-0028](ADR-0028-masked-bridge-and-surge.md) added plain mixing bridges; the client still cannot record, snoop or stream media.

## Confirmation

`internal/platform/ari/client_test.go::TestClientParsesStasisStartCallerID`, `::TestClientReconnects`, `::TestNoRecordingEndpointsUsed`; `internal/intake/ivr_test.go::TestIVRResumesUSSDIncident`, `::TestIVRAnotherLandmarkNeedsCallback`, `::TestIVRWithheldAsksCallback`; `internal/platform/ari/playback_test.go::TestClientStopsPlaybackAndSeesItStart`, `::TestClientContinuesToTheDialplan`, `::TestClientReportsEventStream`; `internal/platform/ivr/controller_test.go::TestControllerRunsACall`, `::TestControllerKeyStopsThePromptAndADoublePressAnswersOnce`, `::TestControllerNoInputWaitsForThePromptToFinish`, `::TestControllerSlowCallDoesNotBlockOthers`, `::TestControllerIntakeErrorHandsOverToACoordinator`, `::TestControllerIntakeDeadlineHandsOver`, `::TestControllerFailedPlay`; `internal/platform/ivr/asterisk_test.go::TestControllerOverAsteriskQueue`; `internal/platform/ivr/dialplan_test.go::TestDialplanFallsBackToACoordinator`; `cmd/ivr-controller/main_test.go::TestReadinessIncludesTheARIEventStream`; `internal/platform/ivr/catalogue_test.go::TestPromptCatalogueIsComplete`; `tests/e2e/m1_ivr_test.go::TestScenario5FinishOnIVR`.
