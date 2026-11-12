---
status: accepted
date: 2026-11-12
decision-makers: Sauti engineering (repository owner)
consulted: Repository owner's global engineering rules
informed: Principal Investigator, Data Protection Officer
---
# ADR-0016: Living-documentation mapping of the global engineering rules

## Context and Problem Statement

The global rules name artefacts from another project (docs/RAG_ARCHITECTURE.md, `python3 -m app.freshness`). Sauti has no RAG and no LLM agent; the intent — Diátaxis, MADR, ISO/IEC 42001 §7.5, NIST AI RMF, model cards, AIBOM, docs-as-code gates — still applies.

## Decision Drivers

* Honour the intent without inventing irrelevant documents

## Considered Options

* Map each artefact to its Sauti equivalent and gate it in CI
* Create the named files verbatim

## Decision Outcome

Chosen: Diátaxis tree under `docs/` (tutorials, how-to, runbooks, reference, explanation); ADRs here; `docs/MODEL_CARD.md` treats the deterministic triage table as the "model" and lists the flagged-off Sunbird ASR; `governance/ai_risk_manifest.yaml` with `verified_by` and `aibom_inventory`; `threat-model/threats.yaml`; `docs/traceability.md`. Gates: `python3 -m tools.freshness --check` (replaces `app.freshness`), `python3 governance/compliance_check.py`, `python3 threat-model/validate_threats.py`. RAG_ARCHITECTURE.md and AGENT_ARCHITECTURE.md are not applicable; `docs/explanation/architecture.md` is the architecture explanation.

## Consequences

* Good: every requirement points at a test that exists, or is explicitly planned.
* Bad: documentation is part of every pull request's cost.

## Confirmation

CI job `governance` runs all gates and their unit tests (`governance/tests`).
