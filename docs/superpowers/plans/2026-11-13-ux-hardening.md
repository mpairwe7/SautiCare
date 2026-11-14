# UX Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the November 2026 audit's findings before Sauti takes live calls. The order puts the most life-critical first: every failure path ends with a person, then the leaders survive a lost host, then each surface keeps one meaning, then every decision leaves a trace.

**Architecture:**
- **Fail safe through the existing path.** Every new fail-safe records an `incident.escalated` event through the single write path and rides `op.claim_coordinator_calls`. No new alerting channel.
- **Protocol changes stay off.** A change to Tool 1, the alert ladder or clinical copy is built behind a flag in `config/gated/flags.yaml` that defaults to today's protocol, with PENDING copy.
- **Interaction rules.** Every surface follows [ADR-0029](../../adr/ADR-0029-interaction-standards-for-voice-ussd-sms.md): keys, retries, read-back, language, trust cues, time budgets and tiers.

**Tech Stack:**
- Go 1.27.4 and PostgreSQL 18.6.
- The in-house ARI client and `sim.FakeARI`; `testing/synctest` for timers.
- The pinned sqlc v1.31.1 binary; `promtool` for rule tests (UX-8, CI only).
- No new Go dependency.

**Spec:**
- `docs/explanation/ux-audit-2026-11.md`: the findings F-01 to F-92, each with its evidence, standard and decision. Read it with this plan.
- `docs/adr/ADR-0029-interaction-standards-for-voice-ussd-sms.md`.
- `docs/superpowers/specs/2026-11-12-sauti-dispatch-design.md` §7 (fail safe, budgets), §10 (living docs).
- Build Prompt §3, §10.1–§10.5, §10.10, §14. The governing documents are confidential: this plan, and every file it creates, cites them by section only.

## Global Constraints

**The 14 hard constraints (Build Prompt §3), as they bind this plan:**
1. **Data in Uganda.** No new outbound host. Any egress change narrows the allowlist (UX-7).
2. **Basic phone first.** Every change works on a 2G handset by keypad and voice.
3. **Deterministic.** No model and no heuristic touches severity. Escalation reasons are fixed codes.
4. **Server-side time.** Every time that is recorded, compared or escalated on comes from `clock_timestamp()`: timer overdue checks, ring counts, escalation ages and attempt times. Go's `time.Now()` is used only for local timers and latency metrics.
5. **Never reveal the caller's number.**
   - A hand-over sets the caller ID that the coordinator sees to "SAUTI" plus the reference, with Sauti's number (F-12).
   - No metric label, log line, ledger payload or CLI output carries a number. `sautictl escalations` prints references, never numbers.
6. **No patient data on messaging apps.** No new SMS content beyond placeholders for public Sauti numbers and codes (F-34). No messaging integration.
7. **No coordinator override.** Tier 2 and the escalation list inform a person; they never dispatch.
8. **Audit carries only incident and staff IDs.** New ledger entries (UX-5) hold type, incident ID or none, staff ID or none, logic version, and the salted SHA-256 of a typed payload.
9. **Five separate stores.** Every new table is operational. Nothing changes in the analytic, linkage, audit or media schemas, apart from the ledger entries of rule 8.
10. **Consent gates the export.** Nothing is exported.
11. **Amendment gate.** New flags and gated parameters go into `config/gated/` and count toward the fingerprint. Every protocol flag ships `false`.
12. **No new record kind without the DPO.**
    - `op.coordinator_call_attempts` (UX-5) holds the attempts of an existing record, as `op.alert_attempts` does. The DPO is informed in the UX-5 PR.
    - A staff register is **not** built (F-46, R-4).
    - `callback: none` (UX-6) is an answer value, not a record kind.
13. **No access from outside Uganda.** No new endpoint. `sautictl` subcommands run on Sauti's servers.
14. **Fail safe to a human.** This plan's purpose. A path that must reach a person never closes the incident in the same step, because a closed incident is never rung.

**Interaction standards.** ADR-0029 binds every task: keys (rule 1), retries (rule 2), a person at the end (rule 3), read-back (rule 4), language (rule 5), Sauti's number (rule 6), budgets (rule 7) and tiers (rule 8).

**Clinical content is never invented.** Every new text or recording is a placeholder that production refuses:
- caller and responder copy: `PENDING-UX`; Luganda: `PENDING-TRANSLATION`;
- first-aid, triage or clinical wording: `PENDING-MD-PI`;
- new recordings are added to `telephony/prompts/catalogue.yaml` with `status: placeholder` and a `text: {en, lg}` entry (UX-4, Task 4.6).

No Build Prompt sentence is copied into copy, code or docs.

**Migrations.**
- Numbers are global. `main` has 0001–0028, and the M3 plan claims 0029–0032. This plan uses 0033–0038:

  | Number | Phase |
  |---|---|
  | 0033 | UX-1 |
  | 0034 | UX-3 |
  | 0035 | UX-4 |
  | 0036, 0037 | UX-5 |
  | 0038 | UX-6 |

  Review fixes take the next free number after 0038.
- **Collisions.** If two open PRs claim one number, the PR that merges second renumbers before it merges. A merged migration is never edited or renumbered.
- **Order.** The migrator applies every unapplied file in name order. So M3's 0029–0032 may apply after this plan's 0033. Nothing here may depend on M3's functions, or redefine one of them.
- **Two functions are never replaced.** Never `CREATE OR REPLACE op.append_incident_event`, which since 0015 wraps `op.append_event_core`. Never replace `op.append_event_core` either: M3's 0029 rebuilds it, and a later apply would silently undo this plan. So **no task adds an incident event type.** New facts that are not state changes are ledger-only records, the `op.log_control` pattern of 0016 (UX-5).
- **Function conventions.** Every function: `SECURITY DEFINER`, `SET search_path = op, pg_temp`, `REVOKE ALL … FROM PUBLIC`, least-privilege `GRANT`. A changed return type is `DROP FUNCTION` then `CREATE`, with grants restored and sqlc regenerated (`bin/sqlc generate && bin/sqlc diff`: no output).

**ADRs.** ADR-0029 is the interaction standards; M3 holds ADR-0030 to ADR-0033. This plan's new records are ADR-0034 (UX-1), ADR-0035 (UX-2), ADR-0036 (UX-5), ADR-0037 (UX-6) and ADR-0038 (UX-8).

**Escalation reasons.** UX-1 adds:
- `voice_handed_over`;
- `bridge_unanswered`;
- `dispatch_not_started`;
- `out_of_area_after_danger` (behind its flag).

Each gets a constant, a row in the registry (Task 1.1), PENDING recordings in both languages, a runbook row, and a line in `docs/reference/events.md`, all before it is emitted.

**Living docs, every phase.** Each phase ends with a docs task that updates:
- the ADRs above, or the existing ADR the phase changes, marked superseded in part;
- the runbooks: `docs/runbooks/` with `Last reviewed:`;
- the reference: `docs/reference/events.md`, `environment.md`, `configuration.md`, `cli-sautictl.md`;
- `docs/traceability.md`: one row per Review Focus case and per new test;
- `threat-model/threats.yaml`: a changed mitigation or a new `verified_by`, with no `planned:` marker for a milestone already merged;
- `governance/ai_risk_manifest.yaml`: the triage and escalation risks' `verified_by`;
- the audit's finding entries, with a status line added below the decision.

`docs/MODEL_CARD.md` changes only if a model changes; none does here.

**Gates, every PR, before pushing:**
- `python3 -m tools.freshness --check`
- `python3 governance/compliance_check.py`
- `python3 threat-model/validate_threats.py`
- `scripts/ci/unit.sh` and `scripts/ci/integration.sh`
- `scripts/ci/arch.sh` and `scripts/ci/sqlc-diff.sh`

Then poll `gh pr checks <PR>` until nothing fails or is pending, resolve every CodeRabbit finding (inline comments and review bodies), and **ask the user before merging**.

**Delivery.** One PR per phase, each under 100 files, each a working tested slice. Merge commits.

| Phase | Branch | Base | Scope (findings) | Tasks | Est. files | Migration | ADR |
|---|---|---|---|---|---|---|---|
| UX-1 | `ux-1-person-at-the-end` | `main` | F-01–F-14, F-07's escalation flag | 13 | ~70 | 0033 | 0034 |
| UX-2 | `ux-2-leader-robustness` | `main` | F-15–F-23 | 9 | ~40 | — | 0035 |
| UX-3 | `ux-3-responder-coordinator-voice` | `ux-1-…` | F-24–F-32 | 8 | ~45 | 0034 | — |
| UX-4 | `ux-4-caller-clarity-trust` | `ux-1-…` | F-33–F-43 | 9 | ~55 | 0035 | — |
| UX-5 | `ux-5-audit-trail` | `ux-1-…` | F-44, F-45, F-47–F-50 | 6 | ~35 | 0036, 0037 | 0036 |
| UX-6 | `ux-6-protocol-flags` | `ux-1-…` | F-07 (confirm), F-51–F-61 | 9 | ~60 | 0038 | 0037 |
| UX-7 | `ux-7-production-refusals` | `ux-2-…` | F-62–F-66 | 5 | ~25 | — | — |
| UX-8 | `ux-8-observability-tools` | `main` after UX-2 and UX-7 | F-67–F-74 | 8 | ~45 | — | 0038 |

**Stacking rules:**
- UX-3 to UX-6 build on UX-1's reason registry, migration 0033 and its controller changes.
- UX-7 builds on UX-2's shared await helper in the commands.
- When a base merges, rebase the next branch on `main`.
- Merge in phase order. A PR that merges out of order renumbers its migration first.

**Not in this plan:**
- **F-75, assignment release.** M3 Part A releases the assignment on the responder's final field status, with the default flagged for the PI. M5 builds the close.
- **Classes (c) and (d).** The recommendations R-1 to R-17 in the audit.
- The deferred list at the end of this plan.

## Review Focus

1. **A hand-over that itself fails.** Cases: the dialplan's dial is busy, unanswered or a placeholder; ARI `continue` errors; ivr-controller stops mid-call.
   - Expected: the incident holds an unacknowledged `voice_handed_over` escalation, so the coordinator line rings whatever the dialplan did. A failed `continue` still escalates, then hangs up.
   - Tests: Task 1.3 `TestHandOverRecordsAnEscalationEvenWhenContinueFails`; Task 1.4 `TestFallbackDialplanRetriesThenCloses`; Task 1.2 `TestShutdownHandsOverLiveCalls`.
2. **A pocket dial against a silent caller who has engaged.**
   - Expected: silence from the very first prompt hangs up (recorded and swept as today); silence after any answer past the language screen hands over. Three wrong keys hand over on any screen.
   - Tests: Task 1.3 `TestSilenceAfterAnAnswerHandsOver`, `TestSilenceFromTheFirstPromptHangsUp`, `TestThreeNoMatchesHandOver`.
3. **Escalations racing an acknowledgement, and a merge.**
   - Cases: a second reason recorded while the first is ringing; an acknowledgement in the same second; an escalated call merged into an escalated survivor.
   - Expected: one acknowledgement covers exactly the reasons announced in that call. A reason recorded after the claim rings again. The merged call's unacknowledged reason rings on the survivor.
   - Tests: Task 1.8 `TestClaimAnnouncesEveryUnacknowledgedReason`, `TestAReasonAfterTheClaimRingsAgain`; Task 1.9 `TestMergedUnacknowledgedEscalationRingsOnTheSurvivor`.
4. **Every protocol flag off keeps today's protocol.**
   - Expected: with `config/gated/flags.yaml` at its committed values, every Tool 1 screen, event batch and alert prompt is identical to `main`.
   - Tests: Task 1.11 `TestOutOfAreaFlagOffClosesAsToday`; Task 6.1 `TestEveryProtocolFlagDefaultsOff`; each UX-6 task's `…FlagOff…` case.
5. **ivr-controller restarts, or its event stream reconnects, with calls live.**
   - Expected: every channel parked in `sauti-ivr` with no worker reaches the fallback once. A channel that already has a worker is not handed over twice.
   - Tests: Task 1.2 `TestReconnectHandsOverOrphanedChannels`, `TestReconnectLeavesOwnedChannelsAlone`.

---

## Phase UX-1: every path ends with a person

**Branch** `ux-1-person-at-the-end` from `main`. **Findings** F-01 to F-14, and F-07's escalation flag. **Migration** 0033. **ADR** 0034.

### Task 1.1: The escalation-reason registry (F-11)

**Files:**
- Create: `internal/incident/reasons.go`, `internal/incident/reasons_test.go`
- Modify: `internal/platform/ivr/outbound.go:693-706` (use the registry), `internal/dispatch/plan.go:89-97`, `internal/intake/sweeper.go:16-19`, `internal/intake/watchdog.go:16`, `internal/intake/dedup.go:105`, `internal/intake/service.go:154`, `internal/dispatch/engine.go:607` (constants defined once, in `incident`)
- Test: `internal/platform/ivr/catalogue_test.go`, `docs/runbooks/no-eligible-responders.md` (the reason table)

**Interfaces:**
- Produces: `package incident`.
  - `const ReasonX = "…"`, one per code, for the 13 existing reasons and the 4 new ones (`ReasonVoiceHandedOver`, `ReasonBridgeUnanswered`, `ReasonDispatchNotStarted`, `ReasonOutOfAreaAfterDanger`).
  - `var EscalationReasons []string`, in announcement priority order: triage_rule_error, red_no_acceptance, dispatch_stalled, dispatch_error, dispatch_not_started, voice_handed_over, abandoned_after_danger_yes, abandoned_red_before_dispatch, out_of_area_after_danger, no_eligible_responder, no_responder_nearby, bridge_unanswered, merged_call_escalated, forwarded_to_facility, no_facility_acknowledged, dispatch_suspended, assigned_follow_up. The order is PENDING the PI.
  - `func ReasonRank(reason string) int`: the index, or `len(EscalationReasons)` for an unknown reason.
- The old package constants remain as aliases (`const ReasonExhausted = incident.ReasonNoEligibleResponder`) only where a test or a caller outside the package names them. M3 adds its reasons here.

- [ ] **Step 1: Write the failing test.** `TestEveryEscalationReasonHasARecordingAndARunbookRow` in `internal/platform/ivr/catalogue_test.go`. For each `incident.EscalationReasons` entry, assert:
  - `sauti/en/escalation-reason-<r>` and `sauti/lg/escalation-reason-<r>` are in the catalogue;
  - the backticked code appears in `docs/runbooks/no-eligible-responders.md`.

  Add `TestReasonRankOrdersTriageRuleErrorFirst` in `reasons_test.go`.
- [ ] **Step 2:** Run `go test ./internal/incident/ ./internal/platform/ivr/ -run 'Reason' -count=1`. Expected: FAIL (`undefined: incident.EscalationReasons`).
- [ ] **Step 3:** Implement `internal/incident/reasons.go`. Point `outbound.go`'s `escalationReasonSound` at `incident.EscalationReasons`. Add the four new recordings to the catalogue with `status: placeholder`. Add the `assigned_follow_up` row and the four new rows to the runbook's reason table.
- [ ] **Step 4:** Run the same command. Expected: PASS. Run `go test ./... -count=1`. Expected: PASS.
- [ ] **Step 5:** Commit `refactor(incident): one escalation-reason registry with recordings and runbook rows (UX-1 Task 1)`.

### Task 1.2: IVR backstop timer, reconnect sweep and shutdown hand-over (F-02: CV-5, RR-9)

**Files:**
- Modify: `internal/platform/ari/client.go` (add `ListChannels`), `internal/platform/ari/client_test.go`, `internal/platform/ivr/controller.go:53-63` (Options), `internal/platform/ivr/controller.go:114-127`, `internal/platform/ivr/controller.go:158-212`, `internal/platform/ivr/controller.go:337-362`
- Modify: `internal/platform/sim/` FakeARI (`ListChannels`), `cmd/ivr-controller/main.go:144`
- Test: `internal/platform/ivr/controller_test.go`

