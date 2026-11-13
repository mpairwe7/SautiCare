# Tool 1 intake flow

**Type:** Reference (Diátaxis) · **Code:** `internal/intake/machine.go`, `internal/intake/service.go`, `internal/intake/sweeper.go` · **ADRs:** [ADR-0018](../adr/ADR-0018-session-state-in-postgresql.md), [ADR-0019](../adr/ADR-0019-signed-triage-decision-table.md), [ADR-0020](../adr/ADR-0020-location-grid-and-gazetteer.md), [ADR-0021](../adr/ADR-0021-sealed-answers-and-keyed-step-keys.md)

The eight screens of Data Collection Tool 1 (Build Prompt §10.1). The wording, categories, age bands, danger questions and first-aid lines are **placeholders** in `config/gated/tool1.yaml` until Tool 1 is received; production refuses them.

| Screen | Field (`screen.answered`) | Values | Triggers |
|---|---|---|---|
| 1 Language | `language` | `en`, `lg` | — |
| 2 Danger check | `danger` | `yes`, `no`, `unknown` | — |
| 3 Category | `category` | catalogue codes = the triage bundle's domain | — |
| 4 Age band | `age_band` | catalogue codes; `child` marks under 18 | — |
| 5 Category danger question | `specific_danger` | `yes`, `no` | `incident.classified` by the signed table (rule error: red, `triage_rule_error`, escalated), in the same transaction as the answer |
| 6 Ward | `ward` | gazetteer ward ID, or `elsewhere` | `elsewhere`: `out_of_area` flag and `incident.closed` in the same transaction as the answer, END with 999 or 112 |
| 7 Landmark | `landmark` | menu landmark ID (1–5), or `other` (6: up to 20 GSM characters, sealed) | typed text matched within the ward at once; at `dispatch_trigger_screen` 7: location resolved, `incident.dispatch_ready` (**T2**) |
| 8 Callback | `callback` | `same_number`, or `provided` (typed number, sealed) | END: HELP IS COMING, the reference, the first-aid line (only once T2 is recorded) |

**Screen 5's question** is the category's question, or the paediatric danger signs (`acute_medical_child`) for acute medical illness under 18 (Guide §5).

**Location at T2** (ADR-0020) is resolved in this order:
1. A listed landmark gives that landmark's cell (`landmark`).
2. Free text matched within the ward at or above `fuzzy_match_min_similarity` gives the matched landmark's cell (`fuzzy`). The match runs at screen 7, when the text is typed. The matched landmark ID (never the text) is kept in the session's USSD step (`op.ussd_steps.matched_landmark`), so a screen-8 trigger in the same session still uses it.
3. Otherwise the ward's centre is used (`ward`), the incident is flagged `needs_location_callback`, and dispatch proceeds on the ward's zones.

`dispatch_ready` carries `zone {col,row}`, `location_quality`, `gazetteer_version` and `trigger_screen`.

**Duplicates at T2** (ADR-0022) are handled in the same transaction that records T2, under an advisory lock, with the candidates read and locked `FOR SHARE` so none can close or be split before the decision commits. A call that matches an incident already dispatching (same category, zones at most `dedup_max_zone_distance` apart, first calls at most `dedup_window` apart; defaults one zone and 15 minutes, set in `config/gated/intake.yaml`) is merged into the earliest-dispatched one, whose T1 becomes the earliest call's. The merged caller still finishes screen 8 on their own record. The merge never makes the emergency look less urgent: if the merged call's severity is higher, the survivor's rises to it (`incident.severity_raised`), and if the merged call was escalated, the survivor is escalated (`merged_call_escalated`). **A call located only to its ward** (location quality `ward`: an unmatched typed landmark, or IVR "another landmark") sits at the ward's centre whatever its real place, so it is never merged and no call is merged into it; both are dispatched.

**Every answer** is persisted at once through the single write path. A typed number or landmark is sealed first (`op.incident_identifiers`) and referenced by `identifier_id`. The answer and what it implies by itself are then **one transaction** (`AppendBatch`): `screen.answered`, plus at screen 5 the classification (with, on a rule error, the `triage_rule_error` flag and the escalation), plus for "Somewhere else" the `out_of_area` flag and the closure. A failure stores all of them or none.

**An answered field never changes.** A `screen.answered` that gives a different value for a field the incident already holds is refused, in the fold (`incident.Apply`) and in the database (trigger `incident_events_answer_unchanged`, migration 0006). The same value again, from a retry or another session, is accepted. So a late callback from an older session cannot change the category or the screen 5 answer after the incident was classified on them; it gets the error screen.

**Catch-up.** T2 is a separate write, so a request can still fail between an answer and T2 (the 800 ms USSD budget, a lost connection). After every applied answer, and whenever a session starts or resumes on either channel, intake reads the projection and records what the answers imply but the log lacks: the classification if screen 5 is answered and severity is empty; T2 if the incident is triaged, in the area, has a landmark, has no T2, and the trigger screen is reached (screen 7, or screen 8 when that is the trigger). So a retried callback or the caller's redial repairs any partial request. On a redial the typed landmark's match is unknown (the text is sealed and intake cannot read it back, and the match lives in the old session's steps): the incident is then located at ward level and flagged `needs_location_callback`, the fail-safe direction.

**Never "help is coming" without T2.** If Tool 1 is finished but T2 could not be recorded, the request fails instead (`intake.ErrNotDispatched`): the caller gets the error screen with the free voice line, and a redial records T2 first.

**Idempotency keys.** What the caller said is keyed by session: `ussd:<provider>:<session>:s<screen>[:<step>]` (or `ivr:<call>:…`), so a retried or duplicated callback writes nothing twice. What the answers imply is keyed by incident (`intake:<incident>:classify`, `:dispatch_ready`, …; [events.md](events.md)), so every session and channel converges on one event.

**Resume** applies when the caller redials from the same number within `resume_window` (10 min), on either channel, while the incident is open and unfinished (`last_screen < 8` or no T2 yet, not closed or merged). The redial records any missing classification or T2 (catch-up), then continues at the first unanswered screen through a new session row and a `session.resumed` event; reference and T1 are unchanged. A withheld number never resumes.

**USSD input.** Each step stores the prompt it showed and the input length it consumed. The next callback's answer is the text after that prefix, and the prefix is verified by its keyed hash. Invalid input repeats the screen with `messages.invalid`.

**Abandonment.** A session silent for `abandon_after` (120 s), with Tool 1 unfinished or without T2, is flagged `abandoned`, and also `abandoned_before_category` if it never reached a category. Before T2 it is escalated at once: with reason `abandoned_after_danger_yes` after a danger `yes`, otherwise with reason `abandoned_red_before_dispatch` if it was classified red (screen 5 yes, whatever screen 2 said) (runbook `abandoned-danger-callback.md`). The flags and the escalation are one transaction, so a failure leaves neither and the next sweep retries. The `abandoned` flag is never cleared. A caller who resumes an abandoned incident, then answers danger `yes` (or is classified red) and drops again before T2, is swept again while the incident is not escalated, and escalated once.

**Screen gate.** Every screen, rendered at its worst case in every language, must fit each operator limit and use only the GSM 03.38 basic table. This is checked in CI (`internal/intake/machine_test.go::TestEveryRenderedScreenFitsLimitAndGSM7`) and by intake-gw at start-up.
