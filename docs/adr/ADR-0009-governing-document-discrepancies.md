---
status: accepted
date: 2026-11-12
decision-makers: Sauti engineering (repository owner)
consulted: Repository owner, 2026-11-12 (spec §2)
informed: Principal Investigator, Data Protection Officer
---
# ADR-0009: Resolution of discrepancies between governing documents

## Context and Problem Statement

Build Prompt §0 says: where documents disagree, stop and ask. Five discrepancies were resolved with the user on 2026-11-12.

## Decision Drivers

* Never invent an answer to an open decision; make values configuration
* Record which definition is in force on every incident

## Considered Options

Per discrepancy: follow the Build Prompt, follow the Engineering Guide, or leave pending.

## Decision Outcome

* **D4 Redial resume window: 10 minutes** (Engineering Guide §4), not 30 (Build Prompt §10.1). Config `config/gated/intake.yaml` `resume_window`, behind the amendment gate.
* **D5 Dispatch trigger / T2: after screen 7** (Build Prompt §10.1). Config `dispatch_trigger_screen: 7`; the trigger is stamped on the dispatch-ready event. Flagged for the PI before the analysis plan is registered (Guide §17).
* **D6 Analytic enclave: PyMC stays available** for exploratory work only; the primary analysis is descriptive (Guide §11).
* **O9 Red alerting: top 3 of each role** (Protocol per the Guide), not "top 3" (Tools v1.1 header).
* **O10 External daily witness** added to the audit design (Guide §15).

## Consequences

* Good: decisions are explicit and reversible through the amendment gate.
* Bad: D5 moves T2 relative to the Guide's definition; the PI must confirm before registration.

## Confirmation

`internal/config/config_test.go::TestLoadRepoConfigDefaults` pins 10 minutes and screen 7; the witness is exercised by `internal/audit/checkpoint_test.go::TestCheckpointSignedAndWitnessed`.
