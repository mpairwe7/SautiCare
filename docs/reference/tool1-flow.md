# Tool 1 intake flow

**Type:** Reference (Diátaxis) · **Code:** `internal/intake/machine.go`, `internal/intake/service.go`, `internal/intake/sweeper.go` · **ADRs:** [ADR-0018](../adr/ADR-0018-session-state-in-postgresql.md), [ADR-0019](../adr/ADR-0019-signed-triage-decision-table.md), [ADR-0020](../adr/ADR-0020-location-grid-and-gazetteer.md), [ADR-0021](../adr/ADR-0021-sealed-answers-and-keyed-step-keys.md)

The eight screens of Data Collection Tool 1 (Build Prompt §10.1). The wording, categories, age bands, danger questions and first-aid lines are **placeholders** in `config/gated/tool1.yaml` until Tool 1 is received; production refuses them.

| Screen | Field (`screen.answered`) | Values | Triggers |
|---|---|---|---|
| 1 Language | `language` | `en`, `lg` | — |
| 2 Danger check | `danger` | `yes`, `no`, `unknown` | — |
| 3 Category | `category` | catalogue codes = the triage bundle's domain | — |
| 4 Age band | `age_band` | catalogue codes; `child` marks under 18 | — |
| 5 Category danger question | `specific_danger` | `yes`, `no` | `incident.classified` by the signed table (rule error: red, `triage_rule_error`, escalated) |
| 6 Ward | `ward` | gazetteer ward ID, or `elsewhere` | `elsewhere`: `out_of_area` flag, `incident.closed`, END with 999 or 112 |
| 7 Landmark | `landmark` | menu landmark ID (1–5), or `other` (6: up to 20 GSM characters, sealed) | at `dispatch_trigger_screen` 7: location resolved, `incident.dispatch_ready` (**T2**) |
| 8 Callback | `callback` | `same_number`, or `provided` (typed number, sealed) | END: HELP IS COMING, the reference, the first-aid line |

**Screen 5's question** is the category's question, or the paediatric danger signs (`acute_medical_child`) for acute medical illness under 18 (Guide §5).

**Location at T2** (ADR-0020) is resolved in this order:
1. A listed landmark gives that landmark's cell (`landmark`).
2. Free text matched within the ward at or above `fuzzy_match_min_similarity` gives the matched landmark's cell (`fuzzy`).
3. Otherwise the ward's centre is used (`ward`), the incident is flagged `needs_location_callback`, and dispatch proceeds on the ward's zones.

`dispatch_ready` carries `zone {col,row}`, `location_quality`, `gazetteer_version` and `trigger_screen`.

**Every answer** is persisted at once through the single write path. Idempotency keys are `ussd:<provider>:<session>:s<screen>[:<step>]`, so a retried or duplicated callback writes nothing twice. A typed number or landmark is sealed first (`op.incident_identifiers`) and referenced by `identifier_id`.

**Resume** applies when the caller redials from the same number within `resume_window` (10 min), on either channel, while the incident is open and unfinished (`last_screen < 8`, not closed or merged). The redial continues at the first unanswered screen through a new session row and a `session.resumed` event; reference and T1 are unchanged. A withheld number never resumes.

**USSD input.** Each step stores the prompt it showed and the input length it consumed. The next callback's answer is the text after that prefix, and the prefix is verified by its keyed hash. Invalid input repeats the screen with `messages.invalid`.

**Abandonment.** A session silent for `abandon_after` (120 s) is flagged `abandoned`, and also `abandoned_before_category` if it never reached a category. After a danger `yes`, before T2, it is escalated with reason `abandoned_after_danger_yes` (runbook `abandoned-danger-callback.md`).

**Screen gate.** Every screen, rendered at its worst case in every language, must fit each operator limit and use only the GSM 03.38 basic table. This is checked in CI (`internal/intake/machine_test.go::TestEveryRenderedScreenFitsLimitAndGSM7`) and by intake-gw at start-up.
