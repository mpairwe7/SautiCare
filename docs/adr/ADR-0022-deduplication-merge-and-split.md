---
status: accepted
date: 2026-11-12
decision-makers: Sauti engineering (repository owner)
consulted: Build Prompt §10.3; Engineering Guide §10 (T1 is the primary outcome's start)
informed: Principal Investigator, Field Coordinator
---
# ADR-0022: Deduplication at T2 into the incident already dispatching

## Context and Problem Statement

Several bystanders often report one emergency. Build Prompt §10.3: calls with the same category, locations within one zone of each other, within 15 minutes, are merged automatically; the earliest call gives T1; every merge is logged; the coordinator can split one, also logged; no call is discarded. When and into which incident should a call merge, and how is T1, the start of the primary outcome, kept honest?

## Decision Drivers

* Never restart or duplicate a dispatch that is already alerting responders
* T1 of the surviving incident is the earliest call, and is recomputed exactly on a split
* Two calls completing at the same instant must not both survive or both merge

## Considered Options

* At T2, merge into the matching incident dispatched earliest; survivor T1 = earliest call through `incident.merge_absorbed`
* Merge into the incident with the earliest T1 (could be a call still at intake, restarting dispatch)
* Merge later, asynchronously (alerts for both calls go out in the meantime)

## Decision Outcome

Chosen option: **merge at T2 into the earliest-dispatched match**, in one transaction under an advisory lock (`opstore.DispatchOrMerge`).
- The new call's `incident.dispatch_ready` (T2) is recorded first.
- If a candidate matches (same category, Chebyshev zone distance ≤ `dedup_max_zone_distance`, first calls within `dedup_window`), the new call gets `incident.merged` and the survivor gets `incident.merge_absorbed` carrying the call's T1. The survivor's T1 becomes the earliest of its own call and every absorbed call.
- The rule itself is a pure function (`intake.DedupRule.Survivor`).
- A merged call's own caller keeps answering on the merged record (screen 8), because every answer is persisted; nothing else lands on it.
- A coordinator splits a wrong merge with `op.split_incident`, available only to the console role, which appends `incident.split` (staff) on the survivor and `incident.unmerged` (staff) on the member, atomically and idempotently. The survivor's T1 is recomputed from the calls that remain, and the member returns to dispatching.

## Consequences

* Good: alerts already out are never restarted; T1 stays the earliest call; every merge and split is an event in the log and the ledger.
* Bad: a merged call is not dispatched separately; a wrong merge relies on the coordinator noticing it (runbook `split-wrong-merge.md`). Until the console (M5), the split is `sautictl incident split`.

## Confirmation

`internal/intake/dedup_test.go::TestSurvivorIsEarliestDispatched`, `::TestNoMergeBeyondOneZone`, `::TestNoMergeAfter15Minutes`; `internal/incident/merge_test.go::TestMergeAbsorbedTakesEarliestT1`, `::TestSplitRestoresT1`; `internal/platform/pg/opstore/dedup_integration_test.go::TestConcurrentCompletionsOneSurvivor`, `::TestSplitIncident`; `internal/platform/pg/opstore/merge_integration_test.go::TestMergeSplitParity`; `tests/e2e/m1_dedup_test.go::TestScenario4ThreeBystandersOneIncident`.
