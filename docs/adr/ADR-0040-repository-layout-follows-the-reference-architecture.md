---
status: accepted
date: 2026-11-14
decision-makers: Sauti engineering (repository owner)
consulted: Scalable MVP Stack, §Repository structure; spec §5
informed: Principal Investigator, emergency-medicine co-investigator
---
# ADR-0040: Repository layout follows the reference architecture

## Context and Problem Statement

The Scalable MVP Stack, §Repository structure, sets out the layout of the monorepo: one Go module with one binary per process under `cmd/`, private domain modules under `internal/`, and separate homes for the console contracts and app, triage rules, per-store database files, telephony, analytics, deployment, tests and docs. Spec §5 adopted it.

The repository has grown milestone by milestone. Most of it matches, but locations that later milestones build do not exist yet, and some directories here are not in the reference. Nothing records which differences are deliberate, so a reader cannot tell a planned gap from drift. How does the repository show where it stands against the reference, and keep that true?

## Decision Drivers

* A contributor should find where new code goes before the milestone that builds it starts.
* Each deliberate difference needs a recorded reason.
* A record of the layout must fail CI when it drifts, like every other living document (ADR-0016).
* Moving Go code now would break open pull requests that import it.

## Considered Options

* **Scaffold every missing reference location with a README, record the layout in one reference table, and test the table against the tree.**
* Create each location only when its milestone starts: rejected, because the gaps stay invisible and unexplained until then.
* Move the code to match the reference now, including the USSD simulator: rejected for this phase, because open pull requests import the simulator from `internal/platform/sim/`.

## Decision Outcome

Chosen option: **scaffold, record and test**, in two phases.

**Phase A (this record):**
* Each missing reference location gets a short README with its purpose, the milestone that builds it, its owner and its hard constraints. No Go file is added, so the build and archcheck are unchanged.
* `deploy/` gets one README naming its planned `ansible/`, `quadlet/`, `haproxy/` and `patroni/` homes, without creating them.
* [`docs/reference/repository-layout.md`](../reference/repository-layout.md) lists every reference location with its path here and its status: present, planned (with the milestone), or a deviation (with the reason).

**Phase B** moves the USSD simulator from `internal/platform/sim/` to `tests/`, after the pull requests that import it merge.

The recorded deviations are kept, not removed:
* `governance/` and `threat-model/`, required by the owner's engineering rules;
* `config/`, the runtime and gated configuration (ADR-0014);
* `tools/`, `scripts/` and `build/`, the CI pipeline's tools;
* `.github/`, by the hosting decision (ADR-0008);
* `db/media/`, the fifth store (ADR-0006);
* the additional modules and processes of spec §4.2 and §4.3.

## Consequences

* Good: every reference location has a home and a named milestone; a deviation without a reason fails review.
* Good: the README placeholders cost nothing at build time, because Go ignores directories without Go files.
* Bad: the README placeholders can go stale when a milestone moves. The layout table is tested; the READMEs' prose is not.
* Neutral: a new top-level directory now needs a row in the layout table, and a new context still needs its archcheck allowlist entry (ADR-0002).

## Confirmation

* `tools/archcheck` (rules R1–R4) still reports zero violations (`scripts/ci/arch.sh`; `tools/archcheck/check_test.go::TestRealModulePasses`).
* `governance/tests/test_repository_layout.py::test_layout_doc_matches_the_tree` fails when a present or planned path in `docs/reference/repository-layout.md` is not a directory, or when a top-level directory in Git is missing from it. It runs in `scripts/ci/governance.sh`.