**Interfaces:**
- Produces:
  - `func (c *ari.Client) ListChannels(ctx context.Context) ([]ari.Channel, error)` with `type Channel struct { ID, App string }`. It reads `GET /ari/channels` and keeps `dialplan.app_name`, or `stasis` where Asterisk reports it.
  - `Options.PromptMax time.Duration` (default 30 s, as `BridgeConfig`).
  - `func (c *Controller) Adopt(ctx context.Context) error`: hands every `sauti-ivr` channel with no worker to `FallbackContext`. Called on every (re)connect, before events are handled.
  - On `ctx.Done()`, `run` calls `handOver` under `context.WithoutCancel` and a 5 s budget for each call not yet ended.

- [ ] **Step 1: Write the failing tests.**
  - `TestALostEndOfPromptStillRepromptsWithinTheBackstop` (synctest): play, drop the `PlaybackFinished`, advance `PromptMax+NoInput`. Assert a second `Play` of the same sounds.
  - `TestReconnectHandsOverOrphanedChannels`: FakeARI lists `ch9` in `sauti-ivr`, with no worker. `Adopt` gives `continues == ["ch9 sauti-ivr-fallback,s,1"]`.
  - `TestReconnectLeavesOwnedChannelsAlone`: `ch1` has a worker, so there is no continue for it.
  - `TestShutdownHandsOverLiveCalls`: cancel ctx mid-screen. Assert `Continue(ch1, FallbackContext, "s", 1)` once.
- [ ] **Step 2:** Run `go test ./internal/platform/ivr/ -run 'Backstop|Reconnect|Shutdown' -count=1`. Expected: FAIL.
- [ ] **Step 3:** Implement. `play` arms the timer at `PromptMax+NoInput` when it requests a prompt, and `PlaybackFinished` re-arms it at `NoInput`, as the bridge does. `Run` calls `Adopt` after `Events` connects. Each worker's exit path hands over unless `st.ended`.
- [ ] **Step 4:** Run the same command. Expected: PASS.
- [ ] **Step 5:** Commit `fix(ivr): a backstop for lost prompt events; hand over orphaned and live calls on reconnect and shutdown (UX-1 Task 2)`.

### Task 1.3: No-match limit, silence hand-over, and an escalation on every hand-over (F-02: CV-1, CV-2, CU-14; F-12)

**Files:**
- Modify: `internal/platform/ivr/controller.go:279-382`, `internal/intake/ivr.go` (add `HandOver`), `internal/platform/ari/client.go:231` (add `SetCallerName`)
- Test: `internal/platform/ivr/controller_test.go`, `internal/intake/ivr_test.go`

**Interfaces:**
- Produces:
  - `func (s *intake.Service) HandOver(ctx context.Context, call IVRCall, reason string) error`. It appends `incident.escalated {reason}` with the key `incident.EscalationKey(reason, "")`, scoped to the call: `handover:<CallID>`. A terminal incident is a no-op.
  - `call.invalid int`: consecutive no-matches on one question. It is reset by a valid answer and by a new question.
  - `Options.MaxNoMatch int`, default 3.
  - `func (c *ari.Client) SetCallerName(ctx context.Context, channelID, name string) error`.
- Consumes: `incident.ReasonVoiceHandedOver` (Task 1.1).

- [ ] **Step 1: Write the failing tests.**
  - `TestThreeNoMatchesHandOver`: on the language screen, keys `0`, `0`, `0`. Assert `continues == ["ch1 sauti-ivr-fallback,s,1"]`.
  - `TestSilenceAfterAnAnswerHandsOver`: answer the language screen, then fire no-input `MaxReprompts+1` times. Assert one continue and no hangup.
  - `TestSilenceFromTheFirstPromptHangsUp`: no key at all. Assert a hangup and no continue (a pocket dial).
  - `TestHandOverRecordsAnEscalationEvenWhenContinueFails`: FakeARI `Continue` returns an error. Assert `HandOver` was called with `voice_handed_over`, then a hangup.
  - `TestHandOverShowsSautiAndTheReference`: before `Continue`, assert `SetCallerID(ch1, <SAUTI_ARI_CALLER_ID>)` and `SetCallerName(ch1, "SAUTI 123 456 7890")`.
  - `TestHandOverEscalatesOncePerCall` (intake): two `HandOver` calls append one event.
  - Rename `TestControllerNoInputRepromptsThenHangsUp` to `TestSilenceFromTheFirstPromptHangsUp`.
- [ ] **Step 2:** Run `go test ./internal/platform/ivr/ ./internal/intake/ -run 'NoMatch|Silence|HandOver' -count=1`. Expected: FAIL.
- [ ] **Step 3:** Implement.
  - `answer` compares the returned prompt's `Notice` and `Kind` with the shown kind to count no-matches.
  - `noInput` hands over when `st.turn.Call.Shown != intake.KindLanguage` or any answer was given.
  - `handOver` calls `HandOver` first, under its own op budget; a failure is logged, never fatal. Then it sets the caller ID and name, then `Continue`.
  - `Controller` gains a `callerID string` option, read from `SAUTI_ARI_CALLER_ID` in `cmd/ivr-controller/main.go`.
- [ ] **Step 4:** Run the same command. Expected: PASS.
- [ ] **Step 5:** Commit `fix(ivr): three no-matches or engaged silence reach a coordinator; every hand-over escalates (UX-1 Task 3)`.

### Task 1.4: The fallback dialplan retries, then closes; '+' DIDs match (F-02: CV-3, SO-15; F-13)

**Files:**
- Modify: `telephony/asterisk/extensions.conf:13-45`, `telephony/prompts/catalogue.yaml` (`sauti/fallback-hold`, `sauti/fallback-closing`: bilingual, placeholder)
- Test: `telephony/asterisk/dialplan_test.go`

**Interfaces:**
- Produces: the `sauti-ivr-fallback` context.
  - It tries `Dial(${SAUTI_COORDINATOR_DIAL},30)` up to 3 times.
  - Between tries it plays `sauti/fallback-hold`, and only when `DIALSTATUS` is `BUSY`, `NOANSWER`, `CHANUNAVAIL` or `CONGESTION`.
  - It then plays `sauti/fallback-closing` and hangs up.
  - Both inbound contexts gain `exten => _+X.` with the same priorities as `_X.`.

- [ ] **Step 1: Write the failing tests.**
  - `TestFallbackDialplanRetriesThenCloses`: parse the context. Assert a `DIALSTATUS` branch, a loop bounded by 3, and a `Playback(sauti/fallback-closing)` before `Hangup()`.
  - `TestInboundContextsMatchPlusPrefixedDIDs`: both contexts.
  - `TestBridgeInboundFallsBackOnStasisFailure`: CV-29's missing case.
- [ ] **Step 2:** Run `go test ./telephony/... -run 'Fallback|Inbound|BridgeInbound' -count=1`. Expected: FAIL.
- [ ] **Step 3:** Edit the dialplan. The escalation from Task 1.3 is what guarantees a person; the closing message tells the caller that a coordinator will call back. Its copy is PENDING.
- [ ] **Step 4:** Run the same command. Expected: PASS.
- [ ] **Step 5:** Commit `fix(dialplan): bounded coordinator retries, a closing message, '+' DIDs (UX-1 Task 4)`.

### Task 1.5: A failed playback hands over (F-03)

**Files:**
- Modify: `internal/platform/ari/client.go:296-330` (decode `playback.state`), `internal/platform/ivr/controller.go:234-244`, `internal/platform/ivr/bridge.go` (the prompt-finished branch), `internal/platform/ivr/alerts.go:228-240`, `internal/platform/ivr/outbound.go:613-625`, `docs/runbooks/bridge-failing.md:28`
- Test: `internal/platform/ari/client_test.go`, `internal/platform/ivr/controller_test.go`, `internal/platform/ivr/bridge_test.go`

**Interfaces:**
- Produces: `ari.Event.PlaybackState string`, holding `"done"`, `"failed"` or `""`.
- A `failed` playback is handled like a `Play` error:
  - on a question, `handOver` (IVR) or `toCoordinator` (bridge);
  - on a closing message, hang up;
  - on an alert or outbound call, hang up, and the attempt is settled `failed`.

- [ ] **Step 1: Write the failing tests.**
  - `TestDecodePlaybackFailedState`: decode `{"type":"PlaybackFinished","playback":{"id":"p","state":"failed"}}`.
  - `TestAFailedQuestionPromptHandsOver` (controller).
  - `TestAFailedBridgePromptGoesToACoordinator` (bridge).
- [ ] **Step 2:** Run `go test ./internal/platform/ari/ ./internal/platform/ivr/ -run 'Failed' -count=1`. Expected: FAIL.
- [ ] **Step 3:** Implement, and correct the runbook line to the real log message.
- [ ] **Step 4:** Run the same command. Expected: PASS.
- [ ] **Step 5:** Commit `fix(ivr): a failed playback is a failure, not silence (UX-1 Task 5)`.

### Task 1.6: The bridge sends an unanswered or unassigned caller to a coordinator (F-02: CV-9, RS-14)

**Files:**
- Modify: `internal/platform/ivr/bridge.go:178-195`, `internal/platform/ivr/bridge.go:225-235`, `internal/platform/ivr/bridge.go:567-579`, `internal/dispatch/store.go` (add `BridgeStore.Escalate`), `internal/platform/pg/opstore/bridge.go`, `db/operational/queries/incidents.sql` (`OpenIncidentForCaller`)
- Test: `internal/platform/ivr/bridge_test.go`, `internal/platform/pg/opstore/bridge_integration_test.go`

**Interfaces:**
- Produces:
  - `BridgeStore.OpenIncidentForCaller(ctx, macs [][]byte) (uuid.UUID, bool, error)`: an open incident with a session from these numbers, assigned or not.
  - `BridgeStore.Escalate(ctx, incidentID uuid.UUID, reason, scope string, every time.Duration) error`: appends `incident.escalated` once per scope (and, with `every > 0`, once per window of that length on the database clock) through `op.dispatch_escalate`, which `op_ivr` may already call. If the grant is missing, migration 0033 adds it.
- Behaviour:
  - A caller (`by == caller`) whose other leg rings out or is destroyed unanswered goes to `toCoordinator`, and `bridge_unanswered` is escalated.
  - An unknown number hears `bridge-no-assignment`; "press 0" then takes it to `toCoordinator` (ADR-0029 rule 1). This is rate-limited by the existing three-tries rule. It has no incident, so nothing is escalated: the live dial is its only path, and a failed `Continue` hangs up as today (ADR-0029 rule 3). A number whose open report has nobody assigned hears exactly the same prompt, and its incident is escalated `bridge_unanswered` quietly, off the call's path, at most once per 10 minutes on the database clock.
  - A responder's no-answer is unchanged.

- [ ] **Step 1: Write the failing tests.**
  - `TestBridgeCallerNoAnswerGoesToACoordinator`: assert `continued == ["in-1→sauti-ivr-fallback"]` and one `Escalate(…, "bridge_unanswered")`.
  - `TestBridgeOpenIncidentIsIndistinguishableFromAnUnknownNumber`: the same prompt and keys as an unknown number, and one quiet `bridge_unanswered`.
  - `TestBridgeUnknownNumberCanPressZeroForACoordinator`.
  - `TestBridgeUnknownNumber` stays green: nothing is revealed and no escalation is written.
- [ ] **Step 2:** Run `go test ./internal/platform/ivr/ -run 'BridgeCaller|BridgeUnknown|BridgeOpen' -count=1`. Expected: FAIL.
- [ ] **Step 3:** Implement. Add the query and run `bin/sqlc generate && bin/sqlc diff` (expected: no output).
- [ ] **Step 4:** Run the same command, then `go test -tags integration ./internal/platform/pg/opstore/ -run Bridge -count=1`. Expected: PASS.
- [ ] **Step 5:** Commit `fix(bridge): a caller who reaches nobody reaches a coordinator (UX-1 Task 6)`.

### Task 1.7: An answered alert call repeats once, then closes (F-02: RS-1)

**Files:**
- Modify: `internal/platform/ivr/alerts.go:58-75`, `internal/platform/ivr/alerts.go:228-246`, `telephony/prompts/catalogue.yaml` (`alert-no-input-closing`, PENDING)
- Test: `internal/platform/ivr/alerts_test.go`

**Interfaces:**
- Produces: `AlertCallsConfig.NoInput time.Duration` (default 10 s); `alertCall.replays int`.
  - When `alert-options` finishes with no key, the timer is armed.
  - The first expiry replays `alertSounds`.
  - The second plays `alert-no-input-closing` and hangs up through `hangupOn`.
  - No accept or decline is written.

- [ ] **Step 1: Write the failing test.** `TestAnUnansweredAlertCallRepeatsThenHangsUp` (synctest):
  - the second play equals the first;
  - after the second expiry, the last play is `alert-no-input-closing`, then `Hangup`;
  - `AcceptAlert` and `DeclineAlert` are never called.
- [ ] **Step 2:** Run `go test ./internal/platform/ivr/ -run UnansweredAlert -count=1`. Expected: FAIL.
- [ ] **Step 3:** Implement, using the same `time.AfterFunc` and generation pattern as `outbound.go`.
- [ ] **Step 4:** Run the same command. Expected: PASS.
- [ ] **Step 5:** Commit `fix(alerts): an answered alert call times out instead of holding the trunk (UX-1 Task 7)`.

### Task 1.8: Every unacknowledged reason is announced; tier 2 and the dead-man gauge (F-01, F-06; migration 0033)

**Files:**
- Create: `db/operational/migrations/0033_escalation_continuity.sql`, `internal/platform/pg/opstore/coordinator_integration_test.go` (or extend the existing coordinator test file)
- Modify: `db/operational/queries/dispatch.sql` (`ClaimCoordinatorCalls`, `OldestUnacknowledgedEscalation`), `internal/platform/ivr/outbound.go:365-470`, `internal/platform/ivr/outbound.go:628-636`, `internal/config/config.go` (`Dispatch.Escalation`), `config/gated/dispatch.yaml`, `cmd/ivr-controller/main.go:216-231`, `cmd/intake-gw/main.go:154-171`
- Test: `internal/platform/ivr/outbound_test.go`

**Interfaces:**
- Produces, in migration 0033:
  - `DROP FUNCTION op.claim_coordinator_calls(interval, int)`, then `CREATE` it with the same arguments returning `(incident_id uuid, seq bigint, reasons text[], roles text[], reference text, calls int, severity text)`.
  - `reasons` is every distinct `incident.escalated` reason with `seq > acked_seq`, ordered by `seq`. `seq` is the newest of them.
  - `roles[i]` is `payload->>'role'` of `reasons[i]`'s newest event, or `''`. UX-3 speaks it, and returning it now avoids a second `DROP`.
  - Grants are restored to `op_ivr`. The 0022 body is otherwise unchanged.
- Produces, in Go:
  - `CoordinatorCall.Reasons []string`, played in `incident.ReasonRank` order.
  - `CoordinatorCallsConfig.BackupEndpoint string` and `BackupAfterCalls int`, from `config/gated/dispatch.yaml` `escalation: {backup_after_calls: 3}` (PENDING the PI).
  - When `calls >= BackupAfterCalls` and `BackupEndpoint != ""`, `RunOnce` originates to both endpoints. The first press of 1 on either acknowledges, and the other leg is hung up.
  - A call answered and never keyed counts as a ring (it already increments `calls`).
  - `OldestUnacknowledgedEscalation(ctx) (seconds float64, count int, err error)`.
  - intake-gw's watchdog leader sets the gauges `sauti_escalations_unacknowledged` and `sauti_escalation_oldest_unacknowledged_seconds` every pass.
- Consumes: `incident.ReasonRank` (Task 1.1).

- [ ] **Step 1: Write the failing tests.**
  - `TestClaimAnnouncesEveryUnacknowledgedReason` (integration): record `triage_rule_error` (seq a), then `no_eligible_responder` (seq b). One claim returns both, with `seq = b`. After acknowledging b, nothing is claimed.
  - `TestAReasonAfterTheClaimRingsAgain` (integration): claim, record `dispatch_stalled`, acknowledge the claimed seq. The next claim returns `dispatch_stalled`.
  - `TestCoordinatorCallSpeaksReasonsInPriorityOrder`: reasons `[no_eligible_responder, triage_rule_error]` play the triage-rule-error sound first.
  - `TestBackupLineRingsAfterThreeUnansweredCalls` (synctest): the fake store returns `Calls: 3`. Assert originates to both endpoints. With `Calls: 2`, assert the primary only.
  - `TestOldestUnacknowledgedEscalationGauge` (integration).
