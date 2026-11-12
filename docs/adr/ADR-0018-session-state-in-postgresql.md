---
status: accepted
date: 2026-11-12
decision-makers: Sauti engineering (repository owner)
consulted: Build Prompt §7, §14; MVP stack doc (Valkey)
informed: Principal Investigator, technical operator
---
# ADR-0018: Tool 1 session state lives in PostgreSQL; Valkey is deferred

## Context and Problem Statement

USSD is stateless between callbacks, so Tool 1 session state must live on the server and survive a dropped session (Guide §4). The stack document and spec §4.2 place a Valkey cache in front of intake-gw. Every answer must also be persisted the moment it is given, as an event, through the single write path (ADR-0003). Where should the state that decides the next screen live?

## Decision Drivers

* An answer is durable before the next screen is shown (Guide §4: abandoned sessions still leave the record)
* Fewer components on the dispatch path; every component is a failure mode (rule 3.14)
* The USSD budget is 800 ms per callback (spec §7.5)

## Considered Options

* PostgreSQL only: the projection (`op.incidents`) holds the coded answers; `op.ussd_steps` holds the prompt last shown and the consumed input length
* Valkey session cache in front of PostgreSQL
* Valkey as the system of record for sessions

## Decision Outcome

Chosen option: **PostgreSQL only**. The next screen is a pure function of the incident projection (`intake.Machine.Next`); the prompt last shown and the input consumed are stored with each idempotent USSD step. A callback costs five short statements on one connection pool, well inside the 800 ms budget at pilot volume. Valkey stays in the architecture for later needs and is added behind the existing ports only if the k6 load test (M7: 200 concurrent sessions) shows PostgreSQL missing the p99 target.

Because the projection is the session state, partial failures are repaired from it (final review C1):
- An answer and what it implies by itself (the classification triple at screen 5; the out-of-area flag and closure) are one transaction (`incident.Service.AppendBatch`).
- What the answers imply but the log lacks (a classification, T2) is recorded by a projection-driven catch-up after every applied answer and whenever a session starts or resumes, with incident-scoped idempotency keys, so a retried callback or a redial on either channel converges.
- The closing screen is never shown without T2 (`intake.ErrNotDispatched`); a finished Tool 1 without T2 stays resumable and is swept as idle.

## Consequences

* Good: no cache coherence problem; a resumed session on either channel reads the same truth; one less dependency on the dispatch path.
* Bad: each callback reads the projection from PostgreSQL. Measured in M7; reversible.

## Confirmation

`internal/intake/service_test.go::TestUSSDResumesWithinWindow`, `::TestUSSDRetryWritesOneEvent`; `internal/intake/atomicity_test.go::TestEveryWriteFailureConverges`, `::TestResumeClassifiesAnIncidentThatLacksIt`, `::TestCompleteWithoutT2IsAnError`; `internal/platform/pg/opstore/batch_integration_test.go::TestAppendBatchIsAllOrNothing`; `tests/e2e/m1_intake_test.go::TestScenario5RedialResumesAtScreen6`.
