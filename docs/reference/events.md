# Incident event catalogue

**Type:** Reference (Diátaxis) · **Code:** `internal/incident/event.go`, `internal/incident/transitions.go` · **ADR:** [ADR-0003](../adr/ADR-0003-single-write-path.md)

Every state change is one row in `op.incident_events`, written only by `op.open_incident` or `op.append_incident_event`. The protocol timestamps are set by the database clock and are first-wins: a later event of the same type never moves them (Build Prompt §15.1).

| Event type | Sets | Payload (M0) | Notes |
|---|---|---|---|
| `session.started` | **T1** | `reference`, `channel` | written only by `op.open_incident` |
| `screen.answered` | — | `screen`, `answer` | Tool 1 answers (M1) |
| `incident.classified` | — | `severity` (`red`/`yellow`/`green`), `category` | logic version in the event's `logic_version` column; severity can only rise |
| `incident.dispatch_ready` | **T2** | `trigger_screen` | severity and location known (screen 7, D5) |
| `alert.issued` | **T3** (first) | `role`, `ring` | |
| `responder.accepted` | **T4** (first) | `role` | per-role T4 lives in assignments (M2) |
| `responder.arrived` | **T5** (first, attested) | `role` | verification in M3 |
| `responder.departed` | **T6** | — | |
| `responder.at_facility` | **T7** | `facility` | |
| `incident.flagged` | — | `flag` (a non-empty JSON string, enforced by the database) | e.g. `out_of_area`, `needs_location_callback`, `test_call` |
| `incident.escalated` | — | `reason` | sets `escalated` |
| `incident.merged` | — | `into` (incident UUID) | duplicates (M1) |
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