- [ ] **Step 2:** Run `go test ./internal/platform/ivr/ -run 'Coordinator|Backup' -count=1`, then `go test -tags integration ./internal/platform/pg/opstore/ -run 'Claim|Oldest' -count=1`. Expected: FAIL.
- [ ] **Step 3:** Write the migration, regenerate (`bin/sqlc diff`: no output), and implement. `SAUTI_COORDINATOR_BACKUP_DIAL` is read in `cmd/ivr-controller/main.go`. Its absence only warns here; UX-7 refuses it in production.
- [ ] **Step 4:** Run both commands. Expected: PASS.
- [ ] **Step 5:** Commit `feat(escalation): announce every unacknowledged reason; tier-2 line; dead-man gauges (UX-1 Task 8)`.

### Task 1.9: A merged call's unacknowledged escalation rings on the survivor (F-06: SO-3)

**Files:**
- Modify: `internal/intake/dedup.go:100-135`, `internal/intake/service.go` (the projection fields used by `Consequences`), `db/operational/queries/incidents.sql` (`HasUnacknowledgedEscalation`)
- Test: `internal/intake/dedup_test.go`, `internal/intake/merge_test.go`

**Interfaces:**
- Produces: `Consequences` escalates the survivor with `merged_call_escalated` and payload `{from_reason}` whenever the absorbed call has an escalation with `seq > acked_seq`, whether or not the survivor was ever escalated.
  - The key is `incident.EscalationKey(ReasonMergedCallEscalated, <absorbed id>)`, so one merge gives one escalation.
  - `DedupReader.HasUnacknowledgedEscalation(ctx, id) (reason string, ok bool, err error)`.

- [ ] **Step 1: Write the failing test.** `TestMergedUnacknowledgedEscalationRingsOnTheSurvivor`: the survivor is escalated and acknowledged; the absorbed call holds an unacknowledged `triage_rule_error`. Assert one `merged_call_escalated {from_reason: triage_rule_error}` on the survivor.
- [ ] **Step 2:** Run `go test ./internal/intake/ -run MergedUnacknowledged -count=1`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `go test ./internal/intake/ -count=1`. Expected: PASS.
- [ ] **Step 5:** Commit `fix(dedup): a merge never silences an unacknowledged escalation (UX-1 Task 9)`.

### Task 1.10: The sweeper escalates no-T2 incidents and survives a poison incident; the watchdog sees overdue timers (F-04, F-05, F-10)

**Files:**
- Modify: `internal/intake/sweeper.go:20-110`, `internal/intake/watchdog.go:25-70`, `db/operational/queries/incidents.sql:91-103` (`ListIdle` adds `ward`, `landmark`, `last_screen`), `db/operational/queries/incidents.sql:144-175` (`ListOverdueDispatch`), `internal/platform/pg/opstore/` (the adapters), `internal/intake/service.go:485-509` (export `CatchUp`), `cmd/intake-gw/main.go`
- Test: `internal/intake/sweeper_test.go`, `internal/intake/watchdog_test.go`, `internal/platform/pg/opstore/idle_integration_test.go`

**Interfaces:**
- Produces:
  - `Idle` gains `Ward`, `Landmark string` and `LastScreen int`.
  - `type CatchUpper interface { CatchUp(ctx context.Context, id uuid.UUID) (dispatched bool, err error) }`, implemented by `*Service`, which wraps `catchUp`. `NewSweeper(r, a, c CatchUpper, idleFor)`.
  - For each idle, undispatched incident, `RunOnce` calls `CatchUp` first: a recorded landmark gets T2 exactly as a redial would. Then `escalation(in)` returns:
    - the existing reasons for a danger yes or red;
    - else `dispatch_not_started` when `!in.Dispatched` and `in.Ward != ""`;
    - else `""`.
  - Whether to dispatch at ward level instead is the PI's choice (R-9). Not built.
  - `StalledReader.ListOverdueDispatch(ctx, after) ([]Overdue, error)`, with `type Overdue struct { ID uuid.UUID; DueAt time.Time }`. It lists `op.dispatch_timers` rows where `due_at < clock_timestamp() - after` and `lease_until` is null or past, on `dispatching` incidents. The watchdog escalates each `dispatch_stalled` with the key `incident.EscalationKey("dispatch_stalled", <due_at unix>)`.
  - Both loops log and count a failed incident, `continue`, and return `errors.Join` at the end.

- [ ] **Step 1: Write the failing tests.**
  - `TestSweeperEscalatesALocatedYellowCallWithoutT2`: `Idle{Severity: yellow, Danger: "no", Ward: "w01"}` with a catch-up that does not dispatch gives `dispatch_not_started`.
  - `TestSweeperRecordsT2ByCatchUpWhenTheLandmarkIsKnown`: the catch-up returns dispatched, so there is no escalation.
  - `TestSweeperBeforeTheWardStillOnlyFlags`: `Idle{Severity: green}` with no ward gives the flag only.
  - `TestOneFailingIncidentDoesNotStopTheSweep`: the appender fails for the first ID. The second is still escalated, and the error is returned.
  - `TestWatchdogEscalatesAnOverdueTimerAfterT3` (integration): T3 set, the timer due 3 minutes ago, no lease. One `dispatch_stalled`.
  - `TestOneFailingIncidentDoesNotStopTheWatchdog`.
- [ ] **Step 2:** Run `go test ./internal/intake/ -run 'Sweeper|Watchdog|Failing' -count=1`. Expected: FAIL.
- [ ] **Step 3:** Implement, and run `bin/sqlc generate && bin/sqlc diff` (no output).
- [ ] **Step 4:** Run the same command, then `go test -tags integration ./internal/platform/pg/opstore/ -run 'Idle|Overdue' -count=1`. Expected: PASS.
- [ ] **Step 5:** Commit `fix(intake): no-T2 incidents reach a coordinator; overdue dispatch timers are stalls; safety nets skip a poison incident (UX-1 Task 10)`.

### Task 1.11: Out-of-area after a danger yes escalates, behind a flag (F-07, part)

**Files:**
- Modify: `config/gated/flags.yaml`, `internal/config/config.go:115` (`Flags`), `internal/intake/service.go:433-443`, `internal/platform/wiring/intake.go`
- Test: `internal/intake/service_test.go`, `internal/config/config_test.go`

**Interfaces:**
- Produces: `Flags.OutOfAreaEscalateAfterDanger bool` (`out_of_area_escalate_after_danger: false`).
  - When it is on and `p.Danger == "yes" || p.Severity == red`, `apply` appends `flagged{out_of_area}` and `escalated{out_of_area_after_danger}`, and does **not** append `closed`.
  - The caller still sees the out-of-area screen with 999 and 112.
  - A coordinator closes the incident in M5. Until then, the escalation list shows it (Task 1.12).
  - When the flag is off, the batch is unchanged.

- [ ] **Step 1: Write the failing tests.**
  - `TestOutOfAreaAfterDangerEscalatesWhenFlagged`: walk `1,1,…,<elsewhere>` with the flag on. The incident is open, flagged and escalated, with no `closed` event. A retried callback gives no second escalation.
  - `TestOutOfAreaFlagOffClosesAsToday`.
  - The config test: an unknown flag is still refused.
- [ ] **Step 2:** Run `go test ./internal/intake/ ./internal/config/ -run 'OutOfArea|Flag' -count=1`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run the same command. Expected: PASS.
- [ ] **Step 5:** Commit `feat(intake): out-of-area after a danger yes can escalate instead of closing, flag off (UX-1 Task 11)`.

### Task 1.12: `sautictl escalations` (F-08)

**Files:**
- Create: `cmd/sautictl/escalations.go`, `cmd/sautictl/escalations_integration_test.go`
- Modify: `cmd/sautictl/main.go:61-74`, `db/operational/queries/incidents.sql` (`ListOpenEscalations`, `ListLocationCallbacks`)

**Interfaces:**
- Produces: `sautictl escalations --dsn <op_readonly DSN> [--json]`. With no `--dsn`, it reads `SAUTI_DSN`; UX-5 Task 5.5 extends this to every command.
  - One row per open incident with unacknowledged escalations: grouped reference, severity, reasons in rank order with their roles, age of the oldest, rings, last ring.
  - Sorted by severity (unknown counts as red), then age.
  - A second section, `tasks`, lists open incidents flagged `needs_location_callback`.
  - No number and no free text. Exit 0.

- [ ] **Step 1: Write the failing test.** `TestEscalationsListsOnlyUnacknowledged`: one acknowledged and one unacknowledged escalation, and one `needs_location_callback` incident. The text output holds the second reference and its severity, and not the first. `--json` parses into `{escalations: [...], tasks: [...]}`.
- [ ] **Step 2:** Run `go test -tags integration ./cmd/sautictl/ -run Escalations -count=1`. Expected: FAIL.
- [ ] **Step 3:** Implement as one SQL statement each, readable by `op_readonly`.
- [ ] **Step 4:** Run the same command. Expected: PASS.
- [ ] **Step 5:** Commit `feat(sautictl): escalations and location call-back tasks for the pre-M5 coordinator (UX-1 Task 12)`.

### Task 1.13: UX-1 docs: ADR-0034, runbooks, reference, traceability, threats (F-09, F-14)

**Files:**
- Create: `docs/adr/ADR-0034-every-failure-path-ends-with-a-person.md`; index it in `docs/adr/README.md`.
- Modify: `docs/runbooks/ivr-controller-down.md`, `no-eligible-responders.md`, `suspension-and-resumption.md`, `abandoned-danger-callback.md`, `gazetteer-update.md`, `voice-alerts-failing.md`, `bridge-failing.md`.
- Modify: `docs/reference/events.md`, `environment.md` (`SAUTI_COORDINATOR_BACKUP_DIAL`), `configuration.md` (`flags.yaml`, `escalation.backup_after_calls`), `cli-sautictl.md`, `ussd-at-adapter.md:40`.
- Modify: `docs/adr/ADR-0027-channels-and-the-alert-ladder.md` and `ADR-0028-masked-bridge-and-surge.md` (front matter: superseded in part by ADR-0034). `docs/traceability.md`, `threat-model/threats.yaml`, `governance/ai_risk_manifest.yaml`.
- Test: create `tools/freshness/` docs-phrase check (or `governance/tests/test_runbooks.py`): `test_no_stale_escalation_phrases`.

**ADR-0034 (MADR 4.0, the four mandatory sections plus Decision Drivers and Considered Options) decides:**
- every hand-over of a call with an incident escalates (`voice_handed_over`); a call with no incident is handed over live and records nothing;
- three no-matches, and engaged silence, hand over;
- the bridge sends a caller who reaches nobody to a coordinator;
- the sweeper escalates any located incident without T2;
- the watchdog treats an overdue timer as a stall;
- one claim announces every unacknowledged reason;
- tier 2 rings after `backup_after_calls`, and intake-gw exports the dead-man gauges;
- out-of-area escalation is a flag, off.

Its Confirmation lists the tests of Tasks 1.2 to 1.12.

**Runbooks:**
- Delete the three stale paragraphs (`only a recorded event`, `arrives with M2 Part B`, `escalation voice call (M2)`).
- `ivr-controller-down.md` Impact: "Also stopped: coordinator escalation calls, responder voice alerts (SMS still goes), forwarding calls and the masked bridge." Mitigation step 1: tell the coordinator lead by voice and run `sautictl escalations` every minute.
- Every step that needs the M5 reveal reads "Not possible before M5:" with the interim action (R-3 pending).
- The gazetteer runbook restarts ivr-controller too.
- Add the reason rows from Task 1.1.

- [ ] **Step 1:** Write `test_no_stale_escalation_phrases`. It fails on the three phrases, and on "call the caller back" in a step without "M5". Run `python3 -m pytest governance/tests -k stale -q`. Expected: FAIL.
- [ ] **Step 2:** Write the docs. Fix `docs/reference/ussd-at-adapter.md:40` to `internal/intake/machine_test.go::TestEveryRenderedScreenFitsLimitAndGSM7`.
- [ ] **Step 3:** Run the gates. Expected: `freshness: OK`, both governance checks OK, and the docs test passes.
- [ ] **Step 4:** Commit `docs: ADR-0034, runbooks, reference and traceability for UX-1 (UX-1 Task 13)`. Push and open the PR against `main`. Poll `gh pr checks` until nothing fails or is pending, resolve every CodeRabbit finding, and **ask the user before merging**. In the PR, flag for the PI: the reason order, `backup_after_calls`, `dispatch_not_started` versus ward-level dispatch, and the out-of-area flag.

---

## Phase UX-2: leader and connection robustness

**Branch** `ux-2-leader-robustness` from `main` (independent of UX-1). **Findings** F-15 to F-23. **ADR** 0035. No migration.

### Task 2.1: Server-side keepalives on every leader connection (F-15)

The audit's proposed fix (client keepalive settings in the DSN) makes the *client* notice a dead server. The Critical case is the reverse: the leader's host dies, and the *server's* backend keeps the lock. PostgreSQL's `tcp_keepalives_idle`, `tcp_keepalives_interval`, `tcp_keepalives_count` and `tcp_user_timeout` are session settings that apply to the server's end of the connection. The client can set them in the startup packet. They work for direct connections. Behind HAProxy, the server's peer is the proxy, so HAProxy's own timeouts must also be set (P0, R-13).

**Files:**
- Modify: `internal/platform/pg/pg.go:60-70`, `internal/platform/pg/leader.go` (the lock connection uses the same runtime parameters)
- Test: `internal/platform/pg/pg_integration_test.go` (or the existing leader integration test file)

**Interfaces:**
- Produces: `pg.RuntimeParams() map[string]string`, which returns `tcp_keepalives_idle=5`, `tcp_keepalives_interval=2`, `tcp_keepalives_count=3` and `tcp_user_timeout=10000`, beside the existing `application_name`, `statement_timeout` and `idle_in_transaction_session_timeout`. Both the pool and `RunAsLeader`'s dedicated connection apply them.

- [ ] **Step 1: Write the failing test.** `TestLeaderConnectionCarriesServerKeepalives` (integration): inside `RunAsLeader`, `SHOW tcp_keepalives_idle` on the lock connection gives `5`, and `SHOW tcp_user_timeout` gives `10s`.
- [ ] **Step 2:** Run `go test -tags integration ./internal/platform/pg/ -run Keepalives -count=1`. Expected: FAIL (`2h` or `0`).
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run the same command. Expected: PASS.
- [ ] **Step 5:** Commit `fix(pg): leader connections set server-side keepalives so a dead host frees its lock (UX-2 Task 1)`.

The black-hole takeover proof needs a dropped route, not a userland proxy, because a proxy's kernel still answers keepalive probes. It goes into the P0 drill in `docs/runbooks/dispatch-worker-down.md` (Task 2.9): pull a leader host's power and time the takeover (expected under 30 s direct, or under the HAProxy timeout).

### Task 2.2: One shutdown helper; intake-gw and ivr-controller release their locks (F-16)

**Files:**
- Create: `internal/platform/lifecycle/await.go`, `internal/platform/lifecycle/await_test.go`
- Modify: `cmd/dispatch-worker/main.go:188-193` (use the helper), `cmd/intake-gw/main.go:70-185`, `cmd/ivr-controller/main.go:125-248`, `tools/archcheck/rules.go` (allow `cmd` to import `lifecycle`)

**Interfaces:**
- Produces: `func lifecycle.Await(ctx context.Context, stop context.CancelFunc, timeout time.Duration, waits ...<-chan error) error`. It cancels, then waits for every leader and loop, at most `timeout`, before the caller closes the pool. This is the body of dispatch-worker's `await`, moved.

