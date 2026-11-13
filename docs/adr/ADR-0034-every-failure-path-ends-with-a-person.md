---
status: accepted
date: 2026-11-14
decision-makers: Sauti engineering (repository owner)
consulted: Build Prompt §3 rules 4, 5, 11, 14; §10.1–§10.5; §14; the November 2026 UX audit (F-01 to F-14); ADR-0029
informed: PI, Medical Director, Data Protection Officer, coordinators, technical operator
---
# ADR-0034: Every failure path ends with a person

## Context and Problem Statement

[ADR-0029](ADR-0029-interaction-standards-for-voice-ussd-sms.md) rule 3 says that an automated step that errors, times out or runs out of retries hands over to a coordinator and records an escalation. The November 2026 audit ([ux-audit-2026-11.md](../explanation/ux-audit-2026-11.md)) found paths on `main` that end in a hang-up, dead air or a closed record instead (F-01 to F-14):
- The voice line loops for ever on a wrong key, hangs up on silence, and treats a failed recording as heard. A lost end-of-prompt event, a restart or a dropped event stream leaves a caller in silence.
- The fallback dials once and hangs up, records no escalation, and shows the coordinator the caller's own number.
- The bridge ends an unanswered caller's call with a closing message.
- An answered alert call never times out.
- The sweeper escalates only danger yes and red. The watchdog misses a stall after T3. Both stop at the first incident that fails.
- A coordinator call announces only the latest reason, one acknowledgement covers reasons nobody heard, and a merge drops an unacknowledged escalation.
- One coordinator line rings for ever with no second tier and no dead-man signal.
- "Somewhere else" after a danger yes closes the incident.
- No pre-M5 tool lists what is waiting for a coordinator.

How does Sauti make each of these paths reach a person, without a new alerting channel and without changing the protocol unapproved?

## Decision Drivers

* Build Prompt §3 rule 14: fail safe to a human. A closed incident is never rung, so a path that must reach a person never closes the incident in the same step.
* Rule 5: the caller's number is never revealed, including to a coordinator taking a handed-over call.
* Rule 4: every time that is recorded, compared or escalated on comes from the database's clock.
* Rule 11: a protocol change ships behind a gated flag that defaults to today's protocol.
* One path: every fail-safe records `incident.escalated` through the single write path and rides `op.claim_coordinator_calls` (no new channel, no new event type).
* ADR-0028: the bridge never reveals who is registered or who has reported.

## Considered Options

* **Escalate through the existing coordinator call, from each failure point, and harden each surface's timers.**
* Page the coordinator from each process directly (SMS or a second call path): rejected; a second channel that bypasses the claim would double-ring, would not be acknowledged in one place, and would need its own audit.
* Rely on the dialplan's fallback dial alone: rejected; a dial that is busy, unanswered or a placeholder leaves nothing that rings again.

## Decision Outcome

Chosen option: **escalate through the existing coordinator call, and harden each surface.**

