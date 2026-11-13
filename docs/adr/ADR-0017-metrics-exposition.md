---
status: accepted
date: 2026-11-12
decision-makers: Sauti engineering (repository owner)
consulted: —
informed: Principal Investigator, Data Protection Officer
---
# ADR-0017: In-house Prometheus metrics until the OpenTelemetry SDK

## Context and Problem Statement

The stack names the OpenTelemetry Go SDK. M0 needs four metrics on one process; the SDK plus its Prometheus exporter would add a large dependency tree before tracing is used.

## Decision Drivers

* Few dependencies; standard library first

## Considered Options

* About 100 lines of in-house Prometheus text exposition
* OpenTelemetry SDK now

## Decision Outcome

Chosen: **`internal/platform/metrics`** (counters and gauges, text format). The OpenTelemetry SDK is adopted when distributed tracing is introduced across intake, dispatch and telephony (M1–M2), with the collector masking phone numbers before storage.

## Consequences

* Good: zero dependencies now.
* Bad: a later migration of metric registration call sites.

## Confirmation

`internal/platform/metrics/metrics_test.go::TestPrometheusExposition`.