- [ ] **Step 1: Write the failing tests.**
  - `TestAwaitStopsThenWaitsForEveryLeader`.
  - `TestIntakeGatewayReleasesItsLockWhenTheListenerFails` (integration, `cmd/intake-gw`): bind the public port first, so `run` returns. Assert it exits within 10 s, and that `pg_locks` no longer holds 7347003.
  - The same test for ivr-controller with 7347005.
- [ ] **Step 2:** Run `go test ./internal/platform/lifecycle/ -count=1`, then `go test -tags integration ./cmd/intake-gw/ ./cmd/ivr-controller/ -run ReleasesItsLock -count=1`. Expected: FAIL.
- [ ] **Step 3:** Implement. Every `RunAsLeader` goroutine reports on a channel that `Await` joins.
- [ ] **Step 4:** Run both commands. Expected: PASS.
- [ ] **Step 5:** Commit `fix(cmd): every process stops its leaders before closing the pool (UX-2 Task 2)`.

### Task 2.3: Unique admin ports and one variable name (F-17)

**Files:** Modify `cmd/outbox-relay/main.go:15` and `cmd/outbox-relay/main.go:153` (`SAUTI_ADMIN_ADDR`, default `127.0.0.1:9103`); keep `SAUTI_HTTP_ADDR` for a release as a deprecated fallback, with a warning. Modify `internal/respond/ussd.go:76` and `internal/platform/channels/sms.go:125` (one `eat` zone, exported from `internal/config`). Create `cmd/ports_test.go`.
- [ ] **Step 1:** Write `TestAdminPortDefaultsAreUnique`. It parses every `cmd/*/main.go` default address constant and fails on a duplicate. Run `go test ./cmd/ -run Ports -count=1`. Expected: FAIL (`9101` twice).
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run the same command. Expected: PASS.
- [ ] **Step 4:** Commit `fix(cmd): unique admin ports, one admin variable, one time zone (UX-2 Task 3)`.

### Task 2.4: Migrations bound their lock waits (F-18)

**Files:** Modify `internal/platform/pg/migrate/migrate.go:141-150`, `cmd/sautictl/main.go:108`. Test `internal/platform/pg/migrate/migrate_integration_test.go`.

**Interfaces:**
- Each file runs `SET LOCAL lock_timeout = '3s'` and `SET LOCAL statement_timeout = '60s'` first.
- A `lock_not_available` error (55P03) is retried up to 5 times with doubling backoff from 1 s, then fails, naming the blocking pid.
- `-- migrate:no-transaction` is **not** added (no current need; deferred).

- [ ] **Step 1:** Write `TestMigrationGivesUpOnALockInsteadOfQueueingWrites`. A second connection holds `BEGIN; SELECT … FROM op.incidents FOR SHARE`. A test migration ALTERs `op.incidents`. Assert that it fails in under 40 s, and that a third connection's insert into `op.incident_events` never waits more than 3 s. Run `go test -tags integration ./internal/platform/pg/migrate/ -run LockInstead -count=1`. Expected: FAIL (it blocks).
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run the same command. Expected: PASS.
- [ ] **Step 4:** Commit `fix(migrate): lock and statement timeouts with bounded retries (UX-2 Task 4)`.

### Task 2.5: The ARI event stream is pinged (F-19)

**Files:** Modify `internal/platform/ari/client.go:332-360`. Test `internal/platform/ari/client_test.go`.
- `Events` runs `conn.Ping(ctx)` every 10 s with a 5 s timeout. On failure it calls `conn.CloseNow()`, so `connected` drops and the loop reconnects.
- [ ] **Step 1:** Write `TestHalfOpenEventStreamIsDetected`: a fake ARI accepts the WebSocket, then stops answering without closing. `EventsConnected()` is false within 20 s. Use synctest where the transport allows; otherwise a 25 s real-time test behind `-short` skip. Run `go test ./internal/platform/ari/ -run HalfOpen -count=1`. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run the same command. Expected: PASS.
- [ ] **Step 4:** Commit `fix(ari): ping the event stream; a half-open socket reconnects (UX-2 Task 5)`.

### Task 2.6: A stalled leader steps down (F-20)

**Files:** Create `internal/platform/lifecycle/stall.go` and `stall_test.go`. Modify `cmd/dispatch-worker/main.go:300-330`.

**Interfaces:** `func lifecycle.StepDownOnStall(ctx context.Context, lastPass func() time.Time, limit time.Duration, run func(context.Context) error) error`. It runs `run`, and returns `ErrStalled` once `lastPass` is older than `limit`, which cancels `run`. `RunAsLeader` then unlocks and the standby campaigns. dispatch-worker passes `Engine.LastPass` and its existing `stallLimit`.
- [ ] **Step 1:** Write `TestABlockedLoopLosesLeadership` (synctest): a `run` that blocks for ever with a frozen `lastPass` returns `ErrStalled` within `limit` plus one check interval. Run `go test ./internal/platform/lifecycle/ -run Blocked -count=1`. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run the same command. Expected: PASS.
- [ ] **Step 4:** Commit `fix(dispatch-worker): a wedged leader steps down (UX-2 Task 6)`.

### Task 2.7: Platform values are validated at load (F-21)

**Files:** Modify `internal/config/load.go:55-126`. Test `internal/config/config_test.go`.
- `Load` refuses `checkpoint_interval < 1m`, `relay_poll_interval <= 0`, and `outbox_batch_size` outside 1–10000, with `config: platform.yaml: <field>: …`.
- [ ] **Step 1:** Write `TestPlatformValuesAreValidated` (a table of zero, missing and out-of-range values, each giving a `Load` error). Run `go test ./internal/config/ -run Platform -count=1`. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run the same command. Expected: PASS.
- [ ] **Step 4:** Commit `fix(config): validate platform.yaml at load (UX-2 Task 7)`.

### Task 2.8: Throttled SMS retry once; one dedup lock per category (F-22, F-23)

**Files:** Modify `internal/platform/africastalking/sms.go:95-110` and `internal/platform/pg/opstore/dedup.go:21` and `internal/platform/pg/opstore/dedup.go:82`. Test `internal/platform/africastalking/sms_test.go` and `internal/platform/pg/opstore/dedup_integration_test.go`.

**Interfaces:**
- 408 and 429 are retried once inside the send context, after `Retry-After` capped at 2 s, or 500 ms without it. A second failure returns a new `ErrSMSThrottled`, which `dispatch.settlement` maps to `unknown` (re-claimable) instead of `failed`.
- The dedup lock is `pg_advisory_xact_lock(7347010, hashtext(category))`. Candidates are already filtered by category.

- [ ] **Step 1: Write the failing tests.**
  - `TestThrottledSendRetriesOnce`: an httptest server returns 429, then 201. The send succeeds, with 2 requests.
  - `TestTwoThrottlesSettleRetryable`.
  - `TestDedupOfDifferentCategoriesDoesNotSerialise` (integration): two `DispatchOrMerge` calls in different categories, the first holding its transaction open. The second completes in under 200 ms.
- [ ] **Step 2:** Run `go test ./internal/platform/africastalking/ -run Throttl -count=1`, then `go test -tags integration ./internal/platform/pg/opstore/ -run DedupOfDifferent -count=1`. Expected: FAIL.
- [ ] **Step 3:** Implement. Check that `docs/runbooks/sms-gateway-down.md` matches the new settlement.
- [ ] **Step 4:** Run both commands. Expected: PASS.
- [ ] **Step 5:** Commit `fix: retry a throttled SMS once; dedup lock per category (UX-2 Task 8)`.

### Task 2.9: UX-2 docs: ADR-0035, runbooks, reference

**Files:**
- Create: `docs/adr/ADR-0035-leader-liveness-and-shutdown.md`, and index it. It decides: server-side keepalives, the shutdown helper, step-down on stall, the ARI ping, and the migration lock timeouts. It updates ADR-0004 and ADR-0024 in place only through a "superseded in part" front-matter line.
- Modify:
  - `docs/runbooks/dispatch-worker-down.md`: an orphaned-lock check (`pg_locks` joined to `pg_stat_activity`, `state_change` older than 15 s on the holder) with `pg_terminate_backend` as the remedy, and the power-pull drill;
  - `docs/runbooks/outbox-relay-lag.md`, `docs/runbooks/database-migrations.md` (lock waits);
  - `docs/reference/environment.md` (ports, `SAUTI_ADMIN_ADDR`);
  - `docs/traceability.md`, `threat-model/threats.yaml`.
- [ ] **Step 1:** Write the docs and run the gates. Expected: all OK.
- [ ] **Step 2:** Commit `docs: ADR-0035 and runbooks for UX-2 (UX-2 Task 9)`. Push and open the PR against `main`. Gates, CodeRabbit, then **ask before merging**. In the PR, list the P0 settings that the code cannot cover (R-13).

---

## Phase UX-3: responder and coordinator voice correctness

**Branch** `ux-3-responder-coordinator-voice` from `ux-1-person-at-the-end`. **Findings** F-24 to F-32. **Migration** 0034.

### Task 3.1: Migration 0034: accept checks the outcome and the gates; the menu offers a lapsed alert; voice attempts record an answer (F-24, F-26, F-30, F-31)

**Files:**
- Create: `db/operational/migrations/0034_alert_outcomes_and_gates.sql`
- Modify: `db/operational/queries/dispatch.sql`, `internal/platform/pg/opstore/` (the adapters), `internal/dispatch/store.go` (`AcceptResult` gains `NotEligible`)
- Test: `internal/platform/pg/opstore/respond_integration_test.go`

**Interfaces (SQL, `CREATE OR REPLACE` on functions this plan may replace):**
- `op.accept_alert(p_alert, p_responder)` keeps its signature. A `declined` alert returns `no_longer_needed` and changes nothing. A responder failing `op.responder_eligible_now` returns the new result `not_eligible`. Otherwise it behaves as in 0023: first acceptance per role wins, and a late accept after a lapse counts while the role is open.
- The menu-alert function from 0020 returns a `no_response` alert as actionable while its incident is `dispatching` and its role has no open assignment.
- `ALTER TABLE op.alert_attempts ADD COLUMN answered_at timestamptz`. `op.mark_voice_attempt_answered(p_attempt uuid)` sets it once, with `clock_timestamp()`; it is granted to `op_ivr`.
- `GRANT EXECUTE ON FUNCTION op.responder_eligible_now(uuid) TO op_intake`.

- [ ] **Step 1: Write the failing tests** (integration):
  - `TestAcceptAfterDeclineIsRefused`;
  - `TestAcceptByAGatedResponderIsNotEligible`;
  - `TestLapsedAlertWithTheRoleOpenIsOfferedOnTheMenu`;
  - `TestVoiceAttemptRecordsItsAnswer`.
- [ ] **Step 2:** Run `go test -tags integration ./internal/platform/pg/opstore/ -run 'AcceptAfter|Gated|Lapsed|RecordsItsAnswer' -count=1`. Expected: FAIL.
- [ ] **Step 3:** Write the migration, generate (`bin/sqlc diff`: no output), and implement the adapters.
- [ ] **Step 4:** Run the same command. Expected: PASS.
- [ ] **Step 5:** Commit `fix(dispatch): accept respects declines and gates; lapsed alerts stay actionable; voice answers recorded (UX-3 Task 1)`.

### Task 3.2: An answered voice alert checks its outcome first (F-24)

**Files:** Modify `internal/platform/ivr/alerts.go:203-300`. Test `internal/platform/ivr/alerts_test.go`.

**Interfaces:**
- On `StasisStart`, after `AlertForAttempt`:
  - `Outcome` stood down, lapsed with the role taken, or declined: `finish` with `alert-no-longer-needed`;
  - accepted by this responder: `finish` with `alert-accepted` plus `whereToGo` plus `alert-bridge-instructions`;
  - otherwise, as today.
- Every answered call calls `MarkVoiceAttemptAnswered`.
- Key 1 maps `NotEligible` to a new `alert-not-eligible` recording ("contact your coordinator", PENDING).

- [ ] **Step 1: Write the failing tests.**
  - `TestAStoodDownAlertCallSaysNoLongerNeeded`: `alertRig` with `Outcome = stood_down`. The plays hold `alert-no-longer-needed` and not `alert-options`, and `AcceptAlert` is never called.
  - `TestADeclinedAlertCannotBeAcceptedByVoice`.
  - `TestAnAnsweredAlertCallIsMarkedAnswered`.
- [ ] **Step 2:** Run `go test ./internal/platform/ivr/ -run 'StoodDown|DeclinedAlert|MarkedAnswered' -count=1`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run the same command. Expected: PASS.
- [ ] **Step 5:** Commit `fix(alerts): never speak a stale alert; a voice call cannot undo a decline (UX-3 Task 2)`.

### Task 3.3: Coordinator and facility calls say how urgent, where, and which role (F-25, F-32)

**Files:** Modify `internal/platform/ivr/outbound.go:320-340` (forward sounds), `internal/platform/ivr/outbound.go:540-640` (coordinator `Handle` and `sounds`), and `telephony/prompts/catalogue.yaml` (`escalation-role-vht`, `escalation-role-transporter`: PENDING, both languages). Test `internal/platform/ivr/outbound_test.go`.

**Interfaces:**
- `CoordinatorCalls.Handle` loads `MessageFacts` for the incident, through the existing `op_ivr` grant used for forwards.
- `sounds` becomes: intro, then the grouped reference (Task 4.7 adds the pauses), then `alert-colour-<sev>`, `alert-category-<cat>`, then the ward and landmark (`whereToGo`; a free-text landmark speaks the ward only), then, for each reason in rank order, the reason followed by `escalation-role-<role>` when the role is set, then the options.
- `ForwardCalls.sounds` adds the ward and landmark after the category.
- The role comes from `claim_coordinator_calls`' `roles` (0033), passed as Stasis arguments after the reasons.

- [ ] **Step 1: Write the failing tests.**
  - `TestCoordinatorCallSaysSeverityCategoryPlaceAndRole`: a red `obstetric` escalation with role `transporter` plays `alert-colour-red`, `alert-category-obstetric`, `ward-w01`, `landmark-…` and `escalation-role-transporter`, in that order, before `escalation-options`.
  - `TestForwardCallSaysWhere`.
- [ ] **Step 2:** Run `go test ./internal/platform/ivr/ -run 'SaysSeverity|ForwardCallSaysWhere' -count=1`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run the same command. Expected: PASS.
- [ ] **Step 5:** Commit `feat(outbound): coordinator and facility calls carry severity, category, place and role (UX-3 Task 3)`.

### Task 3.4: Lapsed alerts on the menu; gated switch-on (F-26, F-31)

**Files:** Modify `internal/respond/ussd.go:110-160` and `internal/respond/ussd.go:280-300`, and `config/gated/responder.yaml` (`alerts_paused` menu text, PENDING-UX). Modify `internal/config/responder.go:30-34` (allow `alerts_paused`). Test `internal/respond/ussd_test.go` and `internal/respond/respond_integration_test.go`.

**Interfaces:**
- `pending` becomes `hasAlert && (alert.Outcome == pending || alert.Actionable)`, where `Actionable` comes from Task 3.1's menu function.
- After a successful switch-on, `ResponderEligibleNow` false shows `alerts_paused` ("on, but alerts are paused: contact your coordinator"). It shows nothing about which gate failed.
- `NotEligible` on accept shows `alerts_paused`.

- [ ] **Step 1: Write the failing tests.**
  - `TestLapsedAlertWithTheRoleOpenCanBeAccepted` (integration): `dialMenu ""` shows "1 Accept", and `"1"` gives the assigned screen.
  - `TestSwitchingOnWhileGatedOutSaysAlertsArePaused` (integration): `"3*1"` gives `alerts_paused`.
- [ ] **Step 2:** Run `go test -tags integration ./internal/respond/ -run 'Lapsed|GatedOut' -count=1`. Expected: FAIL.
- [ ] **Step 3:** Implement. Keep `TestEveryRenderedScreen…` green for the new text.
- [ ] **Step 4:** Run the same command. Expected: PASS.
- [ ] **Step 5:** Commit `fix(respond): lapsed alerts stay acceptable; gated responders are told alerts are paused (UX-3 Task 4)`.

### Task 3.5: Voice decline reasons are composed from the configured list (F-27)

