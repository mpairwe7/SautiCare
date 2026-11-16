---
status: accepted
date: 2026-11-16
decision-makers: Sauti engineering (repository owner)
consulted: UX audit F-78 and R-6 (docs/explanation/ux-audit-2026-11.md); Build Prompt §10.4; ADR-0024; ADR-0025; ADR-0028
informed: PI, Medical Director, technical operator
---
# ADR-0044: One alert per responder, for the incident nearest to them

## Context and Problem Statement

Dispatch planned each incident on its own (ADR-0024). Nothing stopped one responder from holding pending alerts for several incidents at once: in the demo's surge, VHT 4 held five. The responder menu offers only the newest pending alert (`op.responder_menu_alert`, 0020), so the older ones could not be answered at all (UX audit F-78). The planner meanwhile counted the same person as on the way to every one of those incidents, though they can take one call at most. A responder also got whichever incident was planned first, not the one nearest to them.

The audit left the parallel-alert policy to the PI (R-6). The repository owner decided it on 16 November 2026: a responder gets one alert at a time, and it is for the incident nearest to them.

How does dispatch hold a responder to one alert, give them the nearest incident, and still never leave an incident without help or a coordinator?

## Decision Drivers

* A responder can answer only the alert their menu shows, so every alert sent must be answerable.
* The planner must not count one person as help for several incidents.
* Nearest first (Build Prompt §10.4 ranks by zone distance): a responder should go where they are nearest.
* Never take a responder away from a more urgent incident.
* Dispatch never ends silently (ADR-0024): an incident nobody can take still reaches a coordinator.
* The database is the last guard behind the planner (review focus 4), as for every other alert refusal.

## Considered Options

* **One pending alert per responder, enforced by `op.issue_alert`; the planner skips a responder who holds another alert, unless this incident is strictly nearer to them and no less urgent, in which case their first alert is moved; an incident with only held responders waits for one rather than escalating at once.**
* A global assignment over all open incidents at each wake: rejected for now. It changes the planner from one incident at a time to all at once, and the worker's lease, retry and audit model (ADR-0024) are built per incident.
* Keep parallel alerts and let the menu list them: rejected. A basic-phone menu is short, and the planner would still count one person several times.

## Decision Outcome

Chosen: one pending alert per responder, with moves to a nearer incident.

* **The store.** Migration 0042 makes `op.issue_alert` refuse a responder who already holds a pending alert on another incident that is dispatching, assigned or on scene. It locks the responder row for update, so two incidents planned at the same moment cannot both alert them. The refusal is an illegal transition, which the worker treats as it does every refusal: it re-plans without that responder.
* **The planner.** Before ranking, the worker reads every held alert (`HeldAlerts`) and measures how far each held incident is from its responder's stage, by the same zone distance the ranking uses. A responder holding another alert is a candidate here only if `dispatch.Movable` holds: this incident is strictly nearer to them, and it is at least as urgent (red before yellow before green; a triage error counts as red). Otherwise they are counted as *held*.
* **A move.** An alert for a moved responder carries the alert they hold. The worker first stands that alert down through `op.move_alert`, which also wakes its incident's planner at once, so that incident looks for someone else and the responder hears it is no longer needed. Then it alerts them here. If the held alert settled in between, the worker re-plans without them.
* **Waiting, not giving up.** "No eligible responder" is escalated only when nobody is free *and* nobody is held. An incident whose only responders are held waits for one to be freed, and is looked at again at each ring step. When a responder is freed (an alert stood down or lapsed), `op.wake_open_dispatch` wakes every incident still looking for help at once. Red incidents still escalate at three minutes without an acceptance (decision 2), so waiting is bounded.

### Consequences

* Good: every alert a responder holds is the one their menu shows, and they can answer it.
* Good: a responder goes to the incident nearest to them, and never away from a more urgent one.
* Good: in a surge, the calls nobody is free for wait in dispatch and are taken as responders free up. None is dropped, and a red call still reaches a coordinator at three minutes.
* Bad: fewer parallel red alerts when several red calls are close together; the parallel count (`red_parallel`) applies only to free responders.
* Bad: a move stands down an alert the responder may already be reading. The stand-down SMS says it is no longer needed, and the new alert follows; the two SMS may arrive in either order.
* Bad: a responder moved away from an incident is not alerted for it again: an incident alerts each responder once (`op.alerts` holds one row per incident and responder). The incident keeps looking for others, and escalates as before.
* Neutral: the dispatch logic version stamped on alerts comes from `config/gated/dispatch.yaml` (`ranking_version`). This policy change should be recorded there through the amendment process before the study window opens, so that alerts can be attributed to it.

### Confirmation

* `internal/dispatch/rank_test.go::TestMovableOnlyToANearerNoLessUrgentIncident`, `plan_test.go::TestHeldRespondersAreWaitedForNotEscalated` and `TestAMovedResponderCarriesTheAlertToStandDown`.
* `internal/platform/pg/opstore/dispatch_integration_test.go::TestOneAlertPerResponder`: the refusal, the move and its wake, merged incidents, and the wake of open incidents.
* `cmd/demo/edge_integration_test.go` (surge, repeat caller) and the demo's surge scenario: no responder ever holds two alerts.
