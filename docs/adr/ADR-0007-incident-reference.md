---
status: accepted
date: 2026-11-12
decision-makers: Sauti engineering (repository owner)
consulted: Engineering Guide V2 §10 (8+1 proposal)
informed: Principal Investigator, Data Protection Officer
---
# ADR-0007: Incident reference: nine random digits plus a Damm check digit, read 3-3-4

## Context and Problem Statement

The incident reference joins the three MEDS channels (system, Tool 2, paper Tool 3 slip). It must be spoken on a phone line, keyed on a basic handset, hand-written and carry no personal data (ISP §5.2). The Build Prompt (§9) fixes 9 digits + Damm, read 3-3-4; the Guide's earlier proposal was 8 + Damm; the Guide leaves the implementation to engineering.

## Decision Drivers

* Detect every single-digit error and adjacent transposition
* Keypad- and voice-friendly

## Considered Options

* 9 random digits + Damm, spoken 3-3-4 (Build Prompt)
* 8 digits + Damm, spoken in fours (Guide proposal)
* Crockford Base32 with mod-37 check

## Decision Outcome

Chosen option: **9 random digits + Damm** from `crypto/rand` with rejection sampling; a database unique constraint with up to five retries handles collisions. Digits only; Crockford Base32 remains available for printed slips if letters are ever needed.

## Consequences

* Good: 10⁹ space keeps collisions negligible at pilot scale; every single error and transposition is caught.
* Bad: ten digits is one more than the Guide's proposal.

## Confirmation

`internal/incident/damm_test.go::TestDammDetectsEverySingleDigitError`, `::TestDammDetectsEveryAdjacentTransposition`; `internal/incident/service_test.go::TestStartSessionRetriesReferenceCollision`.
