---
status: accepted
date: 2026-11-13
decision-makers: Sauti engineering (repository owner)
consulted: Build Prompt §7 (durable timers), §10.4, §16 (synctest for every timer), §19 decision 2; spec §4.2, §14; Engineering Guide §6
informed: technical operator, Medical Director, PI
---
# ADR-0024: Dispatch as pure planners and a PostgreSQL scheduler

Supersedes the DBOS Transact executor named in spec §4.2 and §14. The spec's "functional core" principle stands: only the executor changes.

## Context and Problem Statement

Dispatch is a set of timers: 60-second ring steps, a 90-second cascade per responder, 60 seconds for a health centre to acknowledge, and the 3-minute red escalation. Each one must survive a restart or a database failover, with no lost and no duplicate alert (scenario 14). It must also be deterministic (rule 3.3) and testable without waiting in real time (§16). The spec chose DBOS Transact workflows. What should run the timers, and where does dispatch state live?

## Decision Drivers

* Every dispatch fact is already a row behind the operational store's single write path (ADR-0003, ADR-0025): idempotent, audited, verified by `verify-projections`.
* A second, serialised workflow state (DBOS's tables) would sit outside those verifiers and duplicate what the write path already guarantees.
* Timers must be deterministic and testable: same facts and same time in, same actions out.
* Study mode (decision 2) changes the ladder's shape, so the ladder must be data-driven, not hard-wired into workflow code.

## Considered Options

* **Pure planner + a timer row per incident + a leader-elected worker** (in-house, about 400 lines).
* DBOS Transact workflows driving the same planner.
* River or another Go job queue.

## Decision Outcome

Chosen option: **pure planner + PostgreSQL scheduler**.
- `dispatch.Plan(state, pool, facilities, params, now) → (actions, next wake)` is a pure function. Inputs: the stored facts (alerts, assignments, forwards, escalations, caller notices), the ranked pool and the time. Outputs: alert, expire, stand down, escalate, forward, forward timeout and caller SMS. It encodes Build Prompt §10.4 and the decision 2 protocol answer. In protocol mode it stays at ring 1 and escalates instead of widening; amended mode is rings to 2.5 km and forwarding.
- `op.dispatch_timers` holds one wake per incident in dispatch. T2 or a split starts it; a merge or closure ends it. A fact that needs a fresh plan (acceptance, decline, acknowledgement, suspension change) wakes it at once and bumps its generation. A worker that planned on older facts therefore never pushes the wake back (`op.schedule_dispatch`).
- `dispatch.Engine` claims due incidents, most urgent first, with a 30-second lease (`FOR UPDATE SKIP LOCKED`). Each round it re-reads the incident's facts and takes *now* from the database clock (rule 3.4). It plans, records each action through its dispatch function, then delivers what the store shows as undelivered and stores the next wake. It keeps no dispatch state in memory.
- `cmd/dispatch-worker` runs one leader (advisory lock 7347004) that polls every second.
- A pass that fails is escalated to the coordinator (`dispatch_error`) and retried when its lease expires (rule 3.14).

### Consequences

* Good: no new runtime dependency. The planner is tested by a simulator for every row of §10.4, scenarios 2, 7 and 8, both modes, and suspension. The engine is tested against the real store through crashes and a failover. The worker can stop at any point and its successor resumes from the store.
* Good: the ring, cascade and forwarding times are gated configuration, and the dispatch logic version (`<ranking_version>+sha256:<12 hex of the gated config>`) is on every dispatch fact.
* Bad: Sauti maintains the scheduler. Its guarantees rest on the lease, the generation guard and idempotent functions, and each has a test.
* Neutral: if DBOS is adopted later, it can drive the same planner; the planner does not depend on its executor.

### Confirmation

`internal/dispatch/plan_test.go` (`TestRedAlertsTopThreePerRoleInParallel`, `TestRedAlertsTopThreeNotYetAlertedAtEachRingStep`, `TestScenario2YellowCascadeDepthThree`, `TestScenario7NobodyAcceptsForwardsToNearest`, `TestScenario8NoAcknowledgementTriesNextNearest`, `TestNoCentreAcknowledgesEscalates`, `TestProtocolModeStaysAtRingOne`, `TestNoEligibleEscalatesImmediately`, `TestAcceptanceStandsDownOthers`, `TestSuspendedNeverAlertsAndEscalates`, `TestSuspensionMidCascade`, `TestDeclineMovesCascadeOn`, `TestPlanIsIdempotent`, `TestTerminalIncidentsPlanNothing`, `TestUnknownSeverityIsRedAndNoT2PlansNothing`, `TestParamsValidate`); `internal/dispatch/engine_test.go::TestRunPollsOnItsInterval` (testing/synctest); `internal/platform/pg/opstore/dispatch_integration_test.go::TestDispatchTimers`; `internal/platform/wiring/dispatch_integration_test.go` (`TestEngineYellowCascadeDepthThree`, `TestEngineCrashBetweenRecordAndDelivery`, `TestEngineFailoverMidCascade`, `TestEngineSuspendedMidCascade`, `TestEngineNoEligibleEscalatesAtOnce`, `TestEngineRunOnceEscalatesAnUnprocessableIncident`, `TestEngineMovesOnWhenTheDatabaseRefusesAResponder`); `internal/platform/wiring/dispatch_test.go::TestDispatchParamsFromConfig`, `::TestDispatchLogicVersion`.

## More Information

Rulings recorded during M2 Part A:
- "Newly eligible" at a red ring step means not yet alerted within the wider ring, up to 3 per role per step, so a closer leftover responder is never skipped.
- The red 3-minute coordinator alert applies in both modes.
- The caller's "forwarded" SMS names the centre that acknowledged.
- An incident with only a ward-level location searches from every zone of the ward.
