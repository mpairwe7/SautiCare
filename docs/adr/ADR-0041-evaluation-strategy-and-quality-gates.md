---
status: accepted
date: 2026-11-14
decision-makers: Sauti engineering (repository owner)
consulted: the November 2026 evaluation audit (five lenses, V-01 to V-41); the November 2026 UX audit; Build Prompt §3, §14, §15, §16; ADR-0014, ADR-0016, ADR-0019, ADR-0029, ADR-0034
informed: PI, Medical Director, Data Protection Officer, technical operator
---
# ADR-0041: Evaluation strategy and quality gates

## Context and Problem Statement

Sauti's tests, gates, drills and reviews were built milestone by milestone, each proving the feature in front of it. The [evaluation audit](../explanation/evaluation-audit-2026-11.md) asked one question of each mechanism: if this broke in a way that could hurt a caller, would something we run catch it, before release and in production? It found 41 gaps, 1 Critical and 17 High, with a common shape:
- The suite checks state well and checks what a person sees or receives poorly. Mutants that put the wrong landmark on a responder's screen, show a foreign caller's full number, start a search from the wrong ward, or drop a severity by one step all passed every test (V-04, V-08, V-09, V-10).
- Safety invariants are proven by one example each, and the clinical scenarios reach 7 of 30 input classes; only an oracle the engineers wrote catches under-triage (V-04, V-05). Nothing binds the label a caller reads to the code triage reads (V-06).
- No test runs the built binaries or a real provider (V-11, V-12). No alert rule exists, and the dead-man signal's wiring is untested (V-01).
- The evidence gates accept a pointer to a test that is skipped or never runs (V-15). Drill freshness is unchecked (V-24).
- The merge gate failed open and lived outside the repository (V-02).
- One fact was graded Medium by one lens and High by two (V-03), because nothing fixed how severity is judged.

How does Sauti decide what counts as evidence that it works, and which gates must hold before a change merges and before go-live?

## Decision Drivers

* Build Prompt §3: the hard constraints, above all "never reveal a caller's number", "no model lowers a severity" and "every failure path ends with a person", must each be proven by a mechanism that fails when the constraint is broken, not by one that merely exists.
* Build Prompt §14 and §16: service levels, drills and scenarios are commitments, so each needs a check that runs.
* Living documentation ([ADR-0016](ADR-0016-living-documentation-mapping.md)): a `verified_by` pointer is evidence to an ethics committee, a DPO and an auditor, so it must mean "this ran and passed".
* A small team and one AI reviewer on a rate-limited tier: the gates must be cheap to run and must fail closed, because nobody is watching them.
* No new runtime dependency, and no new record kind without the DPO.

## Considered Options

* **Eleven evaluation rules, each enforced by a mechanism in the repository, phased by consequence.**
* Raise coverage targets: rejected. Coverage was 71 to 92 % in the safety packages while the person-facing mutants lived; coverage measures what ran, not what was checked.
* Rely on code review to judge test quality: rejected. No human other than the author reviews today (V-03), and the AI reviewer is quota-limited; review is an input, not a gate.
* Buy a test-management or GRC platform: rejected. The evidence already lives in the repository as code and YAML; what is missing is that the gates read real results.

## Decision Outcome

Chosen option: **eleven rules, each with a mechanism in the repository.** The [evaluation-hardening plan](../superpowers/plans/2026-11-14-evaluation-hardening.md) builds them in phases EV-1 to EV-13; until a rule's phase lands, the audit entry it answers stays open.

