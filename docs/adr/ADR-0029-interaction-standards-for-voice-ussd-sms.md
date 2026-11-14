---
status: accepted
date: 2026-11-13
decision-makers: Sauti engineering (repository owner)
consulted: Build Prompt §3 rules 5, 6, 14; §10.1, §10.2, §10.4, §10.5, §14; the November 2026 UX audit
informed: PI, Medical Director, Data Protection Officer, coordinators, technical operator, translators
---
# ADR-0029: Interaction standards for voice, USSD and SMS

## Context and Problem Statement

Sauti reaches people through three channels: USSD screens, voice calls (the toll-free IVR, the masked bridge, alert calls, coordinator and facility calls) and SMS. Each surface was built in its own milestone, and the November 2026 audit ([ux-audit-2026-11.md](../explanation/ux-audit-2026-11.md)) found that they disagree:
- A key means different things on different surfaces: 3 repeats a voice alert but opens Availability on the responder menu (F-55).
- Retries differ: the bridge hands a caller to a coordinator after three tries, while the IVR loops for ever on a wrong key and hangs up on silence (F-02).
- Several failure paths end in a hang-up or dead air instead of a person (F-02, F-03, F-04, F-05).
- Critical input, the place and a typed call-back number, is never read back (F-51, F-54).
- The caller's language is forgotten on the bridge (F-35).
- No text can name the number Sauti calls from (F-34).
- Time limits are set per surface with no shared budget, and escalations have one tier (F-01).

The callers are frightened, often read little, and use basic phones; the responders answer on a motorbike. A rule learnt on one surface must hold on the next. Which interaction standards does Sauti adopt across every channel, and which findings do they settle?

## Decision Drivers

* Build Prompt §3 rule 14: every automated step that errors or times out ends with a person.
* Rule 5: the caller's number is never revealed; rule 6: no patient data in messaging.
* The amendment gate: Tool 1 and the alert ladder are fixed by the protocol. A standard that changes them ships behind a flag that defaults to today's behaviour, with PENDING copy, until the PI amends the protocol.
* USSD carries input as one `*`-separated string, so `*` cannot be a key there, and `#` is the handset's send key.
* Build Prompt §10.4 fixes the voice alert keys: 1 accept, 2 decline, 3 repeat.

## Considered Options

* **One cross-channel standard, recorded once, with each rule tied to a cited source and the findings it settles.**
* Leave each surface to its own ADR (0023, 0027, 0028): rejected, because that is how the surfaces drifted apart.
* Adopt a full IVR standard (ISO/IEC 13714) wholesale: rejected; its key assignments assume a live operator on every menu and are only available here as a secondary summary.

## Decision Outcome

Chosen option: **one cross-channel standard**. Every new or changed surface follows the eight rules below, including M3's "My incident" menu and the caller confirmation call. A rule marked **(flag)** changes the protocol: it is built behind a gated flag that defaults to today's behaviour.

### 1. Keys: one meaning per key

| Key | Meaning everywhere it is offered | Voice | USSD | SMS |
|---|---|---|---|---|
| 1–9 | The numbered options of the current menu, read in order with no gaps. A choice that recurs keeps its digit: 1 accept and 2 decline, 1 yes and 2 no; an escape option ("somewhere else", "other place") is always last. | yes | yes | An SMS names a key only as the menu it points to uses it. |
| `*` | Back one step. With nothing to undo, hear the current step again. | Questions, closing messages, bridge choices, the decline-reasons list (back to the alert), number entry (clears the digits). A synonym for 3 on alert calls. | Never a key: it separates inputs. | — |
| `#` | End of number entry. Alone, it means "no other number". | Number entry only. | The handset's send key; never an option. | — |
| `0` | A person, or "none of these" where no person is reachable. | Caller-facing menus: a coordinator **(flag)**. On number entry `0` is a digit. | The call-back screen: no number **(flag)**. Responder sub-screens: back. | — |

Back is `*` on voice and `0` on USSD because USSD reserves `*`; ETSI ES 202 076 leaves `*` and `#` unstandardised. The responder menu's Availability key moves off 3 while an alert is pending only when the PI agrees, because the M3 plan and the menu use 3.

Sources: ETSI ETR 329 §5.5.1.5 (a key keeps its action through a dialogue) and §5.5.2.1 (`*` cancels or goes back one step): https://www.etsi.org/deliver/etsi_etr/300_399/329/01_60/etr_329e01p.pdf · ETSI ES 202 076 V2.1.1 Table 1a (repeat, help and transfer keep one effect in every context): https://www.etsi.org/deliver/etsi_es/202000_202099/202076/02.01.01_60/es_202076v020101p.pdf · Perrier et al. 2015 (consistent USSD navigation): https://pubs.cs.uct.ac.za/id/eprint/1049/1/perrier-dev2015.pdf

