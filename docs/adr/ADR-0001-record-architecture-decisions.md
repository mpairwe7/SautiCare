---
status: accepted
date: 2026-11-12
decision-makers: Sauti engineering (repository owner)
consulted: —
informed: Principal Investigator, Data Protection Officer
---
# ADR-0001: Record architecture decisions with MADR 4.0

## Context and Problem Statement

The Build Prompt (§8) and the global engineering rules require every architectural, protocol-affecting, security or dependency decision to be recorded where engineers and auditors (ethics committee, DPO, ISO/IEC 42001 auditors) can find it, in the same pull request as the change.

## Decision Drivers

* Traceability for a clinical service with an embedded research programme
* Decisions must survive staff turnover and be reviewable as code

## Considered Options

* MADR 4.0 records in `docs/adr/`
* Free-form wiki pages
* Commit messages only

## Decision Outcome

Chosen option: **MADR 4.0 records in `docs/adr/`**, indexed in `docs/adr/README.md`, with the mandatory sections Context and Problem Statement, Decision Outcome, Consequences and Confirmation. A superseded ADR is never edited; a new ADR supersedes it.

## Consequences

* Good: decisions are versioned, reviewed and linked from code comments.
* Bad: writing ADRs costs time on every architectural change.

## Confirmation

`python3 -m tools.freshness --check` fails CI if an ADR is missing a mandatory section or is absent from the index (`governance/tests/test_freshness.py::test_adr_missing_heading_fails`).
