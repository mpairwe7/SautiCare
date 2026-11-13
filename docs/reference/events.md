# Incident event catalogue

**Type:** Reference (Diátaxis) · **Code:** `internal/incident/event.go`, `internal/incident/transitions.go` · **ADR:** [ADR-0003](../adr/ADR-0003-single-write-path.md)

Every state change is one row in `op.incident_events`, written only by `op.open_incident` or `op.append_incident_event`. The protocol timestamps are set by the database clock and are first-wins: a later event of the same type never moves them (Build Prompt §15.1).

| Event type | Sets | Payload | Notes |
|---|---|---|---|
| `session.started` | **T1** | `reference`, `channel` | written only by `op.open_incident` |
| `session.resumed` | — | `channel` | a redial within the resume window joined this incident (M1) |
| `screen.answered` | — | exactly `screen` (1–8), `field`, `value` (a code), optional `identifier_id` | Tool 1 answers; validated identically in Go and SQL; see [tool1-flow.md](tool1-flow.md) |
| `incident.classified` | — | `severity` (`red`/`yellow`/`green`), `rule_id` | `logic_version` = signed bundle `<version>+sha256:<12 hex>`; severity can only rise |
| `incident.dispatch_ready` | **T2** | `trigger_screen`, `zone {col,row}`, `location_quality` (`landmark`/`fuzzy`/`ward`), `gazetteer_version` | severity and location known (screen 7, D5); zone and quality go together |
| `alert.issued` | **T3** (first) | `role`, `ring` | |
| `responder.accepted` | **T4** (first) | `role` | per-role T4 lives in assignments (M2) |
| `responder.arrived` | **T5** (first, attested) | `role` | verification in M3 |
| `responder.departed` | **T6** | — | |
| `responder.at_facility` | **T7** | `facility` | |
| `incident.flagged` | — | `flag` (a non-empty JSON string, enforced by the database) | intake: `out_of_area`, `needs_location_callback`, `triage_rule_error`, `abandoned`, `abandoned_before_category` |
| `incident.escalated` | — | `reason` | sets `escalated`; intake: `triage_rule_error`, `abandoned_after_danger_yes`, `abandoned_red_before_dispatch`; deduplication: `merged_call_escalated` (an escalated call merged into a survivor that was not) |
| `incident.merged` | — | `into` (incident UUID: existing, open, not itself) | on the later call, at T2 (ADR-0022); a merged call then accepts only its own caller's answers and its unmerge |
| `incident.merge_absorbed` | T1 (earliest) | exactly `from` (incident UUID), `t1` (RFC 3339, ≤ µs) | on the survivor, after the call's own `incident.merged` into it; `t1` must equal the call's stored T1 (the database checks the call's row); T1 = earliest of its own call and every absorbed call |
| `incident.severity_raised` | — | exactly `severity` (`red`/`yellow`/`green`), `from` (the merged call's incident UUID) | on the survivor, in the merge's transaction, when the merged call is more urgent; `from` must be in the survivor's `absorbed` set and `severity` must be that call's own (the database checks the call's row); the severity must rise strictly; sets severity, not `logic_version` |
| `incident.split` | T1 (recomputed) | exactly `from` | staff only, from the console role only (`op.split_incident`; the database refuses every other role); the call leaves the survivor's `absorbed` set |
| `incident.unmerged` | — | exactly `from` (the survivor) | staff, console role only; `merged` → `dispatching`; needs T2 |
| `incident.closed` | — | `reason` | terminal |

## Idempotency keys and atomic batches

Every append carries an idempotency key, unique per incident; a retry with the same key returns the stored event and writes nothing. `op.append_incident_event` can also run several times in one transaction (`incident.Service.AppendBatch`, `opstore.Store.AppendBatch`): all of the events are stored or none, and each sees the state the previous one left.

Intake uses two kinds of key (see [tool1-flow.md](tool1-flow.md)):
- **Session-scoped**, for what a caller said: `ussd:<provider>:<session>:s<screen>` or `ivr:<call>:s<screen>` for `screen.answered`, with a suffix for the sealed identifier it references.
- **Incident-scoped**, for what the answers imply: `intake:<incident>:classify`, `:rule_error`, `:escalate`, `:out_of_area`, `:close`, `:needs_location_callback` and `:dispatch_ready`. Whichever session or channel records one first, every retry or redial converges on that one event.

A merge at T2 (ADR-0022) keys its events by the merged call: `intake:<call>:dispatch_ready:merge` (`incident.merged`), and on the survivor `dedup:absorb:<call>`, `dedup:raise:<call>` (`incident.severity_raised`) and `dedup:escalate:<call>`. A coordinator's split is keyed by the merge it undoes (the call's latest `incident.merged` event): `split:<survivor>:<call>:<merge event>:split` and `…:unmerge`. A retried split is a no-op; a call merged into the same survivor again and split again gets new keys.

**Staff actions.** An event with actor `staff`, `incident.split` and `incident.unmerged` are accepted only from a session of the console role (`op_console`); intake, IVR and every other role get `SA422`.

## Lifecycle

States: `intake → triaged → dispatching → assigned → on_scene → transporting → at_facility`, plus the terminal states `closed` and `merged`. `*` means any non-terminal state; a blank target means the state is unchanged. The same table is seeded into `op.incident_transitions`, and `internal/platform/pg/opstore/opstore_integration_test.go::TestTransitionsTableMatchesGo` fails if the two ever diverge.

| From | Event | To |
|---|---|---|
| intake | incident.classified | triaged |
| triaged | incident.dispatch_ready | dispatching |
| dispatching, assigned | alert.issued | — |
| dispatching | responder.accepted | assigned |
| assigned | responder.accepted | — |
| assigned | responder.arrived | on_scene |
| on_scene | responder.arrived | — |
| on_scene | responder.departed | transporting |
| transporting | responder.at_facility | at_facility |
| * | screen.answered, incident.flagged, incident.escalated | — |
| * | incident.merge_absorbed, incident.split, incident.severity_raised | — |
| * | incident.closed | closed |
| intake, triaged, dispatching | incident.merged | merged |
| merged | incident.unmerged | dispatching |
| merged | screen.answered | — |

A merged incident accepts only its exact transitions (its unmerge and its own caller's answers), never a `*` event.

## Errors raised by the write path

| SQLSTATE | Go error | Meaning |
|---|---|---|
| `SA404` | `incident.ErrNotFound` | unknown incident |
| `SA409` | `incident.ErrVersionConflict` | expected version did not match |
| `SA422` | `incident.ErrIllegalTransition` / `ErrSeverityLowered` | event not allowed in this state / severity downgrade |
| `23505` on `incidents_reference_key` | `incident.ErrReferenceTaken` | random reference collided; the service retries |