Settles: F-33, F-40, F-55; constrains F-27, F-54.

### 2. No-input and no-match, per channel

| Surface | No input | No match | After the limit |
|---|---|---|---|
| USSD (Tool 1, responder menu) | The operator ends the session; the store keeps every answer. 120 s of silence flags the incident (`abandon_after`). | Re-prompt with a notice that names the problem for that kind of input (choice, place name, number). Every re-prompt is counted. | The sweeper decides (rule 3). |
| IVR (Tool 1 by voice) | 15 s from the end of the prompt; two re-prompts. Digits already typed on a number screen are submitted if they form a number. | A notice for that kind of input; the count resets only on a valid answer. | Three no-matches, or silence once any screen after the language screen is answered: a coordinator. Silence from the first prompt: hang up (a pocket dial), recorded. |
| Bridge | 15 s from the end of the prompt or the last key, including the prompt itself. | Only offered keys are taken. | Three tries: a coordinator (unchanged). |
| Voice alert to a responder | 10 s after the options: hear the alert again once. | Only offered keys are taken. | A short closing prompt, then hang up. Nothing is recorded as an answer. |
| Coordinator and facility calls | 10 s after the prompt: hear it again, at most twice. | Only offered keys are taken. | Hang up. A call answered with no key counts as unanswered. |
| SMS | — | — | A throttled send (HTTP 408 or 429) is retried once within the send budget. |

Every voice prompt also arms a backstop of the longest prompt plus the no-input time, so a lost end-of-prompt event cannot leave silence.

Sources: ETSI EG 202 116 V1.2.2 §7.1.9 (generous time-outs; confirm before disconnecting; do not lose data): https://www.etsi.org/deliver/etsi_eg/202100_202199/202116/01.02.02_60/eg_202116v010202p.pdf · W3C VoiceXML 2.0 §2.1.4 (tapered prompts by count): https://www.w3.org/TR/voicexml20/ · ETSI ETR 329 §5.3.7 (after repeated time-outs, inform, then act).

Settles: F-02, F-29, F-33, F-36; constrains F-82.

### 3. Every failure path ends with a person

An automated step that errors, times out or exhausts its retries hands over to a coordinator and records an `incident.escalated` event on the call's incident, so the coordinator line keeps ringing even when the hand-over itself fails. A call with no incident (an unknown number on the bridge, or a voice call whose intake failed before an incident was opened) is handed over live and records nothing: an escalation needs an incident, and opening one outside Tool 1, or keeping a separate record, is the PI's and the DPO's decision. This covers:
- an intake error or timeout, an unplayable or failed prompt, the no-match limit, and silence after the caller engaged;
- a fallback dial nobody answers (a bounded retry, then a closing message, while the escalation rings);
- a caller's bridge call that is not answered, or that finds an incident with no helper yet;
- an incident with a ward but no T2, whatever its severity (`dispatch_not_started`);
- a dispatch timer overdue by the watchdog interval (`dispatch_stalled`);
- a process stopping or restarting with calls in progress (live channels are handed over);
- an escalation unanswered after the tier limit (rule 8).

A closed incident takes no events and is never rung. So a path that must reach a person never closes the incident in the same step (the out-of-area case, F-07, **flag**).

Sources: Build Prompt §3 rule 14 · NENA-STA-020.2-2026 §5.3, §5.4 (no unattended loss; re-contact abandoned and silent calls): https://cdn.ymaws.com/www.nena.org/resource/resmgr/standards/standards1/NENA-STA-020.2-2026_911CallP.pdf · EENA, silent and abandoned 112 calls: https://eena.org/knowledge-hub/documents/emergency-silent-hang-up-and-abandoned-112-calls

Settles: F-02, F-03, F-04, F-05, F-07, F-28.

### 4. Read-back of critical input

The place (ward and landmark) and a typed call-back number are critical: an error in either sends help to the wrong place or loses the only contact. Each is shown or spoken back before the closing message, in the caller's language; a spoken number is read in groups. Read-back never delays T2: dispatch starts at the trigger screen as today. Read-back changes Tool 1, so it ships behind `place_readback` and the call-back read-back flag **(flag)**.

Source: NENA-STA-020.2-2026 §5.2.1 (a verified location is repeated or confirmed back; a different call-back number is asked again).

Settles: F-51, F-54.

### 5. Language persistence

The language chosen on screen 1 belongs to the incident. Every later surface of that incident uses it: each screen, the error text, every SMS, the bridge prompts, alerts about it where the responder's language is unknown, and M3's confirmation call. Before a language is known, a text is bilingual. A caller can switch once, through a last option on screen 2 labelled in the other language **(flag)**.

