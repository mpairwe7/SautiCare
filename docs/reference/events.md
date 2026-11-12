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
| `incident.flagged` | — | `flag` | intake: `out_of_area`, `needs_location_callback`, `triage_rule_error`, `abandoned`, `abandoned_before_category` |
| `incident.escalated` | — | `reason` | sets `escalated`; intake: `triage_rule_error`, `abandoned_after_danger_yes` |
| `incident.merged` | — | `into` (incident UUID: existing, open, not itself) | on the later call, at T2 (ADR-0022); a merged call then accepts only its own caller's answers and its unmerge |
| `incident.merge_absorbed` | T1 (earliest) | exactly `from` (incident UUID), `t1` (RFC 3339, ≤ µs) | on the survivor; T1 = earliest of its own call and every absorbed call |
| `incident.split` | T1 (recomputed) | exactly `from` | staff only; the call leaves the survivor's `absorbed` set |
| `incident.unmerged` | — | exactly `from` (the survivor) | staff; `merged` → `dispatching`; needs T2 |
| `incident.closed` | — | `reason` | terminal |

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
| * | incident.closed | closed |
| intake, triaged, dispatching | incident.merged | merged |

## Errors raised by the write path

| SQLSTATE | Go error | Meaning |
|---|---|---|
| `SA404` | `incident.ErrNotFound` | unknown incident |
| `SA409` | `incident.ErrVersionConflict` | expected version did not match |
| `SA422` | `incident.ErrIllegalTransition` / `ErrSeverityLowered` | event not allowed in this state / severity downgrade |
| `23505` on `incidents_reference_key` | `incident.ErrReferenceTaken` | random reference collided; the service retries |
