---
status: accepted
date: 2026-11-13
decision-makers: Sauti engineering (repository owner)
consulted: Build Prompt §7 (one write path, idempotency), §9 Dispatch, §10.4, §16 scenario 14; spec §6
informed: technical operator, Data Protection Officer
---
# ADR-0025: Dispatch facts through their own functions; first acceptance wins in the database

## Context and Problem Statement

Alerts, acceptances, declines, lapses, stand-downs, forwards, acknowledgements and caller notices are dispatch facts. Each needs its own row: who was alerted, on which ring, with what outcome, and when T3 and T4 happened. Each is also a state change that must add an `incident_events` row in the same transaction (Build Prompt §7). Two responders may accept the same role at the same instant. A worker may die between deciding to alert and the provider confirming the message. How are dispatch facts written so that the tables and the event log never disagree, a role is never taken twice, and no alert is sent twice?

## Decision Drivers

* One write path (ADR-0003): no runtime role writes a table directly.
* "First acceptance wins per role, enforced by a database constraint" (§10.4).
* Scenario 14: a failover mid-cascade loses no alert and duplicates none.
* Defence in depth for review focus 4: a lapsed responder is never alerted, even if the worker read stale data.

## Considered Options

* **One SECURITY DEFINER function per command**, which writes the fact's row and its event in one transaction, with a generic append that refuses dispatch event types.
* Dispatch tables written by the worker, with events appended separately.
* Events only, with dispatch tables as projections.

## Decision Outcome

Chosen option: **one function per command** (migration 0015).
- `op.issue_alert` writes `alert.issued`, and the first alert is T3. There is one alert per responder per incident (`UNIQUE (incident_id, responder_id)`), so a retry returns the same alert. The function refuses a responder who is not, at that moment, active, certified, insured, kit OK and available. It also refuses one assigned to another open incident, a role already taken, and a merged or closed incident.
- `op.accept_alert` (op_intake, op_ivr) writes `responder.accepted`, and the first is T4. It serialises on the incident, and a partial unique index allows one active assignment per (incident, role). A second acceptor hears `already_taken`. A late acceptance after a lapse still counts while the role is open. A responder may accept only their own alert (`SA403`).
- The remaining functions follow the same pattern:
  - `op.decline_alert`: no reason until decision 6's list is approved.
  - `op.close_alert`: `no_response` or `stood_down`.
  - `op.record_forward`, `op.acknowledge_forward` (op_ivr) and `op.time_out_forward`.
  - `op.notify_caller`: once per kind.
  - `op.dispatch_escalate`: once per reason.
- The shared core `op.append_event_core` (no grants) keeps every check and the projection. The generic `op.append_incident_event` refuses the dispatch event types. Payloads are identifiers and codes only, checked identically in Go and SQL.
- `op.alert_attempts` records each delivery before the provider is called: one attempt per alert, purpose and channel. An attempt is claimed once and settled `sent`, `failed` or `unknown`. A claimed attempt is never sent again: the cascade's next rung covers an unknown result.
- An incident cannot close while an alert is pending; whoever closes it stands the responders down first. Closure (and a merge) wakes dispatch, so the stand-downs are delivered, and the planner then parks the wake. A merge is refused while an alert is pending.
- While dispatch is suspended the database issues no alert and claims no alert attempt (`op.issue_alert` and `op.begin_attempt` share-lock `op.dispatch_control`, which a suspension locks exclusively); an alert attempt is claimed only for a pending alert. An alert recorded just before a suspension therefore lapses undelivered (PR #10 review I1).
- `op.dispatch_escalate` keys its event `escalate:<reason>:<role or ->` (`incident.EscalationKey`): once per reason and role. Intake uses the same key for `dispatch_suspended`, so the coordinator is alerted once (review I5, M7).

### Consequences

* Good: the tables and the log cannot disagree. Concurrent accepts give exactly one assignment. A crash or failover never repeats an alert. The guarantees are database constraints, so they hold whatever the worker does.
* Bad: each new dispatch command is a migration with its own function and grants. `op.append_event_core` is a third copy of the write-path body (migrations are immutable history).
* Neutral: alert idempotency is by (incident, responder), stronger than a per-ring key.

### Confirmation

`internal/platform/pg/opstore/dispatch_integration_test.go` (`TestConcurrentAcceptsOneAssignment`, `TestIssueAlertGuards`, `TestDispatchFactsOnlyThroughTheirFunctions`, `TestDispatchLifecycleIsRecordedAndConsistent`, `TestAttemptsAreClaimedOnce`); `internal/platform/pg/opstore/opstore_integration_test.go::TestProjectionMatchesFold`, `::TestTransitionsTableMatchesGo`; `internal/incident/dispatch_test.go::TestDispatchEventPayloads`, `::TestAlertsContinueOnScene`; `internal/platform/wiring/dispatch_integration_test.go::TestEngineCrashBetweenRecordAndDelivery`, `::TestEngineFailoverMidCascade`, `::TestEngineSettlesAttempts`, `::TestEngineDoesNotDeliverAfterADatabaseSuspension`, `::TestEngineDeliversStandDownsAfterClosure`; `internal/platform/pg/opstore/review10_integration_test.go::TestEscalationsKeyedByReasonAndRole`, `::TestNoAlertOrAttemptWhileSuspended`, `::TestCloseAndMergeWakeDispatch`.
