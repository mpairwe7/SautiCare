# UX and readiness audit, November 2026

**Type:** Explanation (Diátaxis) · **Baseline:** `main` at 1123d40 (M0–M2 merged) · **Decisions:** [ADR-0029](../adr/ADR-0029-interaction-standards-for-voice-ussd-sms.md) · **Fix plan:** `docs/superpowers/plans/2026-11-13-ux-hardening.md`

This document explains what five independent audits found in Sauti Dispatch before it takes live calls, how the findings were checked and merged, and what is decided for each one. It is the record that the fix plan and ADR-0029 argue from. It contains no clinical wording and quotes no governing document. The Build Prompt is cited by section only.

## Scope and method

**Five lenses, read-only.** Each audit read the code on `main` at 1123d40 and changed nothing:

| Lens | Surface | Source IDs | As filed |
|---|---|---|---|
| Caller, USSD | Tool 1 on the short code | CU-1 … CU-21 | 1 Critical, 4 High, 10 Medium, 6 Low |
| Caller, voice and SMS | Toll-free IVR, masked bridge, caller SMS | CV-1 … CV-29 | 9 High, 10 Medium, 10 Low |
| Responder | Alert SMS, responder USSD menu, voice alerts, bridge | RS-1 … RS-26 | 5 High, 15 Medium, 6 Low |
| Staff operations | Coordinator calls, facility forwarding, `sautictl`, runbooks | SO-1 … SO-25 | 1 Critical, 9 High, 11 Medium, 4 Low |
| Reliability and readiness | Failover, observability, CI gates, security, compliance | RR-1 … RR-28 | 1 Critical, 4 High, 17 Medium, 6 Low |

A sixth input came from the M3 planning: an assignment is released only when its incident closes, and only the M5 console closes incidents. It matches SO-9 and is folded into F-75.

**Sources.** Each audit cited published standards and guidance: ISO 9241-110:2020, the Nielsen Norman Group heuristics, NENA-STA-020.2-2026, ETSI EG 202 116, ETSI ES 202 076 and ETSI ETR 329, W3C VoiceXML 2.0, the Asterisk and ARI documentation, the UK NCSC guidance on SMS and telephone, PagerDuty's incident response guides, the Google SRE books, NIST SP 800-53r5 and SP 800-61r3, the OWASP ASVS 5.0 and Top 10:2025, clig.dev, Diátaxis, and the Uganda Data Protection and Privacy Act 2019. Each finding below names the standard it rests on, with its URL.

**How the findings were consolidated.**
1. **Verified.** Every Critical and High finding was spot-checked against the code on `main`: the cited lines exist and say what the audit claims. One citation was off by line numbers (CU-5 cites `internal/intake/sweeper.go:182-192`; the rule is at `internal/intake/sweeper.go:98-108`, as RR-3 cites). No High or Critical finding was dropped for want of evidence.
2. **Merged.** Findings that describe one defect from several surfaces became one canonical finding, F-01 to F-92, listing every source ID. Each of the 129 source findings maps to at least one canonical finding. Five describe two defects and are split across two canonical findings, each listing the source with "(part)": CV-6 and CV-10 (F-33, F-54), RR-5 (F-45, F-46), RR-13 (F-03, F-62) and RS-6 (F-31, F-61). The "As filed" counts above count each source finding once; the summary below counts canonical findings.
3. **Re-graded** by the effect on a real caller, responder or coordinator once live (below).
4. **Classified:**
   - **(a)** code-fixable now, inside the protocol: fail-safe, reliability, consistency, non-clinical text structure;
   - **(b)** changes the Tool 1 or alert protocol, or its clinical wording: built behind a configuration flag that defaults to today's protocol, or as PENDING copy, and flagged for the PI and the amendment gate;
   - **(c)** needs a decision by the DPO, the PI or the operator;
   - **(d)** infrastructure (P0) or a GitHub repository setting: a recommendation to the owner, not a code task;
   - **(e)** belongs to a later milestone, which is named.

**Re-grades.**
- **CU-1 (Critical → High, F-07).** The out-of-area screen tells the caller to call 999 or 112, and a redial starts a fresh report. The harm is delay and a lost red report, not a silent loss. The audit's first fix would also not work as written: an escalation appended in the same batch as `incident.closed` never rings, because `op.claim_coordinator_calls` skips closed incidents (`db/operational/migrations/0022_forward_and_coordinator_calls.sql:123`). The incident must stay open to reach a coordinator.
- **CU-4 (High → Medium, F-82).** The abandoned danger-yes call still reaches a coordinator, about two minutes later. The faster signal rests on an Africa's Talking payload nobody has seen.
- **CV-8 (High → Medium, F-35).** A Luganda caller who cannot follow the English bridge prompt is handed to a coordinator after three tries; the dead line on "no answer" is F-02.
- **RS-4 (High → Medium, F-26).** The cascade and the coordinator still pursue the incident; the loss is one willing responder.
- **RS-5 (High → Medium, F-27).** A mis-mapped decline reason corrupts a study item; no caller or responder is harmed.
- **CV-6 (High) is split.** Digits lost on silence stay High (F-33, class a). The missing "no number" exit and read-back are protocol changes (F-54, class b, Medium).
- **RS-1, RS-14 and SO-15** are merged into F-02 (High). Alone, RS-1 is Medium: the alert cascade moves on at 90 s whatever the call does.
- **CU-3 is worse than filed (F-52).** If the first report reached T2 at screen 7 and dropped at screen 8, a second emergency on the same phone within 10 minutes resumes at screen 8 and ends with "help is coming" for the first incident.
- **CV-22 is kept as a documented trade-off,** not a defect: ADR-0023 chose to discard keys pressed before a prompt starts. The M7 field test revisits it (F-88).

## Summary

| Severity | (a) now | (b) protocol, flagged | (c) DPO, PI, operator | (d) P0 or repository | (e) later milestone | Total |
|---|---|---|---|---|---|---|
| Critical | 2 | 0 | 0 | 0 | 0 | 2 |
| High | 14 | 4 | 1 | 1 | 1 | 21 |
| Medium | 32 | 7 | 4 | 1 | 1 | 45 |
| Low | 14 | 1 | 2 | 2 | 5 | 24 |
| **Total** | **62** | **12** | **7** | **4** | **7** | **92** |