Sources: ETSI EG 202 116 §11.6 (offer language selection) · GSMA AgriTech UX Design Guidebook 2025 (local languages, everyday vocabulary): https://www.gsma.com/solutions-and-impact/connectivity-for-good/mobile-for-development/wp-content/uploads/2025/10/AgriTech-UX-Design-Guidebook.pdf

Settles: F-35, F-36, F-37, F-56.

### 6. Trust cues: Sauti's number

- Every call Sauti places presents one number, `SAUTI_ARI_CALLER_ID`, and that number routes back to the bridge.
- Every text that announces a call names that number through a placeholder filled from configuration (`{sauti_number}`, `{bridge}`), never typed into copy. The responder code is `{code}`.
- SMS go out under one registered sender ID, required in production.
- No text carries a link or asks for personal details.
- A coordinator taking a handed-over call sees "SAUTI" and the reference, never the caller's number (rule 5).

Sources: UK NCSC, Business communications: SMS and telephone best practice (publicise your numbers and sender IDs; keep them few): https://www.ncsc.gov.uk/guidance/business-communications-sms-and-telephone-best-practice · Build Prompt §10.4, §10.5.

Settles: F-12, F-34, F-58, F-60, F-62, F-81.

### 7. Time budgets

| Step | Budget | Source |
|---|---|---|
| USSD screen | p99 under 1 s (Build Prompt §14). The 800 ms hard deadline stays until the PI amends the spec (R-10). | Build Prompt §14 |
| Voice feedback | A sound within 1–2 s of an answer or a key; ringback right after a bridge call is answered. | ETSI EG 202 116 §7.1.6 |
| One voice operation | 3 s | ADR-0023 |
| Abandonment flag | 120 s of silence | ADR-0009, `config/gated/intake.yaml` |
| Dispatch watchdog | 2 min after T2, or a dispatch timer overdue by 2 min | spec §7 |
| Voice alert | at 20 s, rings 30 s; cascade 90 s | ADR-0027 |
| Coordinator ring | every 2 min until key 1 | ADR-0027 |
| Tier 2 | after 3 unanswered rings (about 6 min), PENDING the PI | rule 8 |
| Tier 3 page | oldest unacknowledged escalation over 10 min, PENDING the PI | rule 8 |

A tighter budget never ends a call: it hands over (rule 3).

### 8. Escalation tiers

- **Tier 1:** the coordinator line, rung most severe first, every 2 minutes, until a coordinator presses 1. Every unacknowledged reason on the incident is announced, most urgent first; one acknowledgement covers the reasons heard.
- **Tier 2:** a backup endpoint, rung as well once an escalation has had 3 unanswered rings. A call answered with no key counts as unanswered.
- **Tier 3:** the gauge of the oldest unacknowledged escalation, exported by intake-gw (which survives an ivr-controller outage), pages the on-call engineer through P0 alerting.
- Who stands in each tier is a rota of roles that the PI and the Medical Director name (R-1). With no backup endpoint configured, production refuses to start.

Sources: PagerDuty, Being on-call ("Always have a backup schedule"; escalation timeouts of about 5 minutes): https://response.pagerduty.com/oncall/being_oncall/ · Google SRE, Being On-Call (the secondary as fall-through): https://sre.google/sre-book/being-on-call/ · Build Prompt §9 (escalations carry a tier).

Settles: F-01, F-06, F-25, F-29; constrains F-77.

### Consequences

* Good: a caller, a responder or a coordinator learns a key once. Every failure path, on every channel, reaches a person, and the escalation event keeps ringing when a hand-over fails.
* Good: M3's new flows inherit the rules instead of inventing their own.
* Bad: back is `*` on voice and `0` on USSD. That is forced by USSD's use of `*`, and the menus say which key to press.
* Bad: the hand-over on silence and the third no-match add coordinator load; tier 2 needs a second person on call. Both need the PI's agreement on the rota (R-1).
* Bad: four rules change the protocol (`0` for a person, read-back, the language switch, the out-of-area escalation). They ship off and need the amendment gate.
* Neutral: the rules add recordings and PENDING copy (notices, the closing prompt for alert calls, the hold and closing messages of the fallback, the tier-2 intro), which production refuses until approved.

### Confirmation

- The fix plan, `docs/superpowers/plans/2026-11-13-ux-hardening.md`, implements each rule; its tasks name the tests that prove them, and each phase adds those tests to `docs/traceability.md`.
- Existing proof of rules that already hold: `internal/platform/ivr/bridge_test.go::TestBridgeChoiceWithNoKeyGoesToACoordinator` (rule 2, bridge), `internal/platform/ivr/controller_test.go::TestControllerIntakeDeadlineHandsOver` (rule 3, intake timeout), `internal/intake/machine_test.go::TestEveryRenderedScreenFitsLimitAndGSM7` (rule 5, both languages fit).
- A review of any new USSD, voice or SMS surface checks it against the key table, the retry table and rule 3. The M3 plan carries this as a global constraint.