- **Every voice hand-over escalates (`voice_handed_over`).** ivr-controller records the escalation before ARI `continue`, once per call, so the coordinator line rings even when the transfer fails. The coordinator's phone shows Sauti's number (`SAUTI_ARI_CALLER_ID`) and "SAUTI" with the incident number.
- **Three no-matches and engaged silence hand over.** The count resets on a valid answer. Silence after any screen past the language screen hands over; silence from the very first prompt still hangs up, as a pocket dial.
- **No lost prompt, call or shutdown leaves silence.** Every prompt arms a backstop of the longest prompt plus the no-input time. A playback Asterisk reports `failed` is a failure: a question hands over, a closing prompt hangs up, an outbound call ends unanswered. After every (re)connect, ivr-controller hands over each call parked in its application with no worker, one second after the stream is up, so a call that arrived during the reconnect is served. On shutdown it hands over every live call before it exits.
- **The fallback dialplan retries, then closes.** It tries the coordinators up to 3 times for 30 s each. It plays a hold message between tries, only when a later try can succeed. Then it plays a closing message and hangs up. Both inbound contexts also match a `+`-prefixed DID.
- **The bridge sends a caller who reaches nobody to a coordinator.** A caller whose responder does not answer, or whose phone is unreachable, goes to the coordinator line, and the incident is escalated (`bridge_unanswered`). Any number with nothing to connect hears "nothing to connect" with a coordinator option on 0; three silent tries also reach a coordinator.
- **The bridge is not an oracle.** A caller whose report is open with nobody assigned yet hears exactly what an unknown number hears: the same prompts, the same key 0, the same timing and hand-over. Their incident is escalated quietly, off the call's path, at most once per incident per 10-minute window of `clock_timestamp()`. Threat T-030 records this mitigation.
- **An answered alert call repeats once, then closes.** 10 s after the menu with no key, the menu is heard again. After the second silence a closing prompt plays and the call is hung up. Nothing is recorded as an answer, so the alert stays pending for the rest of the ladder.
- **The sweeper escalates any located incident without T2.** It first runs the catch-up a redial would run, which records T2 when the landmark is known. An idle incident still without T2 after a ward answer is escalated `dispatch_not_started`. Whether such calls should be dispatched at ward level instead is the PI's choice (R-9); the safe default is a person.
- **The watchdog treats an overdue timer as a stall.** A dispatching incident whose dispatch timer is overdue by the watchdog time and unleased is escalated `dispatch_stalled`, once per overdue wake, after T3 too.
- **A failing incident does not stop the safety nets.** Both loops report and skip a failing incident and return the joined error.
- **One claim announces every unacknowledged reason.** Migration 0033 rebuilds `op.claim_coordinator_calls`. It returns every distinct reason with `seq > acked_seq` and the role of each, plus the severity, and the newest seq, which the call's acknowledgement covers. The call speaks the reasons in the registry's order (`internal/incident/reasons.go`), which is PENDING the PI. A reason recorded after the claim rings again.
- **A merge never silences an unacknowledged escalation.** The survivor is escalated `merged_call_escalated` whenever the absorbed call holds one, whether or not the survivor was escalated. The payload stays `{reason}`: the event's schema admits no other key.
- **Tier 2 and the dead-man gauge.** After `escalation.backup_after_calls` unanswered rings (3, PENDING the PI), the backup line `SAUTI_COORDINATOR_BACKUP_DIAL` rings as well. The first 1 on either acknowledges, and the other call is hung up. intake-gw, which survives an ivr-controller outage, exports `sauti_escalations_unacknowledged` and `sauti_escalation_oldest_unacknowledged_seconds` for the on-call page (tier 3).
- **Out-of-area escalation is a flag, off.** With `out_of_area_escalate_after_danger` on, "Somewhere else" after a danger yes or a red classification flags and escalates the incident (`out_of_area_after_danger`) and leaves it open. The caller still hears 999 and 112. Off, today's protocol closes the incident.
- **`sautictl escalations`** lists, as `op_readonly`, the open incidents with unacknowledged escalations and the location call-back tasks, most severe first. It prints references, never a number.
- **One registry of reasons.** Every escalation reason is a constant in `internal/incident/reasons.go` with a recording in each language and a row in [no-eligible-responders.md](../runbooks/no-eligible-responders.md). A test ties the three together.

This record supersedes in part [ADR-0027](ADR-0027-channels-and-the-alert-ladder.md) (coordinator calls announce every unacknowledged reason and have a second tier; alert calls time out) and [ADR-0028](ADR-0028-masked-bridge-and-surge.md) (an unanswered or unassigned caller reaches a coordinator; the open-report case sounds like an unknown number).

### Consequences

* Good: every failure path on the voice line, the bridge, the alert calls and the safety nets reaches a person, and the coordinator line keeps ringing when a transfer fails.
* Good: a coordinator hears every reason before acknowledging it, and a merge or a later reason cannot silence one.
* Good: an ivr-controller outage is visible from intake-gw, and `sautictl escalations` replaces a pasted SQL query.
* Bad: more calls reach the coordinators (no-matches, engaged silence, unanswered bridge calls, `dispatch_not_started`). Tier 2 needs a second person on call. The rota is the PI's and the Medical Director's (R-1).
* Bad: a caller of the bridge whose report has nobody assigned yet reaches a coordinator only by pressing 0 or after three silent tries; the quiet escalation rings the coordinator line meanwhile. That is the price of not revealing who has reported.
* Bad: with `out_of_area_escalate_after_danger` on, a redial within the resume window resumes the open incident and hears the out-of-area message again, so a mis-pressed "somewhere else" cannot be corrected by redialling. UX-6's confirmation step addresses that.
* Neutral: new PENDING recordings, refused by production until approved: `sauti/fallback-hold`, `sauti/fallback-closing`, `alert-no-input-closing`, `bridge-coordinator-option`, and `escalation-reason-` recordings for `dispatch_not_started`, `voice_handed_over`, `out_of_area_after_danger` and `bridge_unanswered`, in each language.
* For the PI: the reason order, `backup_after_calls`, `dispatch_not_started` versus ward-level dispatch (R-9), and `out_of_area_escalate_after_danger` (an amendment).