**Files:** Modify `internal/platform/ivr/alerts.go:286-292`, the `AlertSounds` catalogue list, and `telephony/prompts/catalogue.yaml` (`decline-reason-<code>` per configured code, both languages; drop `decline-reasons-<role>`). Test `internal/platform/ivr/alerts_test.go` and `internal/platform/ivr/catalogue_test.go`.
- Key 2 with reasons plays `decline_why`, then `press-k` and `decline-reason-<code>` for each configured reason in order, as `Sounds()` does for Tool 1. `*` returns to the alert (ADR-0029 rule 1) without writing anything.
- [ ] **Step 1: Write the failing tests.**
  - `TestVoiceDeclineReasonsFollowTheConfiguredList`: the transporter plays include `press-3` then `decline-reason-bike_or_fuel`, and reversing the list in the test reverses the speech.
  - `TestStarInTheReasonsListReturnsToTheAlert`.

  Run `go test ./internal/platform/ivr/ -run 'DeclineReasons|StarInTheReasons' -count=1`. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run the same command. Expected: PASS.
- [ ] **Step 4:** Commit `fix(alerts): voice decline reasons follow the configured list (UX-3 Task 5)`.

### Task 3.6: A store error on a voice key is said, retried, then handed over (F-28)

**Files:** Modify `internal/platform/ivr/alerts.go:266-301` and `internal/platform/ivr/outbound.go:255-270` and `internal/platform/ivr/outbound.go:595-610`. Add `telephony/prompts/catalogue.yaml` entries `not-saved-press-again` (PENDING, both languages). Test `alerts_test.go` and `outbound_test.go`.
- On the first error, play `not-saved-press-again` and keep the keys live (`st.done` stays false). On a second error, `Continue` to `FallbackContext` for alert calls, or hang up for coordinator and facility calls (the escalation or forward keeps ringing).
- [ ] **Step 1: Write the failing tests.**
  - `TestAVoiceAcceptStoreErrorAsksAgain`: the second press retries.
  - `TestTwoVoiceAcceptErrorsReachACoordinator`.
  - `TestAFailedCoordinatorAcknowledgementAsksAgain`.

  Run `go test ./internal/platform/ivr/ -run 'StoreErrorAsksAgain|TwoVoiceAccept|FailedCoordinatorAck' -count=1`. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run the same command. Expected: PASS.
- [ ] **Step 4:** Commit `fix(ivr): a failed save is said and retried, never silent (UX-3 Task 6)`.

### Task 3.7: Outbound calls replay on silence, then hang up (F-29)

**Files:** Modify `internal/platform/ivr/outbound.go:141-145` and `internal/platform/ivr/outbound.go:560-626`. Test `outbound_test.go`.
- `DefaultNoInput` becomes 10 s, with at most 2 replays, then a hang-up (about 45 s in all). The answered-and-never-keyed case still counts as a ring (Task 1.8).
- [ ] **Step 1:** Write `TestAnOutboundCallReplaysTwiceThenHangsUp` (synctest): a second `Play` 10 s after `PlaybackFinished`, a third 10 s later, then `Hangup`. Run `go test ./internal/platform/ivr/ -run ReplaysTwice -count=1`. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run the same command. Expected: PASS.
- [ ] **Step 4:** Commit `fix(outbound): replay on silence, end in under a minute (UX-3 Task 7)`.

### Task 3.8: UX-3 docs

**Files:**
- Modify: `docs/adr/ADR-0027-channels-and-the-alert-ladder.md` (front matter: superseded in part by ADR-0029 for keys and retries).
- Modify: `docs/runbooks/voice-alerts-failing.md` (answered and unanswered attempts; the `alert-not-eligible` path), `docs/runbooks/no-eligible-responders.md` (what the coordinator now hears).
- Modify: `docs/reference/events.md`, `docs/traceability.md`, `threat-model/threats.yaml`.
- [ ] **Step 1:** Write the docs and run the gates. Expected: all OK.
- [ ] **Step 2:** Commit `docs: UX-3 runbooks and traceability (UX-3 Task 8)`. Push and open the PR against `ux-1-person-at-the-end`, or `main` once UX-1 has merged. Gates, CodeRabbit, then **ask before merging**. Flag for the PI: the late-accept policy (F-26) and the new recordings.


---

## Phase UX-4: caller clarity and trust cues

**Branch** `ux-4-caller-clarity-trust` from `ux-1-person-at-the-end`. **Findings** F-33 to F-43. **Migration** 0035.

### Task 4.1: Voice call-back digits survive silence; `*` clears (F-33)

**Files:** Modify `internal/platform/ivr/controller.go:289-303` and `internal/platform/ivr/controller.go:337` (Task 1.3 moved the no-input body). Test `controller_test.go`.

**Interfaces:**
- On no-input with `Shown == KindCallback`, if `pii.ParsePhone(st.digits)` succeeds, `answer(st.digits)`. Otherwise clear the digits and re-prompt (counted).
- `*` clears `st.digits` and replays the prompt without a notice.
- `#` with no digits stays invalid here. It means "no number" only with UX-6's `callback_none_option`.

- [ ] **Step 1: Write the failing tests.**
  - `TestTypedNumberIsSubmittedOnSilence`: type `0772999888`, wait past `NoInput`, and `answered()` holds it.
  - `TestStarClearsTypedDigits`: `077*0772999888#` submits `0772999888`.
- [ ] **Step 2:** Run `go test ./internal/platform/ivr/ -run 'SubmittedOnSilence|StarClears' -count=1`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run the same command. Expected: PASS.
- [ ] **Step 5:** Commit `fix(ivr): a typed number is never lost to silence; * clears (UX-4 Task 1)`.

### Task 4.2: Sauti's number, the bridge number and the responder code as placeholders (F-34)

**Files:**
- Modify: `internal/config/sms.go:22-36` and `internal/config/sms.go:95-110`, `internal/config/responder.go:30-34`, `config/gated/sms.yaml`, `config/gated/responder.yaml`.
- Modify: `internal/platform/channels/sms.go` (render), `internal/respond/ussd.go` (render), `cmd/dispatch-worker/main.go`, `cmd/intake-gw/main.go`, `.env.example`.
- Test: `internal/config/sms_test.go`, `internal/platform/channels/sms_test.go`, `internal/respond/ussd_test.go`.

**Interfaces:**
- New placeholders:
  - `{sauti_number}` in `helper_on_the_way` and `forwarded_to_facility`;
  - `{bridge}` in the menu's `accepted` and `assigned` texts;
  - `{code}` in `alert`, `alert_amended` and `availability_expiring`.
- Their values come from `SAUTI_ARI_CALLER_ID`, `SAUTI_BRIDGE_NUMBER` (the bridge DID, display form) and the first of `SAUTI_USSD_RESPONDER_CODES`. They travel as `config.PublicNumbers{Sauti, Bridge, ResponderCode string}`, passed to `CheckFits` and the renderers.
- `digitRun` still checks template text only. Rendered values are measured by `CheckFits` with the longest configured value.
- `Validate` refuses an `alert` or `availability_expiring` template that lacks `{code}` in any language.
- Copy stays `PENDING-SMS` and `PENDING-UX`; the typed `*384*2#` is removed from `sms.yaml`.

- [ ] **Step 1: Write the failing tests.**
  - `TestHelperOnTheWayNamesSautisNumber`: the rendered SMS holds the configured number in one GSM-7 segment.
  - `TestAlertTemplateWithoutCodeIsRefused`: an `lg` alert without `{code}`.
  - `TestUnknownPlaceholderStillRefused`.
  - `TestAcceptedScreenNamesTheBridgeNumber`.
- [ ] **Step 2:** Run `go test ./internal/config/ ./internal/platform/channels/ ./internal/respond/ -run 'SautisNumber|WithoutCode|UnknownPlaceholder|BridgeNumber' -count=1`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run the same command, then `go test ./... -count=1`. Expected: PASS.
- [ ] **Step 5:** Commit `feat(copy): texts name Sauti's number, the bridge and the responder code from configuration (UX-4 Task 2)`.

### Task 4.3: The bridge speaks the caller's language and fills its first silence (F-35; migration 0035)

**Files:** Create `db/operational/migrations/0035_bridge_language.sql`. Modify `db/operational/queries/dispatch.sql`, `internal/dispatch/store.go:163-168` (`BridgeTarget.Language`), `internal/platform/ivr/bridge.go:212-228` and `internal/platform/ivr/bridge.go:675`. Test `bridge_test.go` and `bridge_integration_test.go`.

**Interfaces:**
- 0035: `DROP FUNCTION op.bridge_targets_for_caller(bytea[])`, then `CREATE` it with the extra column `language text` (`i.language`), and restore the grants.
- `sound` takes the call's language: the caller target's language for a caller, and `b.c.Language` for a responder. An unrecognised number hears the bilingual `sauti/bridge-no-assignment`.
- Ringback starts right after `Answer`, before the store look-up.

- [ ] **Step 1: Write the failing tests.**
  - `TestBridgeSpeaksTheCallersLanguage`: `Language:"lg"` gives `sauti/lg/bridge-choose-role`.
  - `TestBridgeRingsBackBeforeTheLookup`.
  - `TestBridgeTargetsCarryTheLanguage` (integration).
- [ ] **Step 2:** Run `go test ./internal/platform/ivr/ -run 'CallersLanguage|RingsBackBefore' -count=1`. Expected: FAIL.
- [ ] **Step 3:** Implement (`bin/sqlc diff`: no output).
- [ ] **Step 4:** Run the same command, then the integration test. Expected: PASS.
- [ ] **Step 5:** Commit `fix(bridge): the caller's language, and no dead air after answer (UX-4 Task 3)`.

### Task 4.4: Notices name the problem; the language-screen notice is bilingual (F-36)

**Files:** Modify `internal/intake/machine.go:261-266`, `internal/platform/ivr/sounds.go:20-25`, `config/gated/tool1.yaml` (`messages.invalid_text`, `messages.invalid_phone`: PENDING-UX), and `internal/config/tool1.go`. Test `machine_test.go` and `sounds_test.go`.
- `Reprompt` picks the notice by `Kind`: free text gets `invalid_text`; call-back gets `invalid_phone`; anything else gets `invalid`. When `pr.Lang == "*"`, the `en` and `lg` notices are joined.
- The IVR plays `invalid-callback` for `KindCallback`.
- [ ] **Step 1: Write the failing tests.**
  - `TestNonMenuRepromptsDoNotSayChooseFromTheList`: `KindLandmarkText` and `KindCallback`, with `hasPhone=false`.
  - `TestLanguageRepromptIsBilingual`.
  - The screen gate still passes.

  Run `go test ./internal/intake/ ./internal/platform/ivr/ -run 'NonMenuReprompts|LanguageReprompt|EveryRendered' -count=1`. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run the same command. Expected: PASS.
- [ ] **Step 4:** Commit `fix(intake): notices name the problem; bilingual before a language is chosen (UX-4 Task 4)`.

### Task 4.5: Error screens: bilingual, gated, and separate for responders (F-37)

**Files:** Modify `config/gated/screens.yaml` (`internal_error` becomes one bilingual text that says a redial continues the report; add `responder_error`: "not saved, dial the menu again"; both PENDING-UX), `cmd/intake-gw/main.go:117-135`, and `internal/config/config.go` (screen gate covers `screens.yaml`). Test `cmd/intake-gw/main_test.go` and `internal/config/config_test.go`.
- The router passes the responder codes' handler its own `ErrorText`.
- [ ] **Step 1: Write the failing tests.**
  - `TestErrorScreensFitAndAreGSM7`.
  - `TestResponderErrorDoesNotSendToTheReportingLine`: a failing responder store gives text without the 0800 line.

  Run `go test ./cmd/intake-gw/ ./internal/config/ -run 'ErrorScreens|ResponderError' -count=1`. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run the same command. Expected: PASS.
- [ ] **Step 4:** Commit `fix(intake-gw): checked bilingual error screens; responders get their own (UX-4 Task 5)`.

### Task 4.6: Every recording has a script (F-38)

**Files:** Modify `telephony/prompts/catalogue.yaml` (each ID gains `text: {en, lg}`, PENDING where unwritten; the role words are shared by SMS and bridge), `internal/platform/ivr/catalogue_test.go`, and `internal/config/config.go` (`ProductionReady` reads the catalogue: a PENDING text or `status: placeholder` refuses production for ivr-controller).
- [ ] **Step 1: Write the failing tests.**
  - `TestEveryCatalogueEntryHasTextInBothLanguages`.
  - `TestEveryCatalogueEntryIsPlayedBySomeCodePath`: it catches the never-played `lg` bridge sounds.

  Run `go test ./internal/platform/ivr/ -run 'CatalogueEntry' -count=1`. Expected: FAIL.
- [ ] **Step 2:** Implement. The voice-only wording (press hash, "another landmark") gets PENDING entries, never final copy.
- [ ] **Step 3:** Run the same command. Expected: PASS.
- [ ] **Step 4:** Commit `feat(prompts): a script for every recording, refused in production until approved (UX-4 Task 6)`.

### Task 4.7: The reference is spoken in groups (F-39)

**Files:** Modify `internal/platform/ivr/outbound.go:708-716` (`digits`), and `internal/platform/ivr/sounds.go:44-56` (the IVR closing message says the reference twice). Test `outbound_test.go` and `sounds_test.go`.
- [ ] **Step 1:** Write `TestReferenceIsSpokenInGroups`: `digits("0471932856")` has `silence/1` after positions 3 and 6. Also write `TestClosingMessageSaysTheReferenceTwice`. Run `go test ./internal/platform/ivr/ -run 'SpokenInGroups|ReferenceTwice' -count=1`. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run the same command. Expected: PASS.
- [ ] **Step 4:** Commit `fix(ivr): speak the reference 3-3-4 (UX-4 Task 7)`.

### Task 4.8: Small text fixes: contiguous landmarks, ŋ, no empty separators, availability line (F-40, F-41, F-42, F-43)

**Files:** Modify `internal/location/gazetteer.go:233-243`, `internal/intake/machine.go:321-325`, `internal/config/gsm.go`, `internal/respond/ussd.go:218-221` and `internal/respond/ussd.go:259-275`, and `config/gated/responder.yaml` (`top_status_on`, `top_status_off`: PENDING-UX). Test the matching `_test.go` files.

**Behaviour:**
- `ParseFile` refuses non-contiguous menu positions.
- Free text maps `ŋ`/`Ŋ` to `ng'` before the GSM-7 check, and the free text is sealed mapped.
- The `IsGSM7` error for a template names ng'.
- A missing ward or a free-text landmark drops its separator.
- The top screen shows "on until HH:MM" or "off duty" (East Africa Time from the single zone of Task 2.3; until UX-2 merges, the existing zone).

- [ ] **Step 1: Write the failing tests.**
  - `TestGazetteerRefusesMenuGaps`.
  - `TestLugandaEngIsAcceptedAsNgApostrophe`: this replaces the assertion in `TestFreeTextLandmark` that pinned ŋ as invalid.
  - `TestNoEmptyPlaceSeparators`.
  - `TestTopScreenShowsAvailability`.

  Run them. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run them. Expected: PASS. `TestEveryRenderedScreen…` still passes.
- [ ] **Step 4:** Commit `fix: contiguous landmark menus, ŋ, clean place text, availability on the top screen (UX-4 Task 8)`.

### Task 4.9: UX-4 docs

**Files:** Modify `docs/reference/tool1-flow.md` (notices, voice call-back entry, `*`), `docs/reference/ussd-at-adapter.md`, `docs/reference/environment.md` (`SAUTI_BRIDGE_NUMBER`), `docs/reference/configuration.md`, `docs/runbooks/bridge-failing.md`, `docs/traceability.md` and `threat-model/threats.yaml` (rule 5: the placeholder values are public Sauti numbers only).
- [ ] **Step 1:** Write the docs and run the gates. Expected: all OK.
- [ ] **Step 2:** Commit `docs: UX-4 reference and traceability (UX-4 Task 9)`. Push and open the PR against `ux-1-…`, or `main`. Gates, CodeRabbit, then **ask before merging**. Flag for the PI: every new PENDING text and recording.

