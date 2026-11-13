---
status: accepted
date: 2026-11-12
decision-makers: Sauti engineering (repository owner)
consulted: ISP §5.1–5.2; DPIA risk 2
informed: Principal Investigator, Data Protection Officer
---
# ADR-0006: Five stores with per-store roles

## Context and Problem Statement

Rule 3.9: operational, analytic, linkage vault, audit and media stores each have their own key and credentials; the linkage vault opens only to the PI and one delegate.

## Decision Drivers

* Separation "by design, not by promise" (ISP §4)
* Least privilege per process

## Considered Options

* Five databases, an owner role and least-privilege runtime roles each, CONNECT revoked from PUBLIC
* One database with five schemas
* Five clusters everywhere, including development

## Decision Outcome

Chosen option: **five databases** (`sauti_<store>`). Owner roles run migrations only; runtime roles get explicit grants; functions have EXECUTE revoked from PUBLIC. In development and CI the five share one PostgreSQL 18 server; in production analytic and linkage get their own clusters and network segments (P0). `sautictl dev-bootstrap` creates the layout and refuses production.

## Consequences

* Good: a role of one store cannot even connect to another.
* Bad: five sets of credentials to manage (OpenBao issues them from P0).

## Confirmation

`internal/platform/pg/bootstrap/bootstrap_integration_test.go::TestBootstrapIsolatesStores`; `internal/platform/pg/schemas_integration_test.go::TestLinkageRolesAreNarrow`; privilege matrix in `docs/reference/stores.md`.