### Confirmation

- Registry: `internal/incident/reasons_test.go::TestReasonRankOrdersTriageRuleErrorFirst`, `internal/platform/ivr/catalogue_test.go::TestEveryEscalationReasonHasARecordingAndARunbookRow`.
- Voice line: `internal/platform/ivr/controller_test.go::TestALostEndOfPromptStillRepromptsWithinTheBackstop`, `::TestReconnectHandsOverOrphanedChannels`, `::TestReconnectLeavesOwnedChannelsAlone`, `::TestShutdownHandsOverLiveCalls`, `::TestThreeNoMatchesHandOver`, `::TestNoMatchCountResetsOnAValidAnswer`, `::TestSilenceAfterAnAnswerHandsOver`, `::TestSilenceFromTheFirstPromptHangsUp`, `::TestHandOverRecordsAnEscalationEvenWhenContinueFails`, `::TestHandOverShowsSautiAndTheReference`, `::TestAFailedQuestionPromptHandsOver`; `internal/platform/ivr/asterisk_test.go::TestRunHandsOverACallParkedWhileTheStreamWasDown`; `internal/intake/ivr_test.go::TestHandOverEscalatesOncePerCall`; `internal/platform/ari/client_test.go::TestClientListsChannelsWithTheirApplication`, `::TestClientStreamReportsEachConnect`, `::TestClientSetsCallerName`, `::TestDecodePlaybackFailedState`.
- Dialplan: `internal/platform/ivr/dialplan_test.go::TestFallbackDialplanRetriesThenCloses`, `::TestInboundContextsMatchPlusPrefixedDIDs`, `::TestBridgeInboundFallsBackOnStasisFailure`.
- Bridge: `internal/platform/ivr/bridge_test.go::TestAFailedBridgePromptGoesToACoordinator`, `::TestBridgeCallerNoAnswerGoesToACoordinator`, `::TestBridgeUnknownNumberCanPressZeroForACoordinator`, `::TestBridgeOpenIncidentIsIndistinguishableFromAnUnknownNumber`, `::TestBridgeUnknownNumber`; `internal/platform/pg/opstore/bridge_integration_test.go::TestBridgeOpenIncidentForCallerAndEscalate`.
- Alert calls: `internal/platform/ivr/alerts_test.go::TestAnUnansweredAlertCallRepeatsThenHangsUp`, `::TestAFailedAlertPromptHangsUp`.
- Coordinator calls: `internal/platform/pg/opstore/coordinator_integration_test.go::TestClaimAnnouncesEveryUnacknowledgedReason`, `::TestAReasonAfterTheClaimRingsAgain`, `::TestOldestUnacknowledgedEscalationGauge`, `::TestHasUnacknowledgedEscalation`; `internal/platform/ivr/outbound_test.go::TestCoordinatorCallSpeaksReasonsInPriorityOrder`, `::TestBackupLineRingsAfterThreeUnansweredCalls`; `cmd/intake-gw/main_test.go::TestEscalationGaugesExportTheDeadManSignal`.
- Merge: `internal/intake/dedup_test.go::TestMergedUnacknowledgedEscalationRingsOnTheSurvivor`.
- Safety nets: `internal/intake/sweeper_test.go::TestSweeperEscalatesALocatedYellowCallWithoutT2`, `::TestSweeperRecordsT2ByCatchUpWhenTheLandmarkIsKnown`, `::TestSweeperBeforeTheWardStillOnlyFlags`, `::TestOneFailingIncidentDoesNotStopTheSweep`; `internal/intake/watchdog_test.go::TestWatchdogEscalatesOverdueTimers`, `::TestOneFailingIncidentDoesNotStopTheWatchdog`; `internal/platform/pg/opstore/idle_integration_test.go::TestListIdleCarriesTheWard`, `::TestWatchdogEscalatesAnOverdueTimerAfterT3`.
- Flag: `internal/intake/service_test.go::TestOutOfAreaAfterDangerEscalatesWhenFlagged`, `::TestOutOfAreaFlagOffClosesAsToday`.
- Tool: `cmd/sautictl/escalations_integration_test.go::TestEscalationsListsOnlyUnacknowledged`.
- Runbooks: `governance/tests/test_runbooks.py::test_no_stale_escalation_phrases`.