---

## Phase UX-5: audit trail

**Branch** `ux-5-audit-trail` from `ux-1-person-at-the-end`. **Findings** F-44, F-45, F-47 to F-50. **Migrations** 0036, 0037. **ADR** 0036. F-46 (staff identity) is not built: it waits for the DPO (R-4) and M5.

### Task 5.1: Migration 0036: coordinator call attempts and a ledger record of each acknowledgement (F-44)

**Files:**
- Create: `db/operational/migrations/0036_coordinator_call_audit.sql`, `internal/platform/pg/opstore/coordinator_audit_integration_test.go`
- Modify: `db/operational/queries/dispatch.sql`, `internal/platform/pg/opstore/` (the adapters), `internal/platform/ivr/outbound.go:440-610`

**Interfaces (SQL):**
- `CREATE TABLE op.coordinator_call_attempts`:
  - `id uuid PK DEFAULT uuidv7()`, `incident_id uuid NOT NULL REFERENCES op.incidents`, `through_seq bigint NOT NULL`;
  - `line text NOT NULL CHECK (line IN ('primary','backup'))`;
  - `claimed_at timestamptz NOT NULL DEFAULT clock_timestamp()`, `answered_at timestamptz`, `ended_at timestamptz`;
  - `result text CHECK (result IN ('acknowledged','no_key','no_answer','failed'))`.

  These are the attempts of an existing record, as `op.alert_attempts` holds alerts' attempts. The DPO is informed. The table holds no number.
- `op.begin_coordinator_call_attempt(p_incident uuid, p_seq bigint, p_line text) RETURNS uuid` and `op.finish_coordinator_call_attempt(p_attempt uuid, p_result text, p_answered boolean) RETURNS boolean` (first end wins). Both are granted to `op_ivr`.
- `op.acknowledge_coordinator_call(p_incident uuid, p_seq bigint)` keeps its signature (`CREATE OR REPLACE`). It also calls `op.enqueue_audit(uuidv7(), clock_timestamp(), 'escalation.acknowledged', p_incident, NULL, 'ux-0036', jsonb_build_object('through_seq', p_seq, 'line', <line of the newest answered attempt>))`. This is a ledger-only record: no incident event, so `op.append_event_core` is untouched. The staff ID is NULL until M5 binds the signed-in coordinator.

**Interfaces (Go):** `CoordinatorCalls.RunOnce` begins an attempt per originate. `Handle` marks it answered on `StasisStart`, and finishes it on acknowledgement, on hang-up with no key (`no_key`), on ring-out (`no_answer`) or on error (`failed`).

- [ ] **Step 1: Write the failing tests** (integration):
  - `TestThreeRingsThenAnAcknowledgementLeaveThreeAttemptsAndOneLedgerEntry`: 3 attempt rows (2 `no_answer`, 1 `acknowledged`) and one `escalation.acknowledged` outbox row whose payload carries `through_seq`.
  - `TestAcknowledgementLedgerEntryCarriesNoNumber`.
  - `go test -tags integration ./cmd/sautictl/ -run VerifyLedger` then shows the entry.
- [ ] **Step 2:** Run `go test -tags integration ./internal/platform/pg/opstore/ -run 'ThreeRings|AcknowledgementLedger' -count=1`. Expected: FAIL.
- [ ] **Step 3:** Write the migration, generate (`bin/sqlc diff`: no output), and wire `outbound.go`.
- [ ] **Step 4:** Run the same command, then `go test ./internal/platform/ivr/ -count=1`. Expected: PASS.
- [ ] **Step 5:** Commit `feat(audit): coordinator call attempts and a ledger record of each acknowledgement (UX-5 Task 1)`.

### Task 5.2: Migration 0037: control changes reach the ledger; events and outbox are append-only (F-45, F-47)

**Files:**
- Create: `db/operational/migrations/0037_ledger_controls.sql`, `internal/platform/pg/opstore/ledger_controls_integration_test.go`

**Interfaces (SQL):**
- `CREATE OR REPLACE` on these functions, with signatures unchanged, so each change also calls `op.enqueue_audit`:
  - `op.register_responder` and `op.set_responder_gates` (type `responder.registered`, `responder.gates_set`);
  - `op.set_availability`, only when the source is staff (`responder.availability_set_by_staff`); a responder's own toggle is not a control change;
  - the gazetteer load (`gazetteer.loaded`, with the version);
  - `op.finish_bridge_call` (`bridge.call_ended`, with the incident, the result and who began it).

  Each payload holds IDs and codes only, salted and hashed as `op.log_control` does (0016). `op.enqueue_audit` cannot serve here: it raises unless its ID names an incident event.
- A trigger `incident_events_append_only` on `op.incident_events` (row-level, every role) refuses `UPDATE` and `DELETE`, and a statement trigger refuses `TRUNCATE`, as 0016 does. Partitions are still dropped by the partition runbook, so M6 retention is unaffected.
- A trigger `outbox_append_only` on `op.outbox` refuses `DELETE` and `TRUNCATE`, and every `UPDATE` except the relay's own: `delivered_at` from NULL to a time, with every other column unchanged (`op.mark_outbox_delivered`, 0002). No job deletes from `op.outbox` on `main`; M6's retention (F-87) relaxes the trigger for delivered rows only.

- [ ] **Step 1: Write the failing tests** (integration):
  - `TestEveryStateChangingControlFunctionWritesOneLedgerEntry`. The function list is generated from `pg_proc`: `SECURITY DEFINER` in schema `op`, granted to `op_console` or the owner, not `STABLE`, minus an allow-list of read helpers. A new function without a ledger entry fails the test.
  - `TestIncidentEventsRefuseUpdateAndDeleteEvenForTheOwner`.
  - `TestOutboxRefusesDeleteAndPayloadRewrite`: `DELETE`, `TRUNCATE`, a payload change, `delivered_at` back to NULL, and a second `delivered_at` on a delivered row are each refused.
  - `TestOutboxRelayStillMarksDelivered`: `op.mark_outbox_delivered` on a pending row returns 1, and `op.claim_outbox` no longer returns it.
- [ ] **Step 2:** Run `go test -tags integration ./internal/platform/pg/opstore/ -run 'ControlFunction|RefuseUpdate|OutboxRefuses|OutboxRelay' -count=1`. Expected: FAIL.
- [ ] **Step 3:** Write the migration. Check that the partition-maintenance runbook's commands still work (`DROP`/`DETACH`, not `DELETE`).
- [ ] **Step 4:** Run the same command, then `scripts/ci/integration.sh`. Expected: PASS.
- [ ] **Step 5:** Commit `feat(audit): control changes reach the ledger; events and outbox are append-only (UX-5 Task 2)`.

### Task 5.3: `sautictl verify-coverage` (F-47)

**Files:** Create `cmd/sautictl/coverage.go` and `cmd/sautictl/coverage_integration_test.go`. Modify `cmd/sautictl/main.go:61-74`.

**Interfaces:** `sautictl verify-coverage --dsn <op_readonly DSN> --audit-dsn <audit reader DSN> [--older-than 5m]`. For every incident with events older than `--older-than`, it compares the count of `op.incident_events` per (incident, type) with the ledger entries per (incident, type). It prints `coverage OK` or one `missing incident=<id> type=<t> events=<n> ledger=<m>` line per mismatch, and exits 1 on any mismatch. Keying the ledger by event ID needs an audit-schema change and canonical encoding v2 (deferred).
- [ ] **Step 1:** Write `TestVerifyCoverageReportsADeletedEvent`. As owner, disable the Task 5.2 trigger inside the test transaction, delete one relayed event, re-enable it, and assert exit 1 with its `missing` line. Also write `TestVerifyCoverageOKOnAHealthyStore`. Run `go test -tags integration ./cmd/sautictl/ -run VerifyCoverage -count=1`. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run the same command. Expected: PASS.
- [ ] **Step 4:** Commit `feat(sautictl): verify-coverage reconciles events with the ledger (UX-5 Task 3)`.

### Task 5.4: State-changing commands confirm; the gazetteer has a dry run (F-48)

**Files:** Modify `cmd/sautictl/main.go:400-410` (gazetteer), `cmd/sautictl/main.go:500-535` (split) and `cmd/sautictl/main.go:636-690` (suspend and resume). Test `cmd/sautictl/main_test.go`.

**Interfaces:**
- `suspend`, `resume` and `split` print the effect, then:
  - on a terminal, require the typed word (`SUSPEND`, `RESUME`) or the reference for a split;
  - off a terminal, require `--yes`, or exit 2 before connecting.
- `gazetteer load --dry-run` validates, diffs against the version in force (wards, landmarks, facilities added or removed), lists the responders whose stage the new version cannot place, and writes nothing.

- [ ] **Step 1: Write the failing tests.**
  - `TestSuspendWithoutYesOffATerminalExitsTwoBeforeConnecting`.
  - `TestSplitWithoutYesOffATerminalExitsTwo`.
  - `TestGazetteerDryRunWritesNothing` (integration).

  Run `go test ./cmd/sautictl/ -run 'WithoutYes' -count=1`. Expected: FAIL.
- [ ] **Step 2:** Implement, detecting a terminal with `os.Stdin.Stat()` and `os.ModeCharDevice`; no new dependency.
- [ ] **Step 3:** Run the same command, then the integration test. Expected: PASS.
- [ ] **Step 4:** Commit `feat(sautictl): confirm before suspend, resume and split; gazetteer dry run (UX-5 Task 4)`.

### Task 5.5: The DSN comes from the environment; a no-op suspend says so (F-49, F-50)

**Files:** Modify `cmd/sautictl/main.go:636-700` (every `--dsn`: fall back to `SAUTI_DSN` with `env.Default`), and `cmd/sautictl/main.go:685` (the message). Test `cmd/sautictl/main_test.go` and `cmd/sautictl/dispatch_integration_test.go`.
- `changed == false` prints `dispatch already suspended since <time> (<reason>, by <staff or automatic>); nothing changed`, read from the register.
- [ ] **Step 1: Write the failing tests.**
  - `TestStatusReadsTheDSNFromTheEnvironment`.
  - `TestASecondSuspendSaysAlreadySuspended` (integration).

  Run them. Expected: FAIL.
- [ ] **Step 2:** Implement. The runbooks drop the passwords from their examples (`PGPASSFILE`; pgx honours it, which this task confirms by test).
- [ ] **Step 3:** Run them. Expected: PASS.
- [ ] **Step 4:** Commit `fix(sautictl): no password on the command line; honest no-op suspend (UX-5 Task 5)`.

### Task 5.6: UX-5 docs: ADR-0036