1. **Test what a person sees or receives, not only state.** A test of a screen, message, call, log line or listing asserts its content against the facts it should carry: the incident's own landmark and category and no other, a mask with no digit run, the ward the search started from. Fixtures have at least two of everything a lookup chooses between (two wards, two landmarks, a Ugandan and a foreign number), so a wrong choice cannot pass by luck.
2. **Safety invariants are proven by property tests.** "Severity never falls", "an out-of-domain input is red with a rule error", "the fold refuses what SQL refuses" and "a parsed phone number re-parses to itself" are stated for every input class, as table tests over the whole domain or as fuzz properties, in Go and, where the database enforces the rule, in SQL. One example per layer is not a proof.
3. **Mutation testing guards the safety-critical packages, with a threshold.** The packages are `internal/triage`, `internal/incident`, `internal/dispatch`, `internal/intake`, `internal/audit`, `internal/pii`, `internal/respond` and `internal/platform/logging`. A pull request's changed lines in them must reach a test efficacy of 80 % (killed over killed plus lived). The job reports only until M3 merges, then it is required. A weekly full run publishes the survivors; each survivor on a line that guards a hard constraint is killed by a new test or recorded in the PR as equivalent, with the reason. Every fix proposed for a test gap names the mutant or fault it must catch.
4. **The clinical scenarios cover the decision table completely.** The expected severity of every combination of the declared triage domain is signed data, authored by the co-investigators independently of the rules and signed under its own context. A test fails if any combination lacks an expectation or the bundle disagrees. An oracle written by engineers is not clinical evidence. The labels a caller reads are bound to the codes triage reads by the bundle's signature.
5. **The provider fakes are checked by contract tests.** A fake (Asterisk ARI, Africa's Talking SMS and USSD, the dialplan) must match recorded behaviour of the real provider: golden event and response files, recorded with their date, that the fake replays and a test compares. The safety rules of the dialplan are proven by executing it in Asterisk, not by parsing it as text.
6. **The binaries are smoke-tested.** CI builds the production binaries, starts them against a real database, walks one scenario through a real route to its first alert, runs the real operator command, stops them with SIGTERM, scans their output for numbers, and proves the production refusal on the built artifacts.
7. **A `verified_by` pointer counts only if its test passed in CI.** The freshness gate reads the saved results of the CI run and fails a pointer whose test was skipped, did not run, or did not pass, wherever the pointer lives: the traceability matrix, the threat model or the AI risk manifest. A register's status field says what the pointer proves: "mitigated" allows no planned pointer, and "accepted" names a role and an issue.
8. **Alert rules are unit-tested.** Every alert rule lives in the repository with `promtool test rules` cases that show it firing and staying quiet, including when its input series is absent or stale. A rule that pages on a gauge is paired with a rule on the gauge's own freshness, and an always-firing Watchdog proves the path to the receiver.
9. **The merge gate fails closed and lives in the repository.** The gate that decides a merge is code under `tools/`, tested against a stub `gh`. Any error or unreadable answer refuses. It requires each expected check by name, and the branch ruleset to require them; a review by the AI reviewer of the head commit, with a quiet window; and each review thread settled by its last word. It never asks the reviewer to approve or dismisses a review, and it merges only the head it judged. GitHub's ruleset is a second gate, not a replacement: the repository gate also catches a weakened ruleset.
10. **Drills have enforced freshness.** A runbook states when it was last reviewed and last drilled, as dates; once `go_live` is set, a runbook whose drill is older than its declared cadence fails the gate. A drill writes a dated record.
11. **Severity is graded by the consequence to a person.** A gap's severity is the worst credible harm to a caller, responder or coordinator if it hides a defect: Critical if such a defect could delay or drop a live emergency, or expose a caller's identity, and would reach production undetected; High if it would be caught only by chance or late, by a person; Medium if it weakens confidence, traceability or compliance evidence with no direct safety path; Low for hygiene. The class says who acts: (a) code or CI now, (b) the PI or Medical Director, (c) the owner, DPO or operator, (d) P0 infrastructure or staging, (e) a named later milestone. No Critical gap may be open at go-live, and every High gap has a merged fix or a written acceptance by the person its class names.

### Consequences

* Good: each hard constraint gets a check that fails when the constraint breaks, and the audits' surviving mutants become regression tests.
* Good: the evidence trail (`verified_by`, the model card, the AIBOM, the threat model) states only what a CI run proved, so the ethics committee and DPO can rely on it.
* Good: merges are judged by code that is reviewed and tested like any other, and a deleted or weakened ruleset is caught at the next merge.
* Bad: CI takes longer: the binary smoke test, the Asterisk contract job, the rules job and the mutation job each add minutes, and the nightly workflow adds scheduled runs. The changed-lines mutation job keeps a pull request's cost proportional to its diff.
* Bad: clinical evidence now waits on people: the expected-severity table and the label sign-off need the co-investigators and translators, and production refuses until they sign. That is the point, and it is on the go-live path.
* Bad: the gate refuses more often than the scratch scripts did (rate-limited reviews, polite objections), so merges need more replies and re-runs.
* Neutral: CodeRabbit's status check stays out of the ruleset, because it passes when rate-limited; its review of the head commit is what counts.

### Confirmation

Each rule is confirmed by the mechanism its phase adds, named in the plan with its test:
- Rule 1: EV-5's responder-screen, masking and ward-level tests; each must kill the audit's mutants R1, R2, P1, P2, L1, L2 and D3.
- Rule 2: EV-1's `ParsePhone` re-parse property; EV-3's severity tables in Go and SQL, the fold fuzz property, fold-SQL parity in both directions and the strengthened `FuzzClassifyNeverPanics` oracle in `internal/triage/fuzz_test.go`. Today's single examples, `internal/incident/fold_test.go::TestApplyRejectsSeverityLowering` and `internal/platform/pg/opstore/opstore_integration_test.go::TestAppendRejectsSeverityLowering`, stay.
- Rule 3: EV-11's changed-lines mutation job and weekly run; the tool is pinned in `build/tools.mod` and listed in `docs/reference/dependencies.md`.
- Rule 4: EV-4's expectations test and label-binding check; `internal/triage/triage_test.go::TestRepositoryBundleVerifies` keeps verifying the signature.
- Rule 5: EV-9's Asterisk contract job and recorded fixtures.
- Rule 6: EV-6's binary smoke test and production-refusal step.
- Rule 7: EV-8's run-proof mode of `python3 -m tools.freshness`, fed by the integration job's saved results, and the status rules in `governance/compliance_check.py` and `threat-model/validate_threats.py`.
- Rule 8: EV-2's rules file and its `promtool` CI job.
- Rule 9: EV-1's gate and its stub-`gh` tests in the governance job; ADR-0042 records its rules.
- Rule 10: EV-12's runbook date and cadence checks in `tools/freshness/__init__.py`.
- Rule 11: the [evaluation audit](../explanation/evaluation-audit-2026-11.md) applies it; each later audit cites this record for its scale.
