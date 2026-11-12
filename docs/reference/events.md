# Incident event catalogue

**Type:** Reference (Diátaxis) · **Code:** `internal/incident/event.go`, `internal/incident/transitions.go` · **ADR:** [ADR-0003](../adr/ADR-0003-single-write-path.md)

Every state change is one row in `op.incident_events`, written only by `op.open_incident`, `op.append_incident_event` or — for the dispatch facts — the dispatch functions of migration 0015 (ADR-0025), which write the fact's own row (`op.alerts`, `op.assignments`, `op.forwards`, `op.caller_notices`) and the event in one transaction; the generic append refuses the dispatch event types. The protocol timestamps are set by the database clock and are first-wins: a later event of the same type never moves them (Build Prompt §15.1).

| Event type | Sets | Payload | Notes |
|---|---|---|---|
| `session.started` | **T1** | `reference`, `channel` | written only by `op.open_incident` |
| `session.resumed` | — | `channel` | a redial within the resume window joined this incident (M1) |
| `screen.answered` | — | exactly `screen` (1–8), `field`, `value` (a code), optional `identifier_id` | Tool 1 answers; validated identically in Go and SQL; see [tool1-flow.md](tool1-flow.md) |
| `incident.classified` | — | `severity` (`red`/`yellow`/`green`), `rule_id` | `logic_version` = signed bundle `<version>+sha256:<12 hex>`; severity can only rise |
| `incident.dispatch_ready` | **T2** | `trigger_screen`, `zone {col,row}`, `location_quality` (`landmark`/`fuzzy`/`ward`), `gazetteer_version` | severity and location known (screen 7, D5); zone and quality go together |
| `alert.issued` | **T3** (first) | exactly `alert`, `responder` (UUIDs), `role` (`vht`/`transporter`), `ring` (1–99), `wave` (1–9999) | `op.issue_alert` only (op_dispatch); one alert per responder per incident; `logic_version` = dispatch parameter version (`<ranking_version>+sha256:<12 hex of the gated config>`); allowed in `dispatching`, `assigned`, `on_scene` |
| `responder.accepted` | **T4** (first) | exactly `alert`, `responder`, `role` | `op.accept_alert` only (op_intake, op_ivr); first per role wins (`op.assignments` partial unique index); `dispatching` → `assigned` |
| `alert.declined` | — | exactly `alert`, `responder`, optional `reason` (a code) | `op.decline_alert`; no reason until decision 6's list is approved (amendment gate) |
| `alert.no_response` | — | exactly `alert` | `op.close_alert` at the cascade timeout; a later acceptance still counts while the role is open |
| `alert.stood_down` | — | exactly `alert` | `op.close_alert` after the role is taken or the incident ends; an incident cannot close while an alert is pending |
| `incident.forwarded` | — | exactly `facility` (gazetteer ID) | `op.record_forward`: a forwarding-enabled facility of the given gazetteer version (amended mode) |
| `incident.forward_acknowledged` | — | exactly `facility` | `op.acknowledge_forward` (op_ivr: the facility pressed 1); a late acknowledgement counts |
| `incident.forward_timed_out` | — | exactly `facility` | `op.time_out_forward` after 60 s without acknowledgement |
| `caller.notified` | — | exactly `kind` (`helper_on_the_way`, or `forwarded_to_facility` with `facility`) | `op.notify_caller`, once per kind |
| `responder.arrived` | **T5** (first, attested) | `role` | verification in M3 |
| `responder.departed` | **T6** | — | |
| `responder.at_facility` | **T7** | `facility` | |
| `incident.flagged` | — | `flag` | intake: `out_of_area`, `needs_location_callback`, `triage_rule_error`, `abandoned`, `abandoned_before_category` |
| `incident.escalated` | — | exactly `reason` (a code), optional `role` | sets `escalated`; dispatch (`op.dispatch_escalate`, once per reason): `no_eligible_responder`, `red_no_acceptance`, `forwarded_to_facility`, `no_facility_acknowledged`, `dispatch_suspended`; intake: `triage_rule_error`, `abandoned_after_danger_yes`, `abandoned_red_before_dispatch`; deduplication: `merged_call_escalated` (an escalated call merged into a survivor that was not) |
| `incident.merged` | — | `into` (incident UUID: existing, open, not itself) | on the later call, at T2 (ADR-0022); a merged call then accepts only its own caller's answers and its unmerge |
| `incident.merge_absorbed` | T1 (earliest) | exactly `from` (incident UUID), `t1` (RFC 3339, ≤ µs) | on the survivor; T1 = earliest of its own call and every absorbed call |
| `incident.severity_raised` | — | exactly `severity` (`red`/`yellow`/`green`), `from` (the merged call's incident UUID) | on the survivor, in the merge's transaction, when the merged call is more urgent; the severity must rise strictly; sets severity, not `logic_version` |
| `incident.split` | T1 (recomputed) | exactly `from` | staff only, from the console role only (`op.split_incident`; the database refuses every other role); the call leaves the survivor's `absorbed` set |
| `incident.unmerged` | — | exactly `from` (the survivor) | staff, console role only; `merged` → `dispatching`; needs T2 |
| `incident.closed` | — | `reason` | terminal |

## Idempotency keys and atomic batches

Every append carries an idempotency key, unique per incident; a retry with the same key returns the stored event and writes nothing. `op.append_incident_event` can also run several times in one transaction (`incident.Service.AppendBatch`, `opstore.Store.AppendBatch`): all of the events are stored or none, and each sees the state the previous one left.

Intake uses two kinds of key (see [tool1-flow.md](tool1-flow.md)):
- **Session-scoped**, for what a caller said: `ussd:<provider>:<session>:s<screen>` or `ivr:<call>:s<screen>` for `screen.answered`, with a suffix for the sealed identifier it references.
- **Incident-scoped**, for what the answers imply: `intake:<incident>:classify`, `:rule_error`, `:escalate`, `:out_of_area`, `:close`, `:needs_location_callback` and `:dispatch_ready`. Whichever session or channel records one first, every retry or redial converges on that one event.

Dispatch keys its events by the fact: `alert:<responder>`, `accept:<alert>`, `decline:<alert>`, `no_response:<alert>`, `stood_down:<alert>`, `forward:<facility>`, `forward_ack:<facility>`, `forward_timeout:<facility>`, `notify:<kind>` and `escalate:<reason>`.

A merge at T2 (ADR-0022) keys its events by the merged call: `intake:<call>:dispatch_ready:merge` (`incident.merged`), and on the survivor `dedup:absorb:<call>`, `dedup:raise:<call>` (`incident.severity_raised`) and `dedup:escalate:<call>`. A coordinator's split uses `split:<survivor>:<call>:split` and `…:unmerge`.

**Staff actions.** An event with actor `staff`, `incident.split` and `incident.unmerged` are accepted only from a session of the console role (`op_console`); intake, IVR and every other role get `SA422`.

## Lifecycle

States: `intake → triaged → dispatching → assigned → on_scene → transporting → at_facility`, plus the terminal states `closed` and `merged`. `*` means any non-terminal state; a blank target means the state is unchanged. The same table is seeded into `op.incident_transitions`, and `internal/platform/pg/opstore/opstore_integration_test.go::TestTransitionsTableMatchesGo` fails if the two ever diverge.

| From | Event | To |
|---|---|---|
| intake | incident.classified | triaged |
| triaged | incident.dispatch_ready | dispatching |
| dispatching, assigned, on_scene | alert.issued | — |
| dispatching | responder.accepted | assigned |
| assigned, on_scene | responder.accepted | — |
| assigned | responder.arrived | on_scene |
| on_scene | responder.arrived | — |
| on_scene | responder.departed | transporting |
| transporting | responder.at_facility | at_facility |
| * | screen.answered, incident.flagged, incident.escalated | — |
| * | incident.merge_absorbed, incident.split, incident.severity_raised | — |
| * | alert.declined, alert.no_response, alert.stood_down | — |
| * | incident.forwarded, incident.forward_acknowledged, incident.forward_timed_out, caller.notified | — |
| * | incident.closed | closed |
| intake, triaged, dispatching | incident.merged | merged |
| merged | incident.unmerged | dispatching |
| merged | screen.answered | — |

A merged incident accepts only its exact transitions (its unmerge and its own caller's answers), never a `*` event. Each role searches on its own, so the other role may still be alerted and accept once the first responder is on scene. `incident.closed` is refused while any alert is pending: whoever closes stands the responders down first.

## Errors raised by the write path

| SQLSTATE | Go error | Meaning |
|---|---|---|
| `SA404` | `incident.ErrNotFound` | unknown incident, alert, responder or forward |
| `SA403` | `incident.ErrForbidden` | a responder answering another responder's alert |
| `SA409` | `incident.ErrVersionConflict` | expected version did not match |
| `SA422` | `incident.ErrIllegalTransition` / `ErrSeverityLowered` | event not allowed in this state / severity downgrade |
| `23505` on `incidents_reference_key` | `incident.ErrReferenceTaken` | random reference collided; the service retries |
