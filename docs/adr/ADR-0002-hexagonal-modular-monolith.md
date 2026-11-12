---
status: accepted
date: 2026-11-12
decision-makers: Sauti engineering (repository owner)
consulted: MVP stack document authors
informed: Principal Investigator, Data Protection Officer
---
# ADR-0002: Hexagonal modular monolith with enforced boundaries

## Context and Problem Statement

Sauti must be modular, testable under simulated telco, payment and database failures, and runnable by a two-engineer team without Kubernetes (MVP stack doc). The Build Prompt (§7–8) asks for one Go module, one binary per process, adapters behind ports, and a CI import-boundary check.

## Decision Drivers

* Simulate every external failure in tests
* Prevent accidental coupling (e.g. intake reading the analytic store)
* Keep a later move to Kubernetes a re-host, not a rewrite

## Considered Options

* A. Hexagonal modular monolith: bounded contexts in `internal/<context>`, adapters in `internal/platform`, composition roots in `cmd/`
* B. Classic layered architecture (domain/usecase/infrastructure)
* C. Full event sourcing with asynchronous CQRS projections

## Decision Outcome

Chosen option: **A**. Contexts hold pure domain logic and application services and declare ports as Go interfaces; adapters implement them; every input becomes one of the ten commands. B entangles contexts and cannot express "only the pseudonymiser joins the stores". C puts projection lag on the dispatch path and breaks "first acceptance wins by a database constraint".

## Consequences

* Good: every adapter can be faked; boundaries are machine-checked.
* Bad: more packages and interfaces than a single-package service.
* Additions to the Build Prompt layout: `outbox-relay` and `sautictl` processes; `intake`, `safety` and `config` contexts (spec §4).

## Confirmation

`tools/archcheck` (rules R1–R4) runs in CI (`scripts/ci/arch.sh`); `tools/archcheck/check_test.go::TestRealModulePasses` asserts the module has zero violations.