**Files:**
- Create: `docs/adr/ADR-0036-ledger-coverage-of-control-changes.md` (index it). It decides: attempts for coordinator calls; acknowledgements, control changes and bridge ends as ledger-only records (identifiers and salted hash; no incident event, because `op.append_event_core` stays M3's); append-only triggers; count-based coverage until encoding v2. Its Confirmation lists the tests of Tasks 5.1 to 5.5.
- Modify: `docs/reference/audit-canonical-encoding.md` (new entry types), `docs/reference/cli-sautictl.md`, `docs/runbooks/audit-ledger-verification-failure.md` (coverage failures), `suspension-and-resumption.md`, `split-wrong-merge.md` and `gazetteer-update.md` (`--yes`, `--dry-run`, `SAUTI_DSN`), `docs/traceability.md`, `threat-model/threats.yaml` (T-006-style tamper threats: the new `verified_by`), `governance/ai_risk_manifest.yaml`.
- [ ] **Step 1:** Write the docs and run the gates. Expected: all OK.
- [ ] **Step 2:** Commit `docs: ADR-0036 and audit runbooks (UX-5 Task 6)`. Push and open the PR. Gates, CodeRabbit, then **ask before merging**. In the PR, inform the DPO of `op.coordinator_call_attempts` and the new ledger entry types.


---

## Phase UX-6: protocol proposals behind flags (class b)

**Branch** `ux-6-protocol-flags` from `ux-1-person-at-the-end`. **Findings** F-07 (confirmation), F-51 to F-61. **Migration** 0038. **ADR** 0037.

Every task here ships its flag `false`. With the flags off, every screen, prompt and event batch equals `main`. All copy is PENDING. Turning a flag on is a protocol amendment for the PI, through the amendment gate and with translation. Production refuses a flag that is on while its copy is PENDING.

### Task 6.1: The protocol flags (F-07, F-51 to F-61)

**Files:** Modify `config/gated/flags.yaml`, `internal/config/config.go:115` (`Flags`), `internal/config/config.go:174-205` (`ProductionReady`), `internal/intake/machine.go` (`NewMachine`). Test `internal/config/config_test.go` and `internal/intake/machine_test.go`.

**Interfaces:** `Flags` gains, all `false`, under a comment that they are protocol proposals (ADR-0029), off until the PI amends:
- `out_of_area_confirm`, `place_readback`, `resume_confirm`;
- `alert_location_before_accept`, `callback_none_option`, `callback_readback`;
- `voice_keys_v2`, `language_switch`, `suspended_first_aid`;
- `closing_next_step`, `voice_line_on_screen1`, `bridge_announce`, `off_duty_declines_pending`.

`out_of_area_escalate_after_danger` comes from Task 1.11.

`NewMachine` refuses `dispatch_trigger_screen: 8` unless `callback_none_option` is on.

- [ ] **Step 1: Write the failing tests.**
  - `TestEveryProtocolFlagDefaultsOff`: load the committed `config/gated/flags.yaml`; every field is false.
  - `TestTriggerEightNeedsTheNoNumberOption`.
  - `TestAFlagOnWithPendingCopyIsRefusedInProduction`.

  Run `go test ./internal/config/ ./internal/intake/ -run 'ProtocolFlag|TriggerEight|FlagOnWithPending' -count=1`. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run the same command. Expected: PASS.
- [ ] **Step 4:** Commit `feat(config): protocol proposal flags, all off (UX-6 Task 1)`.

### Task 6.2: "Somewhere else" asks for confirmation (F-07, `out_of_area_confirm`)

**Files:** Modify `internal/intake/machine.go:183-245` (a `KindOutOfAreaConfirm` sub-step), `internal/intake/service.go`, `config/gated/tool1.yaml` (`ward.elsewhere_confirm`: PENDING-UX "not in {area}? 1 yes 2 show the wards again"), and `internal/platform/ivr/sounds.go`. Test `service_test.go`, `ivr_test.go` and `machine_test.go`.
- With the flag on, the elsewhere digit shows the confirm step and records nothing. `1` records `elsewhere` (then closes, or escalates with Task 1.11's flag). `2` shows screen 6 again.
- [ ] **Step 1: Write the failing tests.**
  - `TestElsewhereThenTwoShowsTheWardsAgain`: no out-of-area flag, and the incident is open.
  - `TestElsewhereThenOneCloses`.
  - `TestElsewhereConfirmFlagOffClosesAtOnce`.
  - The screen gate covers the new step in both languages.

  Run `go test ./internal/intake/ -run 'Elsewhere' -count=1`. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run the same command. Expected: PASS.
- [ ] **Step 4:** Commit `feat(intake): optional confirmation before out-of-area, flag off (UX-6 Task 2)`.

### Task 6.3: Closing and opening texts: the place and its correction, first aid while suspended, what happens next, the voice line (F-51, F-57, F-58, F-59)

**Files:** Modify `internal/intake/machine.go:200-212` (closing renders), `internal/config/tool1.go:240-260` (allowed placeholders), `config/gated/tool1.yaml` (`help_coming_readback`, `dispatch_suspended_first_aid`, `next_step_line`, `language.prompt_with_voice_line`: PENDING-UX and PENDING-MD-PI), and `internal/platform/ivr/sounds.go`. Test `machine_test.go` and `service_test.go`.

**Interfaces:**
- `place_readback` on: the screen-8 prompt and the closing text carry `{place}`, which is the landmark name and the ward name, or the ward name only for a free-text or unmatched landmark. The IVR speaks the `ward-*` and `landmark-*` recordings.
- `place_readback` on, the correction (ADR-0029 rule 4): after the landmark answer, a read-back step offers `1` right and `2` wrong. T2 is already recorded and is never delayed.
  - `2` changes no ward or landmark answer and re-dispatches nothing. It appends `flagged{needs_location_callback}` and `escalated{place_disputed}` (key `incident.EscalationKey("place_disputed", "")`), then goes on to screen 8. On voice, the controller hands the call over with that reason instead.
  - `place_disputed` joins the registry (Task 1.1) with PENDING recordings, a runbook row whose responder step reads "Not possible before M5:" (the M5 relocation tool tells the assigned responders), and a line in `docs/reference/events.md`.
  - `1`, or no answer, goes on to screen 8 with nothing written.
- `suspended_first_aid` on: `dispatch_suspended` may carry `{first_aid}`, and `Validate` requires it.
- `closing_next_step` on: one reserved PENDING line in `help_coming`.
- `voice_line_on_screen1` on: `language.prompt` carries `{voice_line}`, filled from configuration (P0 number).

All four must pass the screen gate with the longest values.

- [ ] **Step 1: Write the failing tests.**
  - `TestClosingTextNamesThePlaceWhenFlagged`: it holds `Test Market 1` and `Test Ward 1`.
  - `TestSuspendedClosingCarriesFirstAidWhenFlagged`.
  - `TestNextStepLineWhenFlagged`.
  - `TestScreenOneNamesTheVoiceLineWhenFlagged`.
  - `TestAWrongPlaceEscalatesAndKeepsThePlace` (service): with the flag on, `2` gives one `needs_location_callback` flag and one `place_disputed` escalation; ward, landmark and T2 are unchanged; a retried callback adds nothing.
  - `TestAWrongPlaceOnVoiceHandsOver` (controller): `2` gives `HandOver(…, "place_disputed")`, then `Continue`.
  - `TestClosingTextsFlagsOffEqualMain`.

  Run them. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run them, then `TestEveryRenderedScreenFitsLimitAndGSM7`. Expected: PASS.
- [ ] **Step 4:** Commit `feat(intake): optional read-back, first aid while suspended, next-step and voice-line texts, flags off (UX-6 Task 3)`.

### Task 6.4: A redial asks before continuing a report (F-52, `resume_confirm`)

**Files:** Modify `internal/intake/service.go:200-226` and `internal/intake/ivr.go:32-50` (`IVRTurn.Resumed bool`), `config/gated/tool1.yaml` (`resume.prompt`: PENDING-UX "continue report {ref}? 1 yes 2 new report"), and `telephony/prompts/catalogue.yaml` (`resume`). Test `service_test.go` and `ivr_test.go`.
- With the flag on, a resumable incident with any answer past the language screen shows the resume screen first. `2` starts a new incident and leaves the old one to the sweeper. A resumable incident with only the language answered resumes silently. The IVR prefixes the `resume` sound in both cases.
- [ ] **Step 1: Write the failing tests.**
  - `TestRedialOnASharedPhoneAsksBeforeContinuing`: S1 answers `1,2,1`, the clock moves 5 minutes, S2 sends `""` and sees the resume screen; `2` gives a new incident ID and the language screen.
  - `TestRedialAfterT2AsksBeforeContinuing`: S1 reached T2 and dropped at screen 8. S2 sees the resume screen, never "help is coming" at once.
  - `TestResumeConfirmFlagOffResumesSilently`.

  Run them. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run them. Expected: PASS.
- [ ] **Step 4:** Commit `feat(intake): optional confirmation before a resumed report, flag off (UX-6 Task 4)`.

### Task 6.5: The responder hears where before accepting; off duty declines a pending alert (F-53, F-61)

**Files:** Modify `internal/config/responder.go:30-34` (`alert` may carry `{ward}` and `{landmark}` when flagged), `internal/respond/ussd.go:110-160` and `internal/respond/ussd.go:280-300`, `internal/platform/ivr/alerts.go:346-358` (`alertSounds` order), `config/gated/responder.yaml` (PENDING-RESPONDER), and migration-free store calls. Test `ussd_test.go`, `alerts_test.go` and `respond_integration_test.go`.

**Interfaces:**
- `alert_location_before_accept` on: the menu alert renders the ward and landmark (an empty landmark drops cleanly, Task 4.8). `alertSounds` becomes intro, colour, category, ward, landmark, options, then the reference. SMS is unchanged (decision 1).
- `off_duty_declines_pending` on: "off duty" with an alert pending first declines it with reason `off_duty` (a configured reason code, PENDING the PI), then switches off, in one store call sequence on the same session.

- [ ] **Step 1: Write the failing tests.**
  - `TestPendingAlertScreenNamesTheWardWhenFlagged`.
  - `TestAlertCallSpeaksPlaceBeforeOptionsWhenFlagged`: `ward-w01` comes before `alert-options`, and the colour comes before the first digit.
  - `TestOffDutyDeclinesThePendingAlertWhenFlagged` (integration: the next pass alerts the next rider).
  - `TestResponderFlagsOffEqualMain`.

  Run them. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run them. Expected: PASS.
- [ ] **Step 4:** Commit `feat(respond): optional place before accept and off-duty decline, flags off (UX-6 Task 5)`.

### Task 6.6: "No number" and a read-back on the call-back screen (F-54)

**Files:** Modify `internal/intake/machine.go:200-210` and `internal/intake/machine.go:328-338`, `internal/incident/tool1.go` (accept `callback: none` as an answer value), `internal/platform/ivr/controller.go:289-303`, `config/gated/tool1.yaml` (`callback.none_label`, `callback.readback`: PENDING-UX), and `telephony/prompts/catalogue.yaml`. Test `machine_test.go`, `controller_test.go` and `ivr_test.go`.

**Interfaces (ADR-0029 rule 1):**
- `callback_none_option` on:
  - on USSD, screen 8 lists `0` "no number", which records `callback: none` and completes;
  - on voice, `#` with no digits does the same.
  - `none` is an answer value: no number is stored, and the incident is flagged `needs_location_callback` only if no session number exists.
- `callback_readback` on (voice only): after `#`, the digits are read back in groups; `1` submits, `2` clears and asks again.

- [ ] **Step 1: Write the failing tests.**
  - `TestNoNumberCompletesTheCallWhenFlagged`: USSD `0` and voice `#` both reach `KindComplete`.
  - `TestTypedNumberIsReadBackWhenFlagged`.
  - `TestCallbackFlagsOffEqualMain`.

  Run them. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run them. Expected: PASS.
- [ ] **Step 4:** Commit `feat(intake): optional no-number exit and voice read-back, flags off (UX-6 Task 6)`.

### Task 6.7: Voice keys v2 and the bridge announcement (F-55, F-60)

**Files:** Modify `internal/platform/ivr/controller.go:276-310` (the `*` and `0` keys; a 5 s wait for `*` after the closing message), `internal/platform/ivr/alerts.go:217-292` (`*` as a synonym for 3; `*` back in reasons comes from Task 3.5), `internal/platform/ivr/bridge.go:160-170` and `internal/platform/ivr/bridge.go:521-544` (`*` repeats a choice; `bridge-announce` to the answered leg), and `telephony/prompts/catalogue.yaml` (key announcements and `bridge-announce`: PENDING). Test `controller_test.go`, `alerts_test.go` and `bridge_test.go`.

**Interfaces:**
- `voice_keys_v2` on:
  - `*` replays the current prompt without a notice and without counting a retry;
  - on caller-facing menus, `0` hands over (Task 1.3's `handOver`, reason `voice_handed_over`); on number entry `0` stays a digit;
  - after the closing message, the call waits 5 s for `*` before hanging up;
  - the responder menu's Availability key is unchanged: moving it needs the PI.
- `bridge_announce` on: a responder-initiated call plays `bridge-announce` to the answered caller leg before `AddToBridge`.

- [ ] **Step 1: Write the failing tests.**
  - `TestStarRepeatsWithoutCountingWhenFlagged`.
  - `TestZeroReachesACoordinatorWhenFlagged`.
  - `TestStarReplaysTheClosingMessageWhenFlagged`.
  - `TestBridgeAnnouncesBeforeJoiningWhenFlagged`.
  - `TestVoiceKeyFlagsOffEqualMain`.

  Run them. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run them. Expected: PASS.
- [ ] **Step 4:** Commit `feat(ivr): optional ADR-0029 voice keys and bridge announcement, flags off (UX-6 Task 7)`.

### Task 6.8: A one-time language switch (F-56; migration 0038)

**Files:** Create `db/operational/migrations/0038_language_switch.sql`. Modify `internal/intake/machine.go` (screen 2's last option, labelled in the other language), `internal/intake/service.go`, `internal/incident/tool1.go` (Go's never-change rule exempts `language`), and `config/gated/tool1.yaml`. Test `service_test.go` and `internal/platform/pg/opstore/tool1_integration_test.go`.

**Interfaces:**
- 0038 replaces the trigger *function* behind `incident_events_answer_unchanged` (0006) so that `language` may change once. It never touches `op.append_event_core` or `op.append_incident_event`. A second change is refused.
- With the flag off, Go never emits a language change, so behaviour is identical.
- The switch is a recorded `screen.answered {field: language}` event, so the study sees it.

- [ ] **Step 1: Write the failing tests.**
  - `TestLanguageCanBeSwitchedOnceOnScreenTwoWhenFlagged`: walk `1`, then the last option on screen 2. Screen 2 renders in `lg`, and a later answer is accepted.
  - `TestASecondLanguageChangeIsRefused` (integration).
  - `TestLanguageFlagOffEqualMain`.

  Run them. Expected: FAIL.
- [ ] **Step 2:** Implement (`bin/sqlc diff`: no output).
- [ ] **Step 3:** Run them. Expected: PASS.
- [ ] **Step 4:** Commit `feat(intake): optional one-time language switch, flag off (UX-6 Task 8)`.

### Task 6.9: UX-6 docs: ADR-0037

**Files:**
- Create: `docs/adr/ADR-0037-protocol-proposals-behind-gated-flags.md` (index it). It decides that each proposal ships off, behind a gated flag with PENDING copy, and that production refuses a flag that is on while its copy is pending. It lists each flag with its finding and owner (PI; DPO for `callback: none` and the place before acceptance).
- Modify: `docs/reference/tool1-flow.md`, `docs/reference/configuration.md` (every flag), `docs/traceability.md`, `governance/amendments.yaml` (one proposed amendment row per flag, status `proposed`, if the file's schema allows; otherwise a note in ADR-0037), and `threat-model/threats.yaml`.
- [ ] **Step 1:** Write the docs and run the gates. Expected: all OK. The amendment gate passes because every flag is unchanged from `false`.
- [ ] **Step 2:** Commit `docs: ADR-0037, protocol proposals behind flags (UX-6 Task 9)`. Push and open the PR. Gates, CodeRabbit, then **ask before merging**. Send the PI the flag list with the audit's user stories.

---

## Phase UX-7: production refusals and security configuration

**Branch** `ux-7-production-refusals` from `ux-2-leader-robustness`. **Findings** F-62 to F-66. No migration, no ADR (ADR-0014's production checks are extended, recorded in the reference).

### Task 7.1: Each process refuses production on placeholder telephony and missing lines (F-62)

**Files:** Modify `cmd/ivr-controller/main.go:200-235`, `cmd/dispatch-worker/main.go:255-270`, `cmd/intake-gw/main.go:120-135`, and `internal/platform/africastalking/ussd.go:143-162`. Add `ari.Client.SoundExists(ctx, id) (bool, error)` (`GET /ari/sounds/{id}`). Test the `main_test.go` of each command, and `ussd_test.go`.

**Interfaces:** `func voiceProductionReady(env Env, cat Catalogue, sounds func(string) (bool, error)) error` in `cmd/ivr-controller`. With `SAUTI_ENV=production`, it refuses:
- `SAUTI_COORDINATOR_DIAL`, `SAUTI_COORDINATOR_BACKUP_DIAL`, `SAUTI_ARI_DIAL` or `SAUTI_ARI_CALLER_ID` empty, or any of them containing `placeholder`;
- the catalogue `status` not `approved`;
- any catalogued sound missing on the host;
- `SAUTI_ARI_CALLER_ID` differing (digits only) from `SAUTI_BRIDGE_NUMBER` (F-81's code check);
- `NewAlertCalls` with an empty `CallerID`, in every environment.

dispatch-worker refuses production without `SAUTI_AT_SENDER_ID`. intake-gw refuses an empty `SAUTI_USSD_SERVICE_CODES`. The USSD handler answers 413 and 415 (token valid) with `END` and the voice line.

- [ ] **Step 1: Write the failing tests.**
  - `TestVoiceProductionReadyRefusesPlaceholders` (a table).
  - `TestAlertCallsNeedACallerID`.
  - `TestDispatchWorkerProductionNeedsASenderID`.
  - `TestIntakeGatewayNeedsToolOneCodes`.
  - `TestATOversizeBodyGetsTheVoiceLine`: this replaces `TestATOversizeBodyRejected`.

  Run them. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run them. Expected: PASS.
- [ ] **Step 4:** Commit `fix(cmd): production refuses placeholder telephony, missing lines and IDs (UX-7 Task 1)`.

### Task 7.2: Outside production, only the sandbox (F-63)

**Files:** Modify `cmd/dispatch-worker/main.go:238-272`. Test `cmd/dispatch-worker/main_test.go`.
- Outside production, `pickMessenger` refuses a real client unless the URL host is `api.sandbox.africastalking.com` and the username is `sandbox`.
- [ ] **Step 1:** Write `TestDevWithTheLiveSMSURLIsRefused`. Run it. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run it. Expected: PASS.
- [ ] **Step 4:** Commit `fix(dispatch-worker): development sends only to the sandbox (UX-7 Task 2)`.

### Task 7.3: A reviewed egress list, https only (F-64)

**Files:** Create `config/gated/egress.yaml` (scheme, host, port per provider; gated, so a change is reviewed; the DPO signs off, R-5). Modify `internal/platform/egress/egress.go:20-40`, `cmd/dispatch-worker/main.go:258-265`, `internal/platform/ari/client.go:91`, `internal/config/load.go`, and `.golangci.yml` (`forbidigo`: `http.DefaultClient`, `&http.Client{`, `http.Get`, `http.Post` outside `internal/platform/egress`). Test `egress_test.go`.
- `egress.NewClient(list config.Egress, …)` refuses a URL whose scheme, host and port are not on the list, and refuses `http` except for loopback ARI.
- [ ] **Step 1: Write the failing tests.**
  - `TestEgressRefusesAHostNotOnTheList`.
  - `TestEgressRefusesPlainHTTP`.
  - `TestEgressAllowsLoopbackARIOverHTTP`.

  Run `go test ./internal/platform/egress/ -count=1`. Expected: FAIL.
- [ ] **Step 2:** Implement. Make sure the CI step that runs golangci-lint with `.golangci.yml` fails on a fixture that calls `http.Get`.
- [ ] **Step 3:** Run the same command. Expected: PASS.
- [ ] **Step 4:** Commit `fix(egress): a reviewed scheme-host-port list, https only (UX-7 Task 3)`.

### Task 7.4: ARI off-host needs https; dependency updates and a daily scan (F-65, F-66)

**Files:** Modify `cmd/ivr-controller/main.go:138` (refuse a non-loopback `http://` ARI URL). Create `.github/dependabot.yml` (gomod, github-actions, pip; weekly) and `.github/workflows/scheduled-vuln.yml` (daily `scripts/ci/vuln.sh` on `main`; actions pinned by SHA; `permissions: contents: read`). Test `cmd/ivr-controller/main_test.go`.
- [ ] **Step 1:** Write `TestOffHostARIOverHTTPIsRefused`. Run it. Expected: FAIL.
- [ ] **Step 2:** Implement and add the two files. Run `scripts/ci/governance.sh` (the workflow pinning check). Expected: OK.
- [ ] **Step 3:** Run the test. Expected: PASS.
- [ ] **Step 4:** Commit `chore: https for off-host ARI; Dependabot and a daily vulnerability scan (UX-7 Task 4)`.

### Task 7.5: UX-7 docs

**Files:** Modify `docs/reference/environment.md` (every variable now required in production), `docs/reference/configuration.md` (`egress.yaml`), `docs/reference/dependencies.md`, `docs/runbooks/ivr-controller-down.md` (the start-up refusals and what each means), `docs/traceability.md`, and `threat-model/threats.yaml` (T-001: `verified_by` the list test; T-015: the ARI check).
- [ ] **Step 1:** Write the docs and run the gates. Expected: all OK.
- [ ] **Step 2:** Commit `docs: production refusals reference (UX-7 Task 5)`. Push and open the PR. Gates, CodeRabbit, then **ask before merging**. Note in the PR that signed provenance needs Forgejo, Harbor and cosign (R-16).

---

## Phase UX-8: observability, operator tools and test depth

**Branch** `ux-8-observability-tools` from `main`, after UX-2 and UX-7 merge. **Findings** F-67 to F-74. **ADR** 0038.

### Task 8.1: A fixed-bucket histogram and labels in the metrics package (F-67)

**Files:** Modify `internal/platform/metrics/metrics.go`. Test `metrics_test.go`.

**Interfaces:**
- `func (r *Registry) Histogram(name, help string, buckets []float64) *Histogram` with `Observe(v float64)`.
- `func (r *Registry) CounterVec(name, help string, labels ...string) *CounterVec` with `With(values ...string) *Counter`.
- Label values must come from a closed set the caller declares. `With` panics on an undeclared value, so no number or free text can become a label (rule 5).

- [ ] **Step 1: Write the failing tests.**
  - `TestHistogramExposition`: the text format has `_bucket{le=…}`, `_sum` and `_count`.
  - `TestCounterVecRefusesAnUndeclaredLabelValue`.

  Run `go test ./internal/platform/metrics/ -count=1`. Expected: FAIL.
- [ ] **Step 2:** Implement (ADR-0017's in-house exposition, extended).
- [ ] **Step 3:** Run the same command. Expected: PASS.
- [ ] **Step 4:** Commit `feat(metrics): histograms and closed-set labels (UX-8 Task 1)`.

### Task 8.2: Instrument intake-gw, ivr-controller and the delivery attempts (F-67)

**Files:** Modify `internal/platform/africastalking/ussd.go`, `internal/intake/service.go` (re-prompts by screen), `cmd/intake-gw/main.go:154-171`, `internal/platform/ivr/controller.go`, `cmd/ivr-controller/main.go:146-160`, and `internal/dispatch/engine.go` (attempts at settle time). Test `ussd_test.go`, `controller_test.go` and `engine_test.go`.

**Metrics:**
- `sauti_ussd_requests_total{outcome}` and `sauti_ussd_request_duration_seconds`;
- `sauti_ussd_error_screens_total{reason}` and `sauti_ussd_reprompts_total{screen}`;
- `sauti_intake_escalations_total{reason}`, a closed set from `incident.EscalationReasons`;
- `sauti_ivr_calls_total{outcome}` and `sauti_ivr_handovers_total{cause}`;
- `sauti_delivery_attempts_total{channel,purpose,status}`;
- `sauti_dispatch_claimed_attempts_stale`;
- `sauti_safety_net_failures_total{loop}` (from Task 1.10's counter).

- [ ] **Step 1:** Write `TestAnInvalidInputIsCountedByScreen`: after one invalid key on screen 2, `/metrics` holds `sauti_ussd_reprompts_total{screen="2"} 1`. Also write `TestAHandOverIsCounted`. Run them. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run them. Expected: PASS.
- [ ] **Step 4:** Commit `feat: service-level metrics for USSD, voice and delivery (UX-8 Task 2)`.

### Task 8.3: Alert rules, tested in CI, and the SLO reference (F-67)

**Files:** Create `build/prometheus/sauti-rules.yml`, `build/prometheus/sauti-rules.test.yml` and `scripts/ci/promtool.sh` (runs `promtool test rules` from a digest-pinned container image). Modify `.github/workflows/ci.yml` (a `rules` job). Create the SLO reference page under `docs/reference/` (Task 8.8).

**Rules:**
- Multi-window burn rates for USSD availability and p99 latency.
- `sauti_escalation_oldest_unacknowledged_seconds > 600` for 1 minute: page tier 3.
- The relay lag and dispatch stall thresholds the runbooks already name.
- `sauti_safety_net_failures_total` increasing.

The targets are PENDING the PI (R-10).

- [ ] **Step 1:** Write the `promtool` fixtures, one per alert, that fire and that stay quiet. Run `scripts/ci/promtool.sh`. Expected: FAIL (no rules).
- [ ] **Step 2:** Write the rules.
- [ ] **Step 3:** Run the same script. Expected: `SUCCESS`.
- [ ] **Step 4:** Commit `feat(ops): Prometheus rules with promtool tests (UX-8 Task 3)`.

### Task 8.4: The simulator has a session clock (F-68)

**Files:** Modify `internal/platform/sim/ussd.go`. Create `tests/e2e/session_limits_test.go`.

**Interfaces:** `sim.USSDLimits{Idle, Total time.Duration}`, defaulting to 30 s and 120 s until O7 gives Uganda's values. `Send` fails with `ErrSessionEnded` past either limit, as an operator would.

- [ ] **Step 1: Write the failing tests.**
  - `TestAWalkAtFourteenSecondsPerScreenRecordsT2InsideTheSession`.
  - `TestAThirtyOneSecondPauseAtScreenSevenStillReachesAPerson`: the session ends, the sweeper runs, and `dispatch_not_started` is recorded (Task 1.10).

  Run `go test -tags integration ./tests/e2e/ -run 'Session|Pause' -count=1`. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run the same command. Expected: PASS.
- [ ] **Step 4:** Commit `test(sim): operator session limits (UX-8 Task 4)`.

### Task 8.5: `sautictl` output for machines; help exits 0 (F-70)

**Files:** Modify `cmd/sautictl/main.go:84-93` and `cmd/sautictl/main.go:697-727`. Test `cmd/sautictl/main_test.go`.
- `dispatch status --json`; `errors.Is(err, flag.ErrHelp)` exits 0; the usage keys are sorted.
- [ ] **Step 1: Write the failing tests.**
  - `TestHelpExitsZero`.
  - `TestUsageIsSorted`.
  - `TestStatusJSONParses` (integration).

  Run them. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run them. Expected: PASS.
- [ ] **Step 4:** Commit `feat(sautictl): status --json, help exits 0, sorted usage (UX-8 Task 5)`.

### Task 8.6: Fuzz the internet-facing parsers (F-71)

**Files:** Create `internal/respond/fuzz_test.go` (`FuzzResponderMenu`), `internal/platform/africastalking/delivery_fuzz_test.go` (`FuzzDeliveryReport`), `internal/platform/pii/fuzz_test.go` (`FuzzParsePhone`), and `internal/audit/fuzz_test.go` (`FuzzWitnessNote`), with seeds under `testdata/fuzz`. Modify `scripts/ci/fuzz-smoke.sh`.
- Each target asserts no panic plus one invariant:
  - the menu always answers `CON` or `END`;
  - a report never settles an unknown attempt;
  - `ParsePhone` round-trips E.164;
  - a note that fails to parse is never verified.
- [ ] **Step 1:** Write the targets. Run `go test ./internal/respond/ -run Fuzz -count=1` (the seed corpus). Expected: PASS on seeds. Then `go test -fuzz FuzzResponderMenu -fuzztime 60s ./internal/respond/`. Expected: no failure; fix any crasher it finds as a RED test first.
- [ ] **Step 2:** Add the targets to `fuzz-smoke.sh`.
- [ ] **Step 3:** Commit `test: fuzz the responder menu, delivery reports, phone parsing and witness notes (UX-8 Task 6)`.

### Task 8.7: One log scan for every rig; UUIDs survive redaction; planned markers expire (F-72, F-73)

**Files:** Modify `internal/platform/logging/redact.go:27`, and create `internal/platform/logging/capture.go` (a capturing handler for tests). Modify the `TestMain` of each package with a rig, `governance/compliance_check.py:8`, and `threat-model/validate_threats.py:6`. Create `governance/current_milestone` (`M2`). Test `redact_test.go`, `governance/tests/test_planned_markers.py`.

**Behaviour:**
- `RedactString` skips matches inside an RFC 9562 UUID.
- Every rig's `TestMain` fails if a known test number appears in any captured output.
- Both checkers fail on `planned:Mn` where `n` is at or below `governance/current_milestone`.

Updating the stale markers (for example `threats.yaml`'s `planned:M2 …` entries) to real pointers is part of this task.

- [ ] **Step 1: Write the failing tests.**
  - `TestRedactKeepsUUIDs`.
  - `test_planned_marker_for_a_merged_milestone_fails`.

  Run `go test ./internal/platform/logging/ -count=1` and `python3 -m pytest governance/tests -k planned -q`. Expected: FAIL.
- [ ] **Step 2:** Implement. Replace each stale marker with the test that now proves it, or with an honest later milestone.
- [ ] **Step 3:** Run both, then the three gates. Expected: PASS and OK.
- [ ] **Step 4:** Commit `test: one log scan for every rig; UUIDs kept; planned markers expire (UX-8 Task 7)`.

### Task 8.8: UX-8 docs: ADR-0038, the SLO reference, the runbook index

**Files:**
- Create: `docs/adr/ADR-0038-service-level-indicators-and-alert-rules.md` (index it; it updates ADR-0017 in part), the SLO reference page `docs/reference/slos.md`, and `docs/runbooks/README.md` (a role glossary with roles only, SEV-1 to SEV-4 definitions, a reason-to-runbook index generated from `incident.EscalationReasons`, and a drill schedule; F-69).
- Modify: `docs/traceability.md` (F-74: every CV-29 case now has its pointer), `governance/ai_risk_manifest.yaml`, and `threat-model/threats.yaml`.
- Test: `governance/tests/test_runbooks.py::test_every_owner_is_in_the_glossary`, and `test_every_escalation_section_has_a_time`.
- [ ] **Step 1:** Write the two docs tests. Run `python3 -m pytest governance/tests -k 'glossary or escalation_section' -q`. Expected: FAIL.
- [ ] **Step 2:** Write the docs. Unify each runbook's Owner field to the glossary's roles.
- [ ] **Step 3:** Run the tests and the gates. Expected: PASS and OK.
- [ ] **Step 4:** Commit `docs: ADR-0038, SLO reference, runbook index (UX-8 Task 8)`. Push and open the PR. Gates, CodeRabbit, then **ask before merging**.

---

## Deferred, with the reason

| Finding | What | Why not now | Unblocked by |
|---|---|---|---|
| F-82 (CU-4) | Escalate an abandoned danger-yes call on the session-end signal, not after 120 s | The Africa's Talking end-of-session payload is unverified | A logged payload in P0 (R-17) |
| F-36 (CU-7, part) | Keep the first 20 characters of an over-long landmark | A reading of Build Prompt §10.1 | The PI (R-9) |
| F-43 (RS-18, part) | An SMS when availability expires; record reminder attempts | Adds a message to the protocol | The PI |
| F-50 (SO-25, part) | An "affirmed" row in the suspension register | A CHECK change for a PI-defined meaning | The PI |
| F-18 (RR-11, part) | `-- migrate:no-transaction` for `CONCURRENTLY` | No current need | The first concurrent index |
| F-47 (RR-17, part) | `event_id` in the ledger and canonical encoding v2 | Changes the audit schema | A dedicated ADR |
| F-32 (SO-13, part) | A second facility call, and recognising a facility's number on the bridge | Forwarding mechanics (decision 2) | The PI (R-9) |
| F-40 (CU-17, CV-20, part) | A fixed escape digit; a voice ward cap | Tool 1 structure | The PI (R-10) |
| F-42 (RS-22, part) | One place order across channels | Copy order | The PI |
| F-23 (CU-6, part) | The 800 ms deadline as an SLO, not a cut-off | Amends the spec | The PI (R-10) |
| F-77 (SO-14) | Ring or task per reason; one suspension announcement | A clinical-operations choice | The PI and Medical Director (R-2) |
| F-78, F-84 (RS-16, RS-25) | Parallel-alert policy; excluding a reporter | Policy | The PI (R-6) |
| F-46 (SO-10, RR-5) | Staff identity | A staff register is staff personal data | The DPO (R-4); M5 OIDC |
| F-75 (SO-9, RS-15) | Assignment release; closure | Milestone scope | M3 Part A; M5 |
| F-79, F-80 | Breach clock, transfer basis; paper IDs | Legal and form design | The DPO (R-5); the PI (R-7) |
| F-83, F-88, F-89, F-90 | Load and soak; type-ahead; label order; a call over USSD | Field and load evidence | M7 |
| F-87 | Retention and growth | Milestone scope | M6 |
| F-85, F-86, F-81 (P0 part), F-76 | Tone zone, clock alarm, numbering, ruleset | Infrastructure or repository settings | The owner and P0 (R-12 to R-16) |
| F-91, F-92 | Colour and clock words; stand-down reasons | Translation; closure reasons | Translators (R-8); M5 |

## Coverage check

Every class (a) and (b) finding has a task. The Critical and High findings are fully specified.

| Finding | Task |
|---|---|
| F-01 | 1.8 |
| F-02 | 1.2, 1.3, 1.4, 1.6, 1.7 |
| F-03 | 1.5 (start-up sound check: 7.1) |
| F-04, F-05, F-10 | 1.10 |
| F-06 | 1.8, 1.9 |
| F-07 | 1.11, 6.2 |
| F-08 | 1.12 |
| F-09, F-14 | 1.13 |
| F-11 | 1.1 |
| F-12 | 1.3 |
| F-13 | 1.4 |
| F-15 | 2.1 |
| F-16 | 2.2 |
| F-17 | 2.3 |
| F-18 | 2.4 |
| F-19 | 2.5 |
| F-20 | 2.6 |
| F-21 | 2.7 |
| F-22, F-23 | 2.8 |
| F-24 | 3.1, 3.2 |
| F-25, F-32 | 3.3 |
| F-26, F-31 | 3.1, 3.4 |
| F-27 | 3.5 |
| F-28 | 3.6 |
| F-29 | 3.7 |
| F-30 | 3.1, 3.2 |
| F-33 | 4.1 |
| F-34 | 4.2 |
| F-35 | 4.3 |
| F-36 | 4.4 |
| F-37 | 4.5 |
| F-38 | 4.6 |
| F-39 | 4.7 |
| F-40 to F-43 | 4.8 |
| F-44 | 5.1 |
| F-45 | 5.2 |
| F-47 | 5.2, 5.3 |
| F-48 | 5.4 |
| F-49, F-50 | 5.5 |
| F-51, F-57, F-58, F-59 | 6.3 |
| F-52 | 6.4 |
| F-53, F-61 | 6.5 |
| F-54 | 6.6 |
| F-55, F-60 | 6.7 |
| F-56 | 6.8 |
| F-62 | 7.1 |
| F-63 | 7.2 |
| F-64 | 7.3 |
| F-65, F-66 | 7.4 |
| F-67 | 8.1, 8.2, 8.3 |
| F-68 | 8.4 |
| F-69 | 8.8 |
| F-70 | 8.5 |
| F-71 | 8.6 |
| F-72, F-73 | 8.7 |
| F-74 | the tests of UX-1 and UX-4, listed in 8.8 |

**Migrations.**

| Number | Task | Content |
|---|---|---|
| 0033 | 1.8 | `claim_coordinator_calls`: reasons and roles |
| 0034 | 3.1 | Accept, menu, voice answers, eligibility grant |
| 0035 | 4.3 | Bridge targets carry the language |
| 0036 | 5.1 | Coordinator call attempts, acknowledgement record |
| 0037 | 5.2 | Ledger controls, append-only triggers |
| 0038 | 6.8 | The language trigger function |

None replaces `op.append_incident_event` or `op.append_event_core`, and none edits a merged file. Task 1.6 adds an `op_ivr` grant only if one is missing; it goes into 0033 rather than a separate file.

**ADRs.** ADR-0029 (this plan's standards; written), ADR-0034 (1.13), ADR-0035 (2.9), ADR-0036 (5.6), ADR-0037 (6.9), ADR-0038 (8.8).
