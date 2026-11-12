---
status: accepted
date: 2026-11-12
decision-makers: Sauti engineering (repository owner)
consulted: —
informed: Principal Investigator, Data Protection Officer
---
# ADR-0012: Test database environments

## Context and Problem Statement

The Build Prompt names Testcontainers. The development machine has no container runtime and no C compiler; integration tests must still run locally against a real PostgreSQL 18.

## Decision Drivers

* Same PostgreSQL major everywhere (18.6)
* No shipped dependency on test tooling

## Considered Options

* embedded-postgres locally, an official image (by digest) as a CI service, `SAUTI_TEST_PG_DSN` override
* Testcontainers everywhere
* A shared long-lived test database

## Decision Outcome

Chosen option: **embedded-postgres v2 (PostgreSQL 18.6) locally, `postgres:18.6` pinned by digest as a CI service**; `internal/platform/pg/pgtest` gives each test a freshly bootstrapped and migrated five-store cluster under a random suffix. Testcontainers v0.45 (Podman support) arrives in M1 for Valkey and failover tests that must kill a database mid-test.

## Consequences

* Good: real database semantics in every test; isolation per test.
* Bad: the first local run downloads the PostgreSQL binaries.

## Confirmation

CI job `integration` sets `SAUTI_TEST_PG_DSN`; every `*_integration_test.go` calls `pgtest.Main`.
