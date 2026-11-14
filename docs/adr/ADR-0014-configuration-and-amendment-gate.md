---
status: accepted; superseded in part by ADR-0042 (2026-11-14)
date: 2026-11-12
decision-makers: Sauti engineering (repository owner)
consulted: Study Protocol change control (Build Prompt rule 3.11)
informed: Principal Investigator, Data Protection Officer
---
# ADR-0014: Versioned configuration, fingerprints and the amendment gate

## Context and Problem Statement

Triage rules, the Tool 2 list, dispatch parameters, SMS content, decline reasons and section 11 flags are frozen while `study_window_active` is true; changes need an `amendment_approved` record. Parameters field data may move must be configuration, not constants (Guide §19).

## Decision Drivers

* Every incident records the configuration version that produced it
* Changes during the study are impossible without an ethics amendment

## Considered Options

* YAML under `config/gated/`, strict decoding, sha256 fingerprint, CI gate keyed to content hashes
* Database-held configuration edited in the admin screen only
* Environment variables

## Decision Outcome

Chosen option: **versioned YAML in Git** (`config/`), decoded strictly (unknown keys fail). The fingerprint is sha256 over every gated file's path and content and is the dispatch-parameter version. `tools/amendgate` fails CI when, with the window active at base or head, a gated path changes without an amendment whose recorded sha256 equals the new content. Hardening after the CodeRabbit review of PR #2:
- **The change under review cannot shrink its own gate.** The effective gate is the union of a built-in minimum (`config/gated/**`, `config/study.yaml`, `triage-rules/**`, `governance/gated-paths.txt`), the gate list at the base ref and the gate list in the change. Editing the list is itself gated.
- **Hashes cover the tracked content.** They cover the blob at HEAD, not whatever a working-tree path resolves to.
- **Gated paths must be regular files.** A symbolic link on a gated path is always a violation.
- **Push runs compare with the pre-push commit** (CodeRabbit PR #5). A pull request is compared with its target branch. A push to `main` is compared with `github.event.before`, the commit `main` held before the push; `origin/main` already equals the pushed commit, so a diff against it is empty. A push that creates the branch has no previous commit (an all-zero SHA), and `scripts/ci/governance.sh` then fails instead of checking nothing.

`config/platform.yaml` (checkpoint interval, batch sizes) is deliberately ungated operational tuning. Rule 3.11 freezes protocol parameters, not platform tuning. `ProductionReady()` refuses `protocol_status: pending` and placeholder copy (`XXX`, `PENDING-TRANSLATION`). Deployment settings stay in environment variables.

## Consequences

* Good: reviewable, signed history of every parameter.
* Bad: changing a parameter is a pull request, not a console edit (intended).
* Bad: CI runs the gate from the tree under review, so the gate cannot protect its own code. A change to `tools/amendgate/`, `scripts/ci/` or `.github/workflows/` could weaken the check that judges it. Adding those paths to the built-in minimum would not stop this, because the minimum is itself defined in `tools/amendgate/check.go`. It would also make every CI fix during the study window wait for an ethics amendment. These paths are protected outside the tree instead (CodeRabbit PR #5):
  * `.github/CODEOWNERS` names their owner: the PI, and in the interim the repository owner.
  * Branch protection on `main` must require code-owner review and a passing `governance and living docs` check, so removing the job blocks the merge rather than skipping it.

## Confirmation

`tools/amendgate/check_test.go::TestCheckFailsWhenHashDiffersFromAmendment`; `internal/config/config_test.go::TestProductionReadyFailsWhilePending`, `::TestLoadRejectsUnknownField`.