**Phases of the fix plan.** UX-1 every path ends with a person; UX-2 leader and connection robustness; UX-3 responder and coordinator voice correctness; UX-4 caller clarity and trust cues; UX-5 audit trail; UX-6 protocol proposals behind flags; UX-7 production refusals and security configuration; UX-8 observability, operator tools and test depth. Classes (c), (d) and (e) are not planned as code; they are listed under [Recommendations to the owners](#recommendations-to-the-owners).

## Findings: Critical and High

Each entry gives the source IDs, the surfaces, the evidence on `main`, a short user story, the standard, and the decision with its phase.

### F-01 · Critical · (a) · An unanswered escalation rings one line forever, with no second tier and no dead-man signal
- **Sources:** SO-1. **Surfaces:** coordinator calls.
- **Evidence:** one endpoint, retried until someone presses 1 (`internal/platform/ivr/outbound.go:396-397`); the `calls` counter is incremented and never acted on (`db/operational/migrations/0022_forward_and_coordinator_calls.sql:129-131`); ivr-controller registers no metric (`cmd/ivr-controller/main.go:146`).
- **Story:** At 02:00 the duty coordinator's phone is flat. A red call has no eligible responder. The line rings every two minutes all night, and nobody else is told.
- **Standard:** PagerDuty, Being on-call ("Always have a backup schedule"): https://response.pagerduty.com/oncall/being_oncall/ · Google SRE, Being On-Call (the secondary as a fall-through): https://sre.google/sre-book/being-on-call/
- **Decision:** UX-1. After `escalation_backup_after_calls` unanswered rings (default 3), also ring a backup endpoint, and export the age of the oldest unacknowledged escalation from intake-gw, which survives an ivr-controller outage. With no backup endpoint configured, behaviour is unchanged and production refuses to start (UX-7). Who the second tier is: recommendation R-1.
- **Status (2026-11-14):** implemented in UX-1 ([ADR-0034](../adr/ADR-0034-every-failure-path-ends-with-a-person.md)): tier 2 after `escalation.backup_after_calls` (3, PENDING the PI) and the dead-man gauges from intake-gw. The production refusal without a backup line is UX-7.

### F-02 · High · (a) · Voice paths that end without a person
- **Sources:** CV-1, CV-2, CV-3, CV-5, CV-9, CU-14, RS-1, RS-14, RR-9, SO-15. **Surfaces:** IVR, dialplan fallback, masked bridge, voice alerts.
- **Evidence:**
  - Every answer, an invalid one included, resets the reprompt count, so a wrong key loops for ever (`internal/platform/ivr/controller.go:327`, `internal/intake/service.go:321-322`).
  - Silence ends in a hang-up after two reprompts (`internal/platform/ivr/controller.go:331-334`; `cmd/ivr-controller/main.go:144`).
  - The fallback dials once for 60 s and hangs up, with no `DIALSTATUS` branch, no closing message and no escalation (`telephony/asterisk/extensions.conf:39-43`; `internal/platform/ivr/controller.go:379`).
  - Events of an unknown call are dropped, and the no-input timer starts only on `PlaybackFinished`, so a restart or a lost event leaves dead air (`internal/platform/ivr/controller.go:165-171`, `internal/platform/ivr/controller.go:242-243`). On shutdown the workers are dropped without a hand-over (`internal/platform/ivr/controller.go:124-127`).
  - A caller's bridge call that is not answered, or a number with nothing open, ends in a closing message and a hang-up (`internal/platform/ivr/bridge.go:230-233`, `internal/platform/ivr/bridge.go:567-579`).
  - An answered voice alert never times out (`internal/platform/ivr/alerts.go:58-73`, `internal/platform/ivr/alerts.go:228-240`).
- **Story:** A caller holding a bleeding child cannot press keys. She hears the question three times and the line drops. Had she answered "danger: no", nobody would call back.
- **Standard:** ETSI EG 202 116 V1.2.2 §7.1.6 and §7.1.9 (recoverable errors; confirm before disconnecting): https://www.etsi.org/deliver/etsi_eg/202100_202199/202116/01.02.02_60/eg_202116v010202p.pdf · NENA-STA-020.2-2026 §5.4 (re-contact abandoned and silent calls): https://cdn.ymaws.com/www.nena.org/resource/resmgr/standards/standards1/NENA-STA-020.2-2026_911CallP.pdf · Asterisk `Dial()`: https://docs.asterisk.org/Latest_API/API_Documentation/Dialplan_Applications/Dial/ · Build Prompt §3 rule 14.
- **Decision:** UX-1, under ADR-0029's retry and escalation rules. Three no-matches hand over to a coordinator. Silence hands over once any screen after the language screen is answered (a call silent from the first prompt is still hung up, as a pocket dial). Every hand-over of a call that belongs to an incident appends an `incident.escalated` event (`voice_handed_over`), so the coordinator line keeps ringing if the dialplan's dial fails. A call with no incident has nothing to escalate, and the live dial is its only path, as today: an unknown number on the bridge, or a voice call whose intake failed before an incident was opened (the store that failed is the one an escalation would be written to). A durable record for such a call would need an incident opened outside Tool 1 or a new record kind, which is the PI's and the DPO's decision; neither is built. The dialplan tries the coordinator a bounded number of times, then plays a closing message. A prompt has a backstop timer, live channels are handed over on reconnect and on shutdown, the bridge sends an unanswered or unassigned caller to a coordinator, and an answered alert call repeats once and then closes.
- **Status (2026-11-14):** implemented in UX-1 ([ADR-0034](../adr/ADR-0034-every-failure-path-ends-with-a-person.md)). The bridge case was hardened in review: an open report with nobody assigned sounds exactly like an unknown number (T-030), and is escalated quietly.

### F-03 · High · (a) · A failed recording plays as silence and counts as heard
- **Sources:** CV-4, RR-13 (part: the sound check). **Surfaces:** IVR, bridge, alerts.
- **Evidence:** the decoded `Playback` has no `state` (`internal/platform/ari/client.go:306-309`); a finished playback starts the no-input timer whatever its state (`internal/platform/ivr/controller.go:234-244`); only an HTTP error from `Play` hands the call over (`internal/platform/ivr/controller.go:345-362`). The bridge runbook promises a log line that this path never writes (`docs/runbooks/bridge-failing.md:28`).
- **Story:** The Luganda ward recording was never copied to the host. The caller hears nothing three times and is hung up.
- **Standard:** ARI data models (Playback `state` includes `failed`): https://docs.asterisk.org/Latest_API/API_Documentation/Asterisk_REST_Interface/Asterisk_REST_Data_Models/ · Build Prompt §3 rule 14.
- **Decision:** UX-1. Decode `state`; treat `failed` like a play error (hand over a question, hang up a closing message). UX-7 adds the start-up check that every catalogued sound exists. The runbook is corrected.
- **Status (2026-11-14):** implemented in UX-1 ([ADR-0034](../adr/ADR-0034-every-failure-path-ends-with-a-person.md)); the bridge runbook quotes the real log line. The start-up sound check is UX-7.

### F-04 · High · (a) · An incident with no T2 is neither dispatched nor escalated unless it is danger-yes or red
- **Sources:** CU-5, RR-3. **Surfaces:** USSD and IVR intake, the abandonment sweeper.
- **Evidence:** the sweeper escalates only `Danger == "yes"` or red (`internal/intake/sweeper.go:98-108`); a failed T2 ends with the error screen and waits for a redial (`internal/intake/service.go:342-346`, `internal/intake/service.go:502-505`); the watchdog reads only `dispatching` incidents (`db/operational/queries/incidents.sql:148`).
- **Story:** A mother reports a yellow poisoning and picks her ward. The screen-7 request times out on a database blip. She is told to call a line she cannot call, and the record says she abandoned.
- **Standard:** Build Prompt §3 rule 14; the spec's "fail safe to a human" (`docs/superpowers/specs/2026-11-12-sauti-dispatch-design.md`); OWASP Top 10:2025 A10, Mishandling of Exceptional Conditions: https://top10.owasp.org/2025/
- **Decision:** UX-1. The sweeper first runs the existing catch-up, which records T2 when the landmark is answered (what a redial would do). Any idle incident still without T2 after a ward answer, or after the trigger screen, is escalated `dispatch_not_started`. Whether such calls should instead be dispatched at ward level is the PI's choice (R-9); the safe default is a person.
- **Status (2026-11-14):** implemented in UX-1 ([ADR-0034](../adr/ADR-0034-every-failure-path-ends-with-a-person.md)): catch-up first, then `dispatch_not_started`. Ward-level dispatch stays the PI's choice (R-9).

### F-05 · High · (a) · A dispatch stall after the first alert reaches no one
- **Sources:** SO-5. **Surfaces:** the dispatch watchdog.
- **Evidence:** `ListStalledDispatch` requires `i.t3 IS NULL` (`db/operational/queries/incidents.sql:148`); the red and no-eligible escalations are computed only by the worker's planner (`internal/dispatch/plan.go:299-327`).
- **Story:** A VHT is alerted at T3 and the worker is killed ten seconds later. The alert never lapses and nothing escalates, because T3 is set.
- **Standard:** Build Prompt §3 rule 14 · Google SRE Workbook, On-Call: https://sre.google/workbook/on-call/
- **Decision:** UX-1. The watchdog also lists an incident whose dispatch timer is overdue by the watchdog interval, and escalates it `dispatch_stalled` as today.
- **Status (2026-11-14):** implemented in UX-1 ([ADR-0034](../adr/ADR-0034-every-failure-path-ends-with-a-person.md)).

### F-06 · High · (a) · Earlier escalation reasons are acknowledged unheard, and a merge drops an unacknowledged escalation
- **Sources:** SO-3. **Surfaces:** coordinator calls, deduplication.
- **Evidence:** the claim reads only the latest reason, `ORDER BY e.seq DESC LIMIT 1` (`db/operational/migrations/0022_forward_and_coordinator_calls.sql:118-121`), while one acknowledgement covers every earlier escalation (`db/operational/migrations/0022_forward_and_coordinator_calls.sql:142-149`); a merge re-escalates the survivor only if it was never escalated (`internal/intake/dedup.go:127`).
- **Story:** A triage-rule error is escalated and missed. Two minutes later "no eligible responder" rings, the coordinator presses 1, and the rule error is closed without anyone hearing it.
- **Standard:** PagerDuty, Alerting principles (every page actionable): https://response.pagerduty.com/oncall/alerting_principles/ · Build Prompt §3 rule 14.
- **Decision:** UX-1, in a new migration (the merged 0022 is not edited). The claim returns every unacknowledged reason, played most urgent first; a merge escalates the survivor whenever the absorbed call holds an unacknowledged escalation.
- **Status (2026-11-14):** implemented in UX-1 ([ADR-0034](../adr/ADR-0034-every-failure-path-ends-with-a-person.md); migration 0033). The survivor's payload stays `{reason}`: the event schema admits no `from_reason`.

### F-07 · High (was Critical) · (b) · "Somewhere else" closes the incident on one key, with no confirmation and no escalation
- **Sources:** CU-1, CV-14. **Surfaces:** Tool 1 screen 6, USSD and IVR.
- **Evidence:** "Somewhere else" is the last ward digit (`internal/intake/machine.go:187-188`) and wins before every other check (`internal/intake/machine.go:223-224`); the flag and `incident.closed` are appended with the answer (`internal/intake/service.go:433-443`); a redial cannot resume a closed incident (`db/operational/queries/incidents.sql:86`).
- **Story:** A caller whose husband is not breathing does not recognise her parish name and presses the last option. She is told Sauti does not cover the area. Her red report closes and no coordinator hears of it.
- **Standard:** ISO 9241-110:2020, use error robustness: https://cdn.standards.iteh.ai/samples/75258/83c8cf072187487686645aad04eff40e/ISO-9241-110-2020.pdf · NENA-STA-020.2-2026 §4.1.2 (verify location first) · Nielsen heuristic 5: https://www.nngroup.com/articles/ten-usability-heuristics/
- **Decision:** Build Prompt §10.1 says the incident closes, so both fixes are protocol changes, behind flags that default to today's behaviour, for the PI and the amendment gate. UX-1: with `out_of_area_escalate_after_danger` on, an out-of-area answer after a danger yes or a red classification flags and escalates the incident and leaves it open. UX-6: with `out_of_area_confirm` on, a confirmation step ("1 yes, 2 show the wards again") precedes the close, as PENDING copy.
- **Status (2026-11-14):** UX-1 part implemented behind `out_of_area_escalate_after_danger`, committed `false` ([ADR-0034](../adr/ADR-0034-every-failure-path-ends-with-a-person.md)); the confirmation step is UX-6.

### F-08 · High · (a) · No pre-M5 tool lists the unacknowledged escalations and the open tasks
- **Sources:** SO-7, SO-21. **Surfaces:** `sautictl`, runbooks.
- **Evidence:** the runbook asks a coordinator to paste SQL every minute (`docs/runbooks/no-eligible-responders.md:17`); `sautictl` has no escalations command (`cmd/sautictl/main.go:61-74`); `needs_location_callback` is a flag only (`internal/intake/service.go:545`).
- **Story:** When calls fail, a clinical coordinator keeps a psql session open on a weak link and reads UUIDs that do not match the reference they hear.
- **Standard:** Google SRE Workbook, On-Call (automate deterministic playbooks): https://sre.google/workbook/on-call/
- **Decision:** UX-1. `sautictl escalations` lists open incidents with unacknowledged escalations (reference, severity, reasons, rings, age) and the location call-back tasks, most severe first, as `op_readonly`.
- **Status (2026-11-14):** implemented in UX-1: `sautictl escalations` ([ADR-0034](../adr/ADR-0034-every-failure-path-ends-with-a-person.md)).

### F-09 · High · (a) · The runbooks are stale on escalation calls and silent on what an ivr-controller outage stops
- **Sources:** SO-6, SO-8, SO-18. **Surfaces:** runbooks.
- **Evidence:** three runbooks still say an escalation is only a recorded event (`docs/runbooks/no-eligible-responders.md:14`, `docs/runbooks/suspension-and-resumption.md:22`, `docs/runbooks/abandoned-danger-callback.md:15`); the ivr-controller outage runbook says USSD is unaffected and omits coordinator calls, voice alerts, forwarding and the bridge (`docs/runbooks/ivr-controller-down.md:24`); the gazetteer runbook never restarts ivr-controller (`docs/runbooks/gazetteer-update.md:38`); several steps ask for a call-back that only the M5 console may make.
- **Story:** At 03:00 the phone rings "no eligible responder". The runbook says escalations do not ring and tells the coordinator to call the caller back, which they cannot lawfully do.
- **Standard:** Diátaxis, how-to guides: https://diataxis.fr/how-to-guides/ · NIST SP 800-61r3 (actionable playbooks): https://csrc.nist.gov/pubs/sp/800/61/r3/final
- **Decision:** UX-1 docs task, with a docs test that fails on the stale phrases. What a coordinator does before M5 when a call-back is needed is a protocol question (R-3).
- **Status (2026-11-14):** implemented in UX-1: runbooks updated, with `governance/tests/test_runbooks.py::test_no_stale_escalation_phrases`. The pre-M5 call-back remains R-3.

### F-15 · Critical · (a) · Leader locks stay held for hours after a host is lost
- **Sources:** RR-1. **Surfaces:** every leader: outbox-relay, the sweeper and watchdog, dispatch-worker, outbound and coordinator calls.
- **Evidence:** session advisory locks (`internal/platform/pg/leader.go:66`); the pool sets no TCP keepalive or user timeout (`internal/platform/pg/pg.go:65-68`); nothing in `internal` or `cmd` sets one.
- **Story:** A Site A server loses power. Its backends hold the locks until the kernel gives up, about two hours. Calls are recorded on the other site, but no responder is alerted and no coordinator is rung.
- **Standard:** PostgreSQL 18, connection settings (`tcp_keepalives_*`, `tcp_user_timeout`): https://www.postgresql.org/docs/18/runtime-config-connection.html · advisory locks: https://www.postgresql.org/docs/18/explicit-locking.html#ADVISORY-LOCKS
- **Decision:** UX-2: client keepalive and user-timeout settings on every pool and on the lock connection, with a takeover test through a black-holed proxy. The server settings and HAProxy timeouts are P0 (R-13).

### F-24 · High · (a) · A voice alert speaks an alert that is already stood down, lapsed or declined, and can overwrite a decline
- **Sources:** RS-2. **Surfaces:** voice alerts.
- **Evidence:** `AlertForAttempt` returns the outcome (`db/operational/queries/dispatch.sql:204-207`), and the call plays the alert without looking at it (`internal/platform/ivr/alerts.go:203-216`); key 1 accepts with no pending check (`internal/platform/ivr/alerts.go:266-267`).
- **Story:** A rider's "no longer needed" SMS arrives at 26 s. At 30 s he answers the call, hears the alert, presses 1 and is told it is taken. He learns to ignore Sauti calls.
- **Standard:** ISA-18.2 (an alarm requires a timely response): https://www.isa.org/intech-home/2016/may-june/features/getting-the-most-from-your-safety-alarms · Nielsen heuristic 1.
- **Decision:** UX-3. An answered call branches on the outcome: not pending plays the existing result prompt and hangs up; key 1 refuses a declined alert.

### F-25 · High · (a) · The coordinator call carries no severity, category, place or role
- **Sources:** SO-2. **Surfaces:** coordinator calls.
- **Evidence:** the call plays the intro, ten digits, the reason and the options (`internal/platform/ivr/outbound.go:631-634`); the claim drops the role and the severity (`db/operational/migrations/0022_forward_and_coordinator_calls.sql:116`).
- **Story:** The coordinator hears ten digits and "no eligible responder". They cannot tell red from green, the VHT from the transport, or one ward from another, and cannot choose which call to act on first.
- **Standard:** NENA-STA-020.2-2026 §5.3 (relay location and nature) · PagerDuty, Alerting principles · Build Prompt §10.10.
- **Decision:** UX-3. Speak colour, category, ward and landmark (recordings that exist) and the role (new PENDING recordings), from the existing `MessageFacts`.

### F-33 · High · (a) · Voice call-back digits are lost on silence, and `*` is typed into the number
- **Sources:** CV-6 (part), CV-10 (part). **Surfaces:** IVR screen 8.
- **Evidence:** the number is taken only on `#` (`internal/platform/ivr/controller.go:292-295`); silence clears typed digits (`internal/platform/ivr/controller.go:337`); `*` is appended (`internal/platform/ivr/controller.go:298-299`).
- **Story:** A caller with a hidden number types her number and waits. Nothing tells her to press hash. Fifteen seconds later the question repeats and her digits are gone.
- **Standard:** ETSI EG 202 116 §7.1.9 (timeouts must not lose data) and §11.2 · W3C VoiceXML 2.0 §6.3.3 (entry ends on the terminator or the inter-digit timeout): https://www.w3.org/TR/voicexml20/
- **Decision:** UX-4. On silence, digits that parse as a number are submitted; `*` clears the digits (ADR-0029 key conventions). The "then press hash" wording is in the voice script (F-38).

### F-34 · High · (a) · No text can name Sauti's number, the bridge number or the responder code
- **Sources:** CV-7, RS-11, RS-20. **Surfaces:** caller SMS, responder SMS and USSD screens.
- **Evidence:** `helper_on_the_way` allows only `{ref}` (`internal/config/sms.go:31`) and any run of digits is refused (`internal/config/sms.go:65`, `internal/config/sms.go:104-106`); the menu's accepted text names a "bridge line" with no number (`config/gated/responder.yaml:35-36`); `*384*2#` is typed into the SMS copy while the real code comes from the environment (`config/gated/sms.yaml:13`).
- **Story:** Minutes after "help is on the way", an unknown number rings. The caller takes it for a scam and rejects it; the helper never gets directions.
- **Standard:** UK NCSC, SMS and telephone best practice ("publicise your contact details"): https://www.ncsc.gov.uk/guidance/business-communications-sms-and-telephone-best-practice · Build Prompt §10.4, §10.5.
- **Decision:** UX-4. Add `{sauti_number}`, `{bridge}` and `{code}` placeholders, filled from deployment configuration and exempt from the digit-run check only as rendered values. The copy stays PENDING for the PI.

### F-44 · High · (a) · A coordinator's acknowledgement leaves no audit trail
- **Sources:** SO-4. **Surfaces:** coordinator calls, the ledger.
- **Evidence:** the acknowledgement is an `UPDATE` that resets the ring count, with no event, outbox row or attempt row (`db/operational/migrations/0022_forward_and_coordinator_calls.sql:144-151`); facility acknowledgements, by contrast, are events (`db/operational/migrations/0015_dispatch.sql:677-678`).
- **Story:** A red call ends badly. The review asks when the coordinator was first rung and how often. The store can say only "acknowledged at 03:41".
- **Standard:** NIST SP 800-53r5 AU-3: https://csrc.nist.gov/pubs/sp/800/53/r5/upd1/final · Build Prompt §3 rule 8, §9.
- **Decision:** UX-5. One attempt row per ring (`op.coordinator_call_attempts`, as for alerts) and an `incident.escalation_acknowledged` event carrying the acknowledged sequence. The staff identity cannot come from a key press; M5 binds it.

### F-45 · High · (a) · Registry, availability, gazetteer and bridge changes bypass the ledger
- **Sources:** RR-5 (part). **Surfaces:** the operational store and the ledger.
- **Evidence:** the registry and gate functions write no outbox row (`db/operational/migrations/0013_responders.sql:56-88`, and their replacements in `db/operational/migrations/0023_review_pr10_coderabbit.sql`); nor do gazetteer loads or bridge calls.
- **Story:** Someone sets a VHT's kit flag to false, removing them from every alert. Nothing records who did it or when.
- **Standard:** OWASP ASVS 5.0 16.2.1 (when, where, who, what): https://github.com/OWASP/ASVS/blob/v5.0.0/5.0/en/0x25-V16-Security-Logging-and-Error-Handling.md
- **Decision:** UX-5. Each such function writes one ledger entry of identifiers and a salted hash (the `op.log_control` pattern of 0016). A test generated from `pg_catalog` fails when a new state-changing function writes none.

### F-46 · High · (c) · Staff identity is whatever the operator types
- **Sources:** SO-10, RR-5 (part). **Surfaces:** `sautictl` suspend, resume, split.
- **Evidence:** the only check is that `--staff` parses as a UUID (`cmd/sautictl/main.go:654-655`); the database checks presence only (`db/operational/migrations/0016_dispatch_control.sql:102-103`); `op_console` is one shared role.
- **Story:** After a stopping-rule breach, dispatch is resumed "by" a UUID that maps to no one and that anyone holding the DSN could have typed.
- **Standard:** NIST SP 800-53r5 AU-10 · OWASP Logging Cheat Sheet: https://cheatsheetseries.owasp.org/cheatsheets/Logging_Cheat_Sheet.html
- **Decision:** a staff register is staff personal data (rule 12): the DPO decides (R-4). Per-person database roles are P0 (R-16). The console binds the identity to a signed-in coordinator in M5.

### F-51 · High · (b) · A wrong ward or landmark is dispatched and never read back
- **Sources:** CU-2. **Surfaces:** Tool 1 screens 6 to 8.
- **Evidence:** T2 is recorded the moment screen 7 is answered (`internal/intake/machine.go:345`); the closing text has no place (`config/gated/tool1.yaml:69`); a different ward or landmark answer is refused later (`db/operational/migrations/0006_tool1_projection.sql:304-306`).
- **Story:** A bystander means one market and presses the one next to it. Responders go to the wrong ward, and the caller is told help is coming.
- **Standard:** NENA-STA-020.2-2026 §5.2.1 (a verified location is repeated or confirmed back) · ISO 9241-110 self-descriptiveness.
- **Decision:** UX-6, behind `place_readback` (default off). The screen-8 prompt and the closing text carry a `{place}` line, within the screen gate. A read-back step takes `1` right or `2` wrong; `2` keeps the recorded place, flags `needs_location_callback` and escalates `place_disputed`, so a person corrects the place and tells the responders, through M5's relocation tool (ADR-0029 rule 4). PENDING copy for the PI and translation.

### F-52 · High · (b) · A redial resumes another emergency on a shared phone without saying so
- **Sources:** CU-3, CV-25. **Surfaces:** USSD and IVR resume.
- **Evidence:** a redial within `resume_window` resumes silently (`internal/intake/service.go:207-224`; `db/operational/queries/incidents.sql:83-89`); answered danger, category and age cannot change (`db/operational/migrations/0006_tool1_projection.sql:304-306`).
- **Story:** A rider starts a report on a stage phone and stops. Six minutes later a second rider dials about a woman in labour, sees "Age of the person?" and goes on. The labour is recorded as the first rider's injury. If the first report had reached T2 and dropped at screen 8, the second caller is told help is coming for the first incident.
- **Standard:** ISO 9241-110 self-descriptiveness · Nielsen heuristic 1.
- **Decision:** UX-6, behind `resume_confirm` (default off). When the resumed incident has an answer past the language screen, the first screen asks "continue report {ref}, or new report"; the IVR plays a short resume prompt. PENDING copy.

### F-53 · High · (b) · A responder cannot see where the incident is before accepting
- **Sources:** RS-3, RS-26. **Surfaces:** responder USSD menu, voice alerts.
- **Evidence:** the alert text may carry only `{ref}`, `{colour}` and `{category}` (`internal/config/responder.go:31`); the voice alert speaks no place, and speaks ten digits before the colour (`internal/platform/ivr/alerts.go:346-358`).
- **Story:** A VHT sees "RED obstetric / 1 Accept 2 Decline" and cannot tell whether it is his village or across the swamp.
- **Standard:** ETSI ETR 329 §5.1.1 (information before the control action): https://www.etsi.org/deliver/etsi_etr/300_399/329/01_60/etr_329e01p.pdf
- **Decision:** UX-6, behind `alert_location_before_accept` (default off). The menu alert may carry `{ward}` and `{landmark}`; the voice alert speaks colour, category, ward and landmark, then the options, then the number. SMS stays unchanged (decision 1). The PI and the DPO confirm that decision 1 covers voice and USSD before acceptance (R-9).

### F-67 · High · (a) · intake-gw and ivr-controller export no metrics; no alert rule exists
- **Sources:** RR-2, CU-13. **Surfaces:** every service level of Build Prompt §14.
- **Evidence:** both processes build a registry and register nothing (`cmd/intake-gw/main.go:154-158`, `cmd/ivr-controller/main.go:146`); the metrics package has no histogram (`internal/platform/metrics/metrics.go:19-22`); no rules file exists.
- **Story:** USSD answers degrade to the error screen for an hour. Nobody is paged; the only trace is a log line.
- **Standard:** Google SRE Workbook, Implementing SLOs and Alerting on SLOs: https://sre.google/workbook/implementing-slos/, https://sre.google/workbook/alerting-on-slos/
- **Decision:** UX-8. A fixed-bucket histogram, the USSD, IVR and delivery counters, and a Prometheus rules file tested with `promtool` in CI. Paging is P0 (R-16); the SLO numbers are the PI's (R-10).

### F-75 · High · (e: M3 Part A, M5) · An assignment is never released, so the responder pool shrinks with every call
- **Sources:** SO-9, RS-15, and the M3 planning finding. **Surfaces:** dispatch, the responder menu.
- **Evidence:** busy means an assignment with `released_at IS NULL` on an open incident (`db/operational/queries/dispatch.sql:90-93`); `released_at` is declared (`db/operational/migrations/0015_dispatch.sql:155`) and never written; only intake's out-of-area path closes an incident (`internal/intake/service.go:438`).
- **Story:** In a two-week drill each acceptance locks a responder for good. By the second week ring 1 is empty, and every call escalates "no eligible responder".
- **Standard:** Build Prompt §10.4 · APCO ANS 1.121.1-2022, operational overload: https://www.apcointl.org/~documents/standard/11211-2022-managing-operational-overload-in-the-ecc?layout=default
- **Decision:** not planned here. M3 Part A releases the assignment on the responder's final field status, with the default flagged for the PI. M5 builds the coordinator's close. Until then, a drill must reset assignments by hand, and the menu should say "on {ref}" (RS-15's interim line is folded into M3 Part A's menu work).

### F-76 · High · (d) · The main-branch ruleset requires no passing check and no review
- **Sources:** RR-4. **Surfaces:** the repository.
- **Evidence:** ruleset 27188855 has `required_status_checks: []`, `required_approving_review_count: 0` and `require_code_owner_review: false` (read through the GitHub API on 2026-11-13).
- **Story:** A change to `triage-rules/` merges with the amendment gate failing, because the gate is advisory.
- **Standard:** OpenSSF Scorecard, Branch-Protection and Code-Review: https://github.com/ossf/scorecard/blob/main/docs/checks.md · NIST SSDF PW.7: https://doi.org/10.6028/NIST.SP.800-218
- **Decision:** a repository setting for the owner (R-12).

## Findings: Medium and Low, by phase

Each entry gives the severity and class, the source IDs and surfaces, the evidence as filed, a short story, the standard and the decision. Medium and Low evidence was read in the audits and not re-verified line by line (see [Unverified](#unverified)).

### UX-1: every path ends with a person

**F-10 · Medium · (a) · The safety nets stop at the first incident that fails.** RR-18 · sweeper, watchdog. Evidence: `internal/intake/sweeper.go:88-90`, `internal/intake/watchdog.go:61-64`; both lists are stably ordered (`db/operational/queries/incidents.sql:103`). Story: one incident whose append keeps failing sits first in both lists, and every red incident behind it waits for ever. Standard: OWASP Top 10:2025 A10, https://top10.owasp.org/2025/. Decision: UX-1; log and count each failure, continue, return a joined error. **Status (2026-11-14):** implemented in UX-1 ([ADR-0034](../adr/ADR-0034-every-failure-path-ends-with-a-person.md)).

**F-11 · Medium · (a) · Escalation reasons have no single registry.** SO-20 · reasons, recordings, runbooks. Evidence: hand-kept in `internal/dispatch/plan.go:89-97`, `internal/platform/ivr/outbound.go:695-699`, `db/operational/queries/incidents.sql:152-154`, `docs/reference/events.md` and `docs/runbooks/no-eligible-responders.md`; `assigned_follow_up` has no runbook row. Story: a reason added in one place is announced as "other", and a coordinator finds no runbook for what they hear. Standard: Diátaxis, reference: https://diataxis.fr/reference/. Decision: UX-1, before UX-1 adds four reasons; a test ties every reason to a recording and a runbook row. **Status (2026-11-14):** implemented in UX-1: `internal/incident/reasons.go`.

**F-12 · Medium · (a) · A coordinator taking a fallback call sees the caller's own number and no reference.** CV-17 · dialplan fallback. Evidence: `internal/platform/ivr/controller.go:374` continues with the caller ID untouched; `telephony/asterisk/extensions.conf:42` passes no reference. Story: the coordinator answers with no idea the caller already answered five screens, and the caller's number appears outside the audited reveal. Standard: NENA-STA-020.2-2026 §5.3 (warm transfer relays what is known); Build Prompt §3 rule 5. Decision: UX-1; the fallback dialplan, before every dial, sets the caller ID number to Sauti's and the name to "SAUTI" and the reference (a channel variable set by ivr-controller). Masking in the dialplan cannot fail open, and it also covers the bridge's hand-over and a fallback reached with the application down, where no Go code runs. **Status (2026-11-14):** implemented in UX-1 ([ADR-0034](../adr/ADR-0034-every-failure-path-ends-with-a-person.md)).

**F-13 · Low · (a) · The inbound dialplan misses a '+'-prefixed DID.** CV-28 · dialplan. Evidence: `_X.` is the only extension in each inbound context (`telephony/asterisk/extensions.conf:14`). Story: a carrier that delivers `+256…` gets a 404 before Stasis, and even the fallback never runs. Standard: Build Prompt §3 rule 14. Decision: UX-1; add `_+X.` beside `_X.`; confirm the DID format in P0. **Status (2026-11-14):** implemented in UX-1; the DID format is still to confirm in P0.

**F-14 · Low · (a) · The USSD adapter reference cites a test that does not exist.** CU-21 · docs. Evidence: `docs/reference/ussd-at-adapter.md:40`. Story: a reviewer checking the screen gate follows a dead pointer. Standard: the living-documentation rule (pointers resolve). Decision: UX-1 docs task; point at `internal/intake/machine_test.go::TestEveryRenderedScreenFitsLimitAndGSM7`. **Status (2026-11-14):** implemented in UX-1.

### UX-2: leader and connection robustness

**F-16 · Medium · (a) · intake-gw and ivr-controller can hang on exit holding a leader lock.** RR-7 · process lifecycle. Evidence: `defer pool.Close()` runs before the leader is awaited (`cmd/intake-gw/main.go:102`, `cmd/intake-gw/main.go:171`); dispatch-worker already waits (`cmd/dispatch-worker/main.go:188-193`). Story: a bind error on restart makes the process return, `pool.Close` blocks for ever, and the sweeper and watchdog are gone on every site. Standard: Go `http.Server.Shutdown`, https://pkg.go.dev/net/http#Server.Shutdown. Decision: UX-2; one shared await helper.

**F-17 · Medium · (a) · Two processes default to the same admin port, and one variable means two things.** RR-8 · configuration. Evidence: `cmd/outbox-relay/main.go:15` and `cmd/ivr-controller/main.go:22` both default to `127.0.0.1:9101`; `SAUTI_HTTP_ADDR` is intake-gw's public listener. Story: on the co-located Site A host, the audit relay or the voice line crash-loops. Standard: Prometheus naming practice, https://prometheus.io/docs/practices/naming/. Decision: UX-2; unique defaults, one variable name, a test over every default.

**F-18 · Medium · (a) · Migrations run without a lock timeout.** RR-11 · migrator. Evidence: `cmd/sautictl/main.go:108` connects with no `lock_timeout`; each file is one transaction (`internal/platform/pg/migrate/migrate.go:141-150`). Story: an idle `psql` transaction blocks an `ALTER`, and every intake write queues behind it for up to 30 minutes. Standard: PostgreSQL 18 `lock_timeout`, https://www.postgresql.org/docs/18/runtime-config-client.html#GUC-LOCK-TIMEOUT. Decision: UX-2; `SET LOCAL lock_timeout` and `statement_timeout` with bounded retries. Role-level idle timeouts for human roles are P0 (R-16).

**F-19 · Medium · (a) · The ARI event stream has no heartbeat.** RR-10 · ivr-controller. Evidence: `internal/platform/ari/client.go:349` reads with no deadline and no ping. Story: the Asterisk host loses power; ivr-controller still reports connected and burns voice alerts until TCP notices. Standard: RFC 6455 §5.5.2, https://www.rfc-editor.org/rfc/rfc6455#section-5.5.2. Decision: UX-2; ping every 10 s, close on failure.

**F-20 · Medium · (a) · A wedged leader keeps its lock, and liveness always passes.** RR-6 · leaders. Evidence: `/livez` has an empty check map (`cmd/dispatch-worker/main.go:120`); the pinger runs apart from the loop (`internal/platform/pg/leader.go:119-149`). Story: a blocked dispatch loop keeps the lock; the standby never takes over. Standard: Kubernetes probe semantics, https://kubernetes.io/docs/concepts/configuration/liveness-readiness-startup-probes/. Decision: UX-2; the leader steps down when its loop's last pass is older than the stall limit. `WatchdogSec` in the units is P0.

**F-21 · Medium · (a) · Ungated platform values are never validated.** RR-12 · configuration. Evidence: `internal/config/load.go:55-126` skips the platform file; a zero checkpoint interval panics in the leader (`cmd/outbox-relay/main.go:191`). Story: a typo starts every relay cleanly, and each panics the moment it leads. Standard: OWASP Top 10:2025 A02. Decision: UX-2; `Load` refuses zero or out-of-range values.

**F-22 · Medium · (a) · Provider throttling counts as a permanent rejection.** RR-16 · SMS. Evidence: every 4xx, 429 included, becomes `ErrSMSRejected` (`internal/platform/africastalking/sms.go:104-105`), and a failed attempt is never re-sent. Story: in a surge, every throttled alert settles `failed`, and responders get no SMS. Standard: Google SRE, Handling Overload, https://sre.google/sre-book/handling-overload/. Decision: UX-2; 408 and 429 retry once within the send budget, honouring `Retry-After` up to 2 s. AT's rate limits in writing: R-17.

**F-23 · Medium · (a) · One global lock serialises every T2 inside the 800 ms USSD budget.** CU-6, RR-19 (lock part) · USSD intake. Evidence: `internal/platform/pg/opstore/dedup.go:82`; the budget is the request deadline (`internal/platform/africastalking/ussd.go:73-75`). Story: two bystanders at one crash reach screen 7 together during a slow checkpoint; one gets the error screen. Standard: ISO 9241-110, use error robustness. Decision: UX-2; key the lock by category (candidates are already filtered by category). Turning the 800 ms target into an SLO rather than a hard deadline amends the spec: the PI decides (R-10).

### UX-3: responder and coordinator voice correctness

**F-26 · Medium (was High) · (a) · A lapsed alert is hidden on USSD while voice still accepts it.** RS-4 · responder menu. Evidence: the menu offers Accept only while pending (`internal/respond/ussd.go:114`, `internal/respond/ussd.go:121`); the store accepts after a lapse while the role is open (`db/operational/migrations/0015_dispatch.sql:527-529`). Story: a rider dials two minutes after the SMS, reads "no active alert" and goes home, while the role is still open. Standard: ETSI ETR 329 §5.1.4, consistency. Decision: UX-3; a `no_response` alert on an incident still dispatching is offered as pending, as the store already allows. The PI confirms the late-accept policy (R-6).

**F-27 · Medium (was High) · (a) · The voice decline-reason list is one recording, not the configured list.** RS-5 · voice alerts. Evidence: `internal/platform/ivr/alerts.go:289-290` plays one file; the digit maps to the YAML list (`internal/platform/ivr/alerts.go:261-262`). Story: the PI reorders the list; voice still says the old order, and a reason is stored wrongly. Standard: ETSI ETR 329 §5.5.1.5 (one function per key). Decision: UX-3; compose "press k" and a per-code recording from the configured list.

**F-28 · Medium · (a) · A store error on a voice key leaves dead air.** RS-9, SO-12 · voice alerts, coordinator and facility calls. Evidence: `internal/platform/ivr/alerts.go:267-271`, `internal/platform/ivr/outbound.go:598-604`. Story: a health-centre in-charge presses 1, hears nothing and hangs up believing it worked; the forward times out. Standard: Nielsen heuristic 9; Build Prompt §3 rule 14. Decision: UX-3; play "not saved, press 1 again", keep the keys live, and hand over after a second failure.

**F-29 · Medium · (a) · Outbound calls play once, then stay silent for two minutes.** SO-11 · coordinator and facility calls. Evidence: no replay on `PlaybackFinished` (`internal/platform/ivr/outbound.go:613-625`); `DefaultNoInput` is 2 minutes (`internal/platform/ivr/outbound.go:141-145`). Story: a coordinator woken at night catches only "…press 1", hears silence, and presses 1 to stop it. Standard: W3C VoiceXML 2.0 (noinput reprompt); sleep inertia, https://pmc.ncbi.nlm.nih.gov/articles/PMC6710480/. Decision: UX-3; replay after 10 s of silence at most twice, then hang up; an answer with no key counts as unanswered for F-01's tier rule. No voicemail on coordinator endpoints: R-14.

**F-30 · Medium · (a) · A voice alert counts as sent once placed.** RS-19 · voice alerts. Evidence: the status is `sent` when Originate succeeds (`internal/platform/ivr/alerts.go:136-147`). Story: a rider on another call is never rung again, and nobody can measure how often voice reaches anyone. Standard: Build Prompt §10.4 (each attempt logged with its result). Decision: UX-3; record answered or not answered per attempt. A retry policy for red alerts is the PI's (R-6).

**F-31 · Medium · (a) · Accepting and switching on ignore the gates.** RS-6 (gate part), RS-17 · responder menu. Evidence: `accept_alert` re-checks no gate (`db/operational/migrations/0023_review_pr10_coderabbit.sql:84-131`), and `set_availability` checks none (`db/operational/migrations/0023_review_pr10_coderabbit.sql:26-50`), while `issue_alert` requires them (`db/operational/migrations/0015_dispatch.sql:487-493`). Story: a VHT whose insurance lapsed is told every morning she is available, and is never alerted. Standard: Build Prompt §10.11. Decision: UX-3; refuse an accept that fails a gate, and show "on, but alerts are paused: contact your coordinator" (PENDING copy) when switching on while gated out.

**F-32 · Medium · (a) · The facility call never says where the patient is.** SO-13 (place part) · forwarding. Evidence: the forward call's sounds have no ward or landmark (`internal/platform/ivr/outbound.go:330-338`). Story: the SMS was lost; the in-charge answers and hears no place. Standard: NENA-STA-020.2-2026 (location passed on). Decision: UX-3; speak the ward and landmark. A second call and number recognition on the bridge are the PI's forwarding mechanics (R-9).

### UX-4: caller clarity and trust cues

**F-35 · Medium (was High) · (a) · Bridge prompts are always English, and the bridge starts with dead air.** CV-8, CV-23 · masked bridge. Evidence: `internal/platform/ivr/bridge.go:675` uses one configured language; `BridgeTarget` carries none (`internal/dispatch/store.go:163-168`); Answer and a store look-up precede any sound (`internal/platform/ivr/bridge.go:212-228`). Story: a Luganda caller hears "choose 1 for health worker" in English. Standard: ETSI EG 202 116 §11.6, §7.1.6. Decision: UX-4; carry the incident language on the target (a new migration replaces `op.bridge_targets_for_caller`) and play ringback right after Answer.

**F-36 · Medium · (a) · The invalid notice says "choose a number from the list" where there is no list.** CU-7, CV-11, CU-16 · Tool 1 free text and call-back, both channels. Evidence: `Reprompt` always uses one notice (`internal/intake/machine.go:261-266`; `config/gated/tool1.yaml:74`); the IVR prefixes one sound (`internal/platform/ivr/sounds.go:20-25`); the language-screen notice is English on USSD (`internal/intake/machine.go:264`). Story: a caller types a long place name and is told to choose from a list that is not there. Standard: Nielsen heuristic 9; ETSI EG 202 116 §7.1.6. Decision: UX-4; one notice per input kind and a bilingual notice on the language screen, PENDING copy. Keeping the first 20 characters of a long landmark is a reading of Build Prompt §10.1 for the PI (deferred).

**F-37 · Medium · (a) · The error screen is English-only, unchecked, and sends responders to the public line.** CU-10, RS-10 · USSD error screen. Evidence: `cmd/intake-gw/main.go:117-118` picks the English text for both codes; `config/gated/screens.yaml` is outside the screen gate. Story: a responder's accept fails and they are told to call the reporting line, where they may start a second incident. Standard: Nielsen heuristic 9. Decision: UX-4; one bilingual caller error text that says a redial continues, a responder error text, both length- and GSM-checked.

**F-38 · Medium · (a) · Voice-only wording has no script.** CV-18, CV-26 · prompt catalogue. Evidence: `telephony/prompts/catalogue.yaml` lists IDs only; the bridge, fallback and invalid sounds have no text. Story: the recording team records the USSD words, and callers are never told to press hash. Standard: ETSI EG 202 116 §9.5.5. Decision: UX-4; a `text: {en, lg}` per catalogue ID, PENDING until approved, checked by the catalogue test; the role words are shared by SMS and bridge.

**F-39 · Low · (a) · The reference is spoken as ten digits in one run, once.** CV-13, SO-23 · IVR, coordinator, facility and alert calls. Evidence: `internal/platform/ivr/outbound.go:708-716` keeps digits only. Story: a coordinator writing the reference at night must hold ten digits. Standard: ETSI EG 202 116 §9.5.4. Decision: UX-4; a pause at each 3-3-4 group; the IVR closing message says it twice.

**F-40 · Low · (a) · Landmark menus may have gaps, and the ward menu reaches nine options by voice.** CU-17, CV-20 · gazetteer, Tool 1 screens 6 and 7. Evidence: positions are checked for range and duplicates only (`internal/location/gazetteer.go:233-243`); `MaxWards = 8` (`internal/location/gazetteer.go:25-27`). Story: on a ward with landmarks 1, 2 and 4, a caller who learnt "the last one" presses 5. Standard: ISO 9241-110, conformity with user expectations. Decision: UX-4; require contiguous positions. A fixed escape digit and a voice ward cap are the PI's (R-10).

**F-41 · Low · (a) · Luganda "ŋ" is refused in free text and templates.** CU-18, CV-27 · Tool 1 free text, Luganda copy. Evidence: `internal/intake/machine.go:323`; `internal/config/gsm.go:6-7`. Story: a caller spells a place as Luganda does and gets the wrong error. Standard: Luganda orthography, https://en.wikipedia.org/wiki/Luganda. Decision: UX-4; map ŋ to ng' before the GSM-7 check in free text, and name ng' in the template error. The translation brief: R-8.

**F-42 · Low · (a) · Place order differs across channels, and an empty place renders as ", ".** RS-22 · responder texts. Evidence: `internal/respond/ussd.go:218-221`; `config/gated/sms.yaml:16` and `config/gated/sms.yaml:31` use opposite orders. Story: a free-text landmark gives "Go to , Kawempe I". Standard: Nielsen heuristic 4. Decision: UX-4; no empty separators. One order across channels is the PI's.

**F-43 · Medium · (a) · The top screen hides whether a responder is on duty.** RS-18 · responder menu. Evidence: `internal/respond/ussd.go:259-275`; a missed reminder is never caught up (`db/operational/migrations/0019_sms.sql:131-141`). Story: a VHT's phone is charging when the reminder fails; at 18:00 he drops out of the pool and believes he is on duty. Standard: Nielsen heuristic 1. Decision: UX-4; one line "on until 18:00" or "off duty". An SMS at expiry is a protocol addition (deferred).

### UX-5: audit trail

**F-47 · Medium · (a) · Nothing reconciles the ledger with the event log.** RR-17 · audit. Evidence: the relay drops `event_id` (`internal/audit/outbox.go:88`); `op.incident_events` has no append-only trigger. Story: an owner deletes an inconvenient escalation before it is relayed, and every verifier passes. Standard: OWASP ASVS 5.0 16.4.2; RFC 9162. Decision: UX-5: an append-only trigger on `op.incident_events`; on `op.outbox`, a trigger that refuses `DELETE` and `TRUNCATE` and every `UPDATE` except the relay's (`op.mark_outbox_delivered` sets `delivered_at` from NULL to a time, 0002), so delivery still completes; and `sautictl verify-coverage`. No job deletes from `op.outbox` on `main`; M6's retention (F-87) must relax the trigger for delivered rows only. Nightly timers and the external witness are P0 (R-16).

**F-48 · Medium · (a) · State-changing CLI commands have no confirmation and no dry run.** SO-16 · `sautictl`. Evidence: `cmd/sautictl/main.go:680-685` suspends at once. Story: an operator edits `resume` from history under pressure and runs `suspend`. Standard: clig.dev, https://clig.dev/. Decision: UX-5; a typed confirmation on a terminal, `--yes` otherwise, `gazetteer load --dry-run`.

**F-49 · Medium · (a) · Every runbook puts a database password on the command line.** SO-17 · `sautictl`. Evidence: `--dsn` is the only source (`cmd/sautictl/main.go:638`). Story: the `op_console` password lands in shell history on a shared laptop. Standard: clig.dev ("do not read secrets directly from flags"). Decision: UX-5; fall back to `SAUTI_DSN`, document `PGPASSFILE`.

**F-50 · Low · (a) · A no-op suspend says "dispatch suspended".** SO-25 · `sautictl`. Evidence: `db/operational/migrations/0016_dispatch_control.sql:85-87`; `cmd/sautictl/main.go:685`. Story: the coordinator lead's decision is in no register. Standard: clig.dev. Decision: UX-5; print "already suspended since … by …; nothing changed". An "affirmed" register row is the PI's (deferred).

### UX-6: protocol proposals behind flags

Every entry here is class (b): built behind a gated flag that defaults to today's protocol, with PENDING copy, for the PI, translation and the amendment gate.

**F-54 · Medium · (b) · The call-back screen has no "no number" exit and no read-back.** CU-8, CV-6 (part), CV-10 (part) · Tool 1 screen 8. Evidence: `internal/intake/machine.go:328-338`; `internal/platform/ivr/controller.go:292-303`. Story: a caller on a borrowed phone knows no number to give and never sees the first-aid line. Standard: NENA-STA-020.2-2026 §5.2.1; Nielsen heuristic 3. Decision: `callback_none_option`: "0 no number" records `callback: none` (an answer value, not a record kind; the DPO is informed); a voice read-back sub-step for a typed number. `NewMachine` refuses trigger screen 8 unless the option is on.

**F-55 · Medium · (b) · No repeat, back or person key, and key 3 means two things.** CV-12, RS-7, RS-8 · IVR, voice alerts, responder menu. Evidence: keys are ignored during the closing message (`internal/platform/ivr/controller.go:279`); 3 repeats on voice and opens Availability on USSD (`internal/platform/ivr/alerts.go:291-292`, `internal/respond/ussd.go:134-135`). Story: a VHT presses 3 on USSD "to see it again" and lands on Availability, where 2 means off duty. Standard: ETSI ES 202 076 Table 1a, https://www.etsi.org/deliver/etsi_es/202000_202099/202076/02.01.01_60/es_202076v020101p.pdf; ETSI ETR 329 §5.5.1.5. Decision: ADR-0029's key conventions behind `voice_keys_v2`; the menu's Availability key moves only with the PI.

**F-56 · Medium · (b) · A wrong language choice is permanent.** CU-9 · Tool 1. Evidence: `db/operational/migrations/0006_tool1_projection.sql:293`. Story: a Luganda reader presses 1 and gets English for the rest of the call and every redial. Standard: ISO 9241-110, controllability. Decision: `language_switch`: a last option on screen 2, labelled in the other language, switches once (ADR-0029 rule 5); a new migration exempts `language` from the never-change trigger.

**F-57 · Medium · (b) · The suspended closing screen carries no first-aid line.** CU-11 · Tool 1 closing screen. Evidence: `config/gated/tool1.yaml:73`. Story: while dispatch is suspended a caller with a bleeding child is given no instruction. Standard: Build Prompt §10.1. Decision: `{first_aid}` allowed in `dispatch_suspended`; MD and PI copy.

**F-58 · Medium · (b) · The closing screen does not say what happens next.** CU-12 · Tool 1 closing screen. Evidence: `config/gated/tool1.yaml:69`. Story: a caller puts the phone away and ignores two calls from an unknown number. Standard: EENA, SMS communication with PSAPs, https://eena.org/knowledge-hub/documents/sms-communication-with-psaps-and-eros-updated. Decision: one PENDING line reserved within the screen gate; ADR-0029's trust cue.

**F-59 · Low · (b) · Screen 1 does not name the free voice line.** CU-20 · Tool 1 screen 1. Evidence: `config/gated/tool1.yaml:19`. Story: a teenager dialling for a neighbour who cannot read never learns a voice line exists. Standard: WHA72.16 (toll-free access), https://apps.who.int/gb/ebwha/pdf_files/WHA72/A72_R16-en.pdf. Decision: a `{voice_line}` placeholder in the language prompt, behind the screen gate; the number is P0.

**F-60 · Medium · (b) · The caller is dropped into a masked call with no announcement.** CV-15 · masked bridge. Evidence: `internal/platform/ivr/bridge.go:521-544`. Story: a stranger says "where are you?" from an unknown number, and the caller hangs up. Standard: UK NCSC (let customers check your calls). Decision: `bridge_announce`: a short announcement to the answered leg before it joins.

**F-61 · Medium · (b) · Going off duty leaves a pending alert live for 90 s.** RS-6 (policy part) · responder menu. Evidence: `db/operational/migrations/0023_review_pr10_coderabbit.sql:26-50`. Story: a rider presses Off duty instead of Decline, and the yellow cascade waits on him. Standard: Nielsen heuristic 5. Decision: `off_duty_declines_pending`; whether it counts as a decline, and its reason code, is the PI's.

### UX-7: production refusals and security configuration

**F-62 · Medium · (a) · Production would start with placeholder telephony, no coordinator line, no caller ID or sender ID, or no Tool 1 code.** SO-19, RR-13 (part), CU-19, CV-19, RS-12 · start-up. Evidence: missing lines only warn (`cmd/ivr-controller/main.go:230-231`); `ProductionReady` covers copy only (`internal/config/config.go:174-205`); alert calls accept an empty caller ID; the sender ID is optional (`cmd/dispatch-worker/main.go:264`); an empty Tool 1 code list passes (`cmd/intake-gw/main.go:133`). Story: one Quadlet unit misses a variable at go-live; escalations are recorded and never rung. Standard: Build Prompt §3 rule 14; OWASP Top 10:2025 A02. Decision: UX-7; each process refuses production on these.

**F-63 · Medium · (a) · A dev environment can send real SMS with placeholder copy.** RR-14 · dispatch-worker. Evidence: `cmd/dispatch-worker/main.go:238-246`. Story: someone "tries it" on a field host with the live key, and responders receive PENDING text. Standard: OWASP ASVS 5.0 13.3.1. Decision: UX-7; outside production, refuse a non-sandbox AT host or user.

**F-64 · Medium · (a) · The egress allowlist trusts whatever host the environment names, over plain http.** RR-15 · egress. Evidence: `cmd/dispatch-worker/main.go:258-265`; host-only comparison (`internal/platform/egress/egress.go:23-28`). Story: a wrong URL sends responder numbers and the API key to any host. Standard: OWASP ASVS 5.0 13.2.4, 13.2.5. Decision: UX-7; a gated scheme-host-port list and `https` only. The DPO signs off the list (R-5).

**F-65 · Low · (a) · ARI over http off-host; the callback token travels in the URL path.** RR-25 · ivr-controller, USSD adapter. Evidence: `cmd/ivr-controller/main.go:138`; `internal/platform/africastalking/ussd.go:85`. Story: anyone reading proxy logs can forge callbacks. Standard: OWASP ASVS 5.0 13.3.1. Decision: UX-7; refuse a non-loopback `http://` ARI URL. A token-free HAProxy log format is P0 (R-16).

**F-66 · Medium · (a) · No dependency updates, scheduled vulnerability scan or provenance.** RR-20 · supply chain. Evidence: no `dependabot.yml`; the SBOM is a 30-day artifact (`.github/workflows/ci.yml`). Story: an advisory in a dependency is caught only when a PR happens to run the scan. Standard: NIST SSDF PS.3; SLSA v1.2 Build L2, https://slsa.dev/spec/v1.2/build-track-basics#build-l2. Decision: UX-7; Dependabot and a daily scan on `main`. Signed provenance needs Forgejo, Harbor and cosign (R-16).

### UX-8: observability, operator tools and test depth

**F-68 · Medium · (a) · Time to T2 against operator session limits is not modelled.** CU-15 · USSD. Evidence: the simulator has no clock (`internal/platform/sim/ussd.go`). Story: a slow reader at 25 s a screen runs out of session at screen 5, before any location. Standard: ITU-T FG DFS 2017 (120 s typical), https://www.itu.int/en/publications/Documents/tsb/2017-DFS-TechInnovationCompetition/files/basic-html/page192.html. Decision: UX-8; a simulator session clock defaulting to 30 s idle and 120 s total. The Uganda values are O7 (R-17).

**F-69 · Low · (a) · Runbook roles, severity levels and drills are undefined.** SO-24 · runbooks. Evidence: eight owner labels; SEV-1 to SEV-4 undefined; all runbooks "not yet drilled". Story: "technical operator on call" and "on-call engineer" are read as two pagers, or as nobody's. Standard: PagerDuty, Severity levels, https://response.pagerduty.com/before/severity_levels/. Decision: UX-8; a runbook index with a role glossary and SEV definitions. The coordinator roles are the PI's (R-1).

**F-70 · Low · (a) · `dispatch status` is human-only, and help exits 1.** SO-22 · `sautictl`. Evidence: `cmd/sautictl/main.go:89-93`, `cmd/sautictl/main.go:697-727`. Story: a monitoring check cannot read the surge counts. Standard: clig.dev. Decision: UX-8; `--json`, exit 0 on help, sorted usage.

**F-71 · Low · (a) · Internet-facing parsers are missing from fuzzing.** RR-23 · responder menu, delivery reports, phone parsing. Evidence: five fuzz targets (`scripts/ci/fuzz-smoke.sh`). Story: a crafted `text` panics the responder menu and the responder sees nothing. Standard: NIST SSDF PW.8. Decision: UX-8; four fuzz targets.

**F-72 · Low · (a) · The log scan covers one path, and the redactor garbles some UUIDs.** RR-24 · logging. Evidence: `tests/e2e/m1_intake_test.go:224`; `internal/platform/logging/redact.go:27`. Story: a future SMS error log leaks a number with no test failing. Standard: OWASP ASVS 5.0 16.2.5. Decision: UX-8; one capturing handler for every rig, and UUIDs kept intact.

**F-73 · Low · (a) · `planned:` markers never expire.** RR-26 · governance gates. Evidence: `governance/compliance_check.py:8`; `threat-model/validate_threats.py:6`. Story: the risk register shows M1 and M2 controls as pending. Standard: ISO/IEC 42001 clause 7.5. Decision: UX-8; both checkers fail on a marker for a milestone already merged.

**F-74 · Low · (a) · Caller-impacting branches have no test.** CV-29 · tests. Evidence: the bridge dialplan context, a failed playback and a Luganda call are untested. Story: CV-1 to CV-8 shipped unnoticed. Standard: ETSI EG 202 116 §9.5.4. Decision: settled by the tests of UX-1 and UX-4.

### Not planned as code: classes (c), (d), (e) and deferred (a)

| ID | Sev | Class | Sources | Finding | Decision |
|---|---|---|---|---|---|
| F-77 | Medium | (c) | SO-14 | Routine follow-ups and life-threatening escalations share one ring-until-acknowledged line; a suspension rings once per incident. | PI and Medical Director choose which reasons ring and which are tasks (R-2). TJC 2026 NPG.01.05.01 on alarm noise. |
| F-78 | Medium | (c) | RS-16 | A responder can hold two pending alerts; the menu shows only the newest. | PI sets the parallel-alert policy (R-6). |
| F-79 | Medium | (c) | RR-21 | The breach runbook gives a 72-hour clock where the DPPA says immediately; the SMS provider's processing location is unassessed. | DPO (R-5). Uganda DPPA 2019 s.23, s.19. |
| F-80 | Medium | (c) | RR-22 | Paper incident IDs for a two-site outage can collide with live references; no `manual_entry` path. | PI designs the paper form; the reserved block and entry command follow (R-7). Second routes are M7. |
| F-81 | Medium | (d) | CV-16, RS-13 | The number Sauti calls from may not route back to the bridge. | P0 numbering (R-15); UX-7 adds a configuration check. |
| F-82 | Medium (was High) | (a), deferred | CU-4 | An abandoned danger-yes call waits for the 120 s silence sweep. | Deferred until a logged Africa's Talking end-of-session payload confirms its fields (R-17). |
| F-83 | Medium | (e: M7) | RR-19 (load part) | No load or soak test. | M7, as the roadmap places it. |
| F-84 | Low | (c) | RS-25 | A responder who reported the incident is alerted to it. | PI (R-6). |
| F-85 | Low | (d) | CV-24 | Ringback uses the US cadence. | P0 tone zone (R-16). |
| F-86 | Low | (d) | RR-27 | No clock-offset alarm. | P0 (R-16). |
| F-87 | Low | (e: M6) | RR-28 | Outbox, sessions and witness notes grow without limit. | M6 retention; periods are the DPO's. |
| F-88 | Low | (e: M7) | CV-22 | Keys pressed before a prompt starts are discarded. | A deliberate trade-off (ADR-0023); revisit after the M7 field test. |
| F-89 | Low | (e: M7) | CV-21 | Options are voiced "press N, label". | Practitioner evidence only; M7 usability test. |
| F-90 | Low | (e: M7) | RS-21 | The 20 s voice alert rings over an open USSD session. | Handset behaviour unverified; M7 field test. |
| F-91 | Low | (c) | RS-23 | The colour word and clock format are fixed in code. | Translators (R-8). |
| F-92 | Low | (e: M5) | RS-24 | Stand-down messages do not say why. | Closure reasons arrive with M5's close. |

## Recommendations to the owners

These need a person, not code. Each names who decides.

**Class (c): the DPO, the PI or the operator.**
- **R-1 (PI, Medical Director, operator): name the escalation tiers.** Who is tier 2 behind the coordinator line, after how many unanswered rings (F-01's default is 3, about 6 minutes), and who is tier 3. Name roles, then put people on a rota. Also settle the runbook role glossary (F-69).
- **R-2 (PI, Medical Director): ring or task.** Which escalation reasons ring the coordinator and which go to the task list, and whether a suspension is announced once (F-77). ADR-0028's choice to ring follow-ups changes with this.
- **R-3 (PI, Medical Director): what a coordinator does before M5** when a step needs a call-back that only the console's audited reveal allows (F-09, F-08's location call-backs).
- **R-4 (DPO): staff identity.** Whether a staff register (subject UUID and role) may be kept before M5, or per-person database roles only (F-46).
- **R-5 (DPO): breach and transfer.** Reconcile the breach clock with the DPPA's "immediately" and Form 7; record where the SMS provider processes data and the s.19 basis; sign off the egress list (F-79, F-64).
- **R-6 (PI): responder policies.** Late acceptance after a lapse (F-26), parallel alerts (F-78), a retry for red voice alerts (F-30), and whether a reporter is alerted to their own incident (F-84).
- **R-7 (PI): the paper form** and a reserved reference block for a two-site outage (F-80).
- **R-8 (translators): Luganda.** Colour words and clock format (F-91), ŋ as ng' (F-41), the language label on screen 1.
- **R-9 (PI, amendment gate): the (b) flags.** Each flag in UX-6, and the out-of-area escalation flag in UX-1, ships off. Turning one on is a protocol amendment. The PI and the DPO also confirm that decision 1 covers the place before acceptance (F-53), and decide forwarding mechanics (F-32) and whether yellow and green calls with no T2 are dispatched at ward level (F-04).
- **R-10 (PI): numbers.** The SLO targets (F-67), the USSD deadline as an SLO rather than a hard cut-off (F-23), a voice ward-menu cap and a fixed escape digit (F-40).
- **R-11 (DPO): call-back lookup.** ADR-0028's proposal to let a caller reach the bridge from a typed call-back number.

**Class (d): P0 infrastructure and repository settings.**
- **R-12 (repository owner): the ruleset.** Require every CI job by name, one code-owner approval (a second account, or a documented waiver for a sole maintainer), and dismissal of stale reviews (F-76).
- **R-13 (P0): PostgreSQL and HAProxy.** `tcp_keepalives_idle=5`, `tcp_keepalives_interval=2`, `tcp_keepalives_count=3`, `tcp_user_timeout=10000`; HAProxy client and server timeouts of 30 s or less on the PostgreSQL frontends (F-15). Then pull a server's power as a drill.
- **R-14 (P0): the coordinator endpoint.** A hunt group of two or more, no voicemail diversion, and its own fallback dial string so inbound fallbacks do not queue behind escalation calls (F-01, F-02, F-29).
- **R-15 (P0, operator): numbers and IDs.** The caller ID Sauti presents must be the bridge DID, or route to the bridge context; confirm that MTN and Airtel display it; register the SMS sender ID (F-81, F-62).
- **R-16 (P0): platform.** Prometheus and Alertmanager paging on the rules UX-8 adds; `WatchdogSec` and a SIGTERM drain in the units; nightly verifier timers and the external witness; idle-transaction timeouts on human roles; per-person database roles; a token-free HAProxy log format; a Uganda tone zone; a chrony offset alarm; Forgejo, Harbor and cosign for provenance.
- **R-17 (operator): ask the providers in writing** for the MTN and Airtel Uganda USSD session, idle and response timeouts (O7); a logged Africa's Talking end-of-session payload; AT's SMS rate limits and use of 429; the carrier's DID format.

## Strengths

The audits agree on what already works, and the fixes build on it:
- **Nothing answered is lost.** Every answer is durable before the next screen, a redial resumes on either channel, and idempotency keys make every retry converge (`internal/intake/atomicity_test.go::TestEveryWriteFailureConverges`).
- **Dispatch never waits for the call-back question,** and "help is coming" is never said without T2.
- **Strict input handling.** One digit 1–9; junk re-prompts without a write; input is fuzzed; IVR keys before a prompt starts are ignored by design.
- **The screen gate** renders every prompt at its worst case in every language against every operator limit, in CI and at start-up.
- **Escalations are durable.** Coordinator calls derive from events, survive restarts, ring most severe first, and treat an unknown severity as red.
- **The bridge never reveals a number,** sets Sauti's caller ID before joining, records nothing, and hands failures to a coordinator.
- **Time comes from the database.** T-times, claims and leases use `clock_timestamp()`.
- **The single write path, the outbox and the ledger** (append-only, signed checkpoints, salted hashes) are sound; the gaps are coverage, not design.
- **Production refuses placeholders** in screen, SMS and responder copy, unapproved triage rules and development keys.
- **The runbooks share one template,** name roles not people, and refuse any improvised reveal of a caller's number.
- **CI hygiene:** actions pinned by SHA, race tests, govulncheck, OSV-Scanner, gitleaks, `sqlc diff`, archcheck and the governance gates.

## Unverified

- **Medium and Low evidence** was taken from the audits and not re-read line by line. Every Critical and High citation was checked on `main` at 1123d40.
- **P0 values** not in the repository decide how severe F-15, F-20, F-65 and F-86 are: server keepalives, HAProxy timeouts and log format, unit restart and watchdog settings, alert rules, chrony.
- **Asterisk 22 behaviour:** whether channels stay in Stasis after the WebSocket drops (F-02); whether a missing file reports `failed` after a successful `play` (F-03); whether `TIMEOUT(absolute)` fires inside Stasis.
- **Uganda carriers:** USSD session, idle and response timeouts; 0800 display as outbound caller ID; DID format; DTMF mode on the trunk; sender-ID rules.
- **Africa's Talking:** the end-of-session payload fields (F-82), SMS rate limits and 429 behaviour (F-22), processing location (F-79).
- **Handsets and people:** whether an incoming call closes a USSD dialog (F-90); whether callers recognise their parish names (F-07); Luganda ŋ on common handsets; multi-SIM prevalence.
- **Standards read in part:** ISO 9241-110 (official preview), ISO/IEC 13714 (secondary summary), ISO 22320 and ISO 27001 Annex A titles (secondary sources), NENA silent-call guidance, ITU-T E.161 and E.180.
- **Gates not run by the audits.** The governance gates were run when this document was written.
