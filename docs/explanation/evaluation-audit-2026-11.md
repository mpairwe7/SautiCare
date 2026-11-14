# Evaluation audit, November 2026

**Type:** Explanation (Diátaxis) · **Baseline:** `main` at c4d6627 (M0–M2 and UX-1 merged) · **Decisions:** [ADR-0041](../adr/ADR-0041-evaluation-strategy-and-quality-gates.md) · **Fix plan:** `docs/superpowers/plans/2026-11-14-evaluation-hardening.md`

This document explains what five independent audits found in the way Sauti Dispatch proves that it works: its tests, gates, drills, metrics, reviews and evidence. It records how the findings were checked, merged and re-graded, and what is decided for each one. The [UX audit](ux-audit-2026-11.md) asked whether each path serves a person; this audit asks whether, if a path broke in a way that could hurt a person, anything we run would notice before release or in production. The fix plan and ADR-0041 argue from this record. It contains no clinical wording and quotes no governing document. The Build Prompt and the Scalable MVP Stack are cited by section only.

## Scope and method

**Five lenses, read-only.** Each audit read `main` at c4d6627, ran its probes in a scratch copy, and left the repository unchanged. Each followed the same prompt, reproduced in [Appendix A](#appendix-a-the-auditors-prompt).

| Lens | Subject | Source IDs | As filed |
|---|---|---|---|
| Test-suite efficacy | Unit, integration, e2e, fuzzing, mutation, fakes, flakes | EA-1 … EA-11 | 5 High, 6 Medium |
| Safety and clinical evaluation | Triage evaluation, the severity ratchet, signatures, hazards, usability, clinical metrics, AI components | ES-1 … ES-12 | 5 High, 6 Medium, 1 Low |
| Operations and resilience | Metrics, alert rules, SLOs, synthetic probes, failover, drills, clocks, the demo as a smoke test | EO-1 … EO-13 | 1 Critical, 4 High, 6 Medium, 2 Low |
| CI, supply chain, review and merge | Required checks, the merge gate, review, secrets, SBOM, provenance | EC-1 … EC-14 | 3 High, 5 Medium, 6 Low |
| Governance, security and privacy | `verified_by` gates, threat model, privilege tests, the DPO gate, AIBOM, ASVS | EG-1 … EG-18 | 4 High, 9 Medium, 5 Low |

**What the audits ran.** About forty hand mutants, each applied to a scratch copy, run against the packages that should catch it, and reverted; gremlins v0.6.0 over ten safety packages; three fuzz probes; offline simulations of the merge scripts against a stub `gh`; and the three governance gates against planted faults. A finding proven by a surviving mutant outranks one argued from the code. The evidence is kept outside the repository with the audits.

**How the findings were consolidated.**
1. **Verified.** Every Critical and High finding was spot-checked against `main` at c4d6627 for this document: the cited lines exist and say what the audit claims. Two citations are off by a few lines and are corrected below (EA-1 cites `internal/respond/ussd_test.go:145` for the start of `TestAcceptFromTheMenu`, which starts at line 134; EO-1's gauge loop is at `cmd/intake-gw/main.go:177-191`, as filed). The protection evidence of EC-1, EG-2 and EA-9 was true when filed and is partly superseded (below). No Critical or High finding was dropped for want of evidence.
2. **Merged.** Findings that describe one gap from several lenses became one canonical finding, V-01 to V-41, listing every source ID. Each of the 68 source findings maps to at least one canonical finding or to a UX-audit finding that already decides it ([Source mapping](#source-mapping)). Overlaps with the UX audit are named on each entry, and the decision says which plan owns the work, so nothing is built twice.
3. **Re-graded** by the worst credible harm to a caller, responder or coordinator if the gap hides a defect:
   - **Critical:** a defect that could delay or drop a live emergency, or expose a caller's identity, would reach production undetected.
   - **High:** such a defect would be caught only by chance or late: in production, by a person.
   - **Medium:** it weakens confidence, traceability or compliance evidence, with no direct patient-safety path.
   - **Low:** hygiene.
4. **Classified:**
   - **(a)** code or CI, now;
   - **(b)** a PI or clinical decision (the PI or the Medical Director);
   - **(c)** an owner, DPO or operator decision, including repository settings;
   - **(d)** P0 infrastructure or staging;
   - **(e)** a later milestone, which is named.

   Many findings carry two classes, for example a test that can be written now and a threshold only the PI can set. The summary counts each finding once, by the class that decides its next step; the entry lists every class.

**Events since the audits.**
- **`main` protection is restored.** On the owner's instruction, ruleset 27260742, "main protection", was created on 2026-11-14. It blocks deletion and non-fast-forward pushes, requires a pull request and resolved review threads, and requires nine strict status checks: the nine CI jobs by name. It has no bypass actors. Read through `gh api repos/mpairwe7/SautiCare/rules/branches/main` for this document. EC-1, EG-2 and EA-9 are therefore partly resolved. What remains is V-03: no human approval (`required_approving_review_count: 0`), CODEOWNERS not enforced (`require_code_owner_review: false`) and owned by the author's own account, and CodeRabbit's check not required, which is right, because that check passes when CodeRabbit is rate-limited.
- **Merges are frozen** until the merge gate fails closed, is tested, and is versioned in the repository (V-02). The `$id` bug in the scratch `resolve.py` (EC-2) has been fixed in the scratchpad; the other failure modes EC-2 proved remain until EV-1.
- **Open pull requests.** PR #15 (the demo) now carries the black-box edge suite and fixes a board error after a merge (a timer that never ended); its suite still skips in CI (V-11). PR #18 (edge-case verification, G-1) and PR #17 (layout) are open.

**Re-grades.**
- **EO-1 stays Critical (V-01).** It is latent today, because production refuses to start. But tier 3 is the only automated path to a person while ivr-controller is down ([ADR-0029](../adr/ADR-0029-interaction-standards-for-voice-ussd-sms.md) rule 8), and one refactor of the gauge loop plus one ivr-controller outage would leave a red escalation unheard with every test green. The release path checks nothing about alerting. EO-7 (no dispatch leader gauge) merges into it, since the fix is the same staleness-aware rule set.
- **EO-3 (High → Medium, V-25).** No synthetic probe exists yet, so no mechanism can be fooled today. The harm needs M7 to build a probe without a marker. The decision it needs (whether probes are excluded from the study and whether a probe marker is a new record kind) is a precondition of M7, recorded here.
- **ES-4 stays High, as the auditor re-graded it (V-07).** PR #18 filed G-1 as Medium. A yellow call is time-critical by the protocol's own definition, the drop point is where the operator's session limit falls, and each occurrence is silent.
- **EA-9 (Medium), EC-1 (High) and EG-2 (High)** graded one fact three ways. The part left after the ruleset (V-03) is High: a change can still merge with no human having read it.
- **EG-16 (Low) merges into V-09 (High)** and **EO-12 (Low) into V-11 (High)**: each is one more case of the same missing oracle.

**Corrections to the audits.**
- **EA-7 calls the `ParsePhone` counterexample harmless. It is not.** Screen 8 parses a typed call-back number with `pii.ParsePhone` (`internal/intake/machine.go:333`), so "0000000000", or any ten digits starting "00", is accepted as `+2560…`. Three paths later re-parse the stored value and reject it: the caller SMS (`internal/platform/channels/sms.go:132`), the bridge (`internal/platform/ivr/bridge.go:555`) and voice alerts (`internal/platform/ivr/alerts.go:190`), each with "the stored number is not a phone number". A caller with no number to give, who types zeros to get past the screen, is never asked again. It stays Medium, because the number reaches nobody either way, and is fixed in EV-1.
- **ES-9's fix would collide twice.** It proposes migration 0034, which the UX plan reserves for UX-3, and a seventh copy of the event-append function, which both open plans forbid outside M3's 0029. The plan uses 0039 and a trigger instead (EV-3).
- **EC-1 and EC-3 propose requiring CodeRabbit's status check.** That check passes as "Review rate limited" when no review happened, so requiring it proves nothing. The gate requires a CodeRabbit review of the head commit instead (V-02), and the old practice of posting a resolve command to obtain an approval stops.
- **UX-8 Task 8.6 places `FuzzParsePhone` in a package that does not exist** (internal/platform/pii; the package is `internal/pii`). EV-1 builds that target; UX-8 Task 8.6 drops it.
- **EO-1 and EO-2 propose an SLO page and a rules file that UX-8 Tasks 8.3 and 8.8 already plan,** under other names. The plan builds one rules file and one SLO page, whichever phase lands first, and the other extends it.

## Summary

| Severity | (a) now | (b) PI or clinical | (c) owner, DPO, operator | (d) P0 or staging | (e) later milestone | Total |
|---|---|---|---|---|---|---|
| Critical | 1 | 0 | 0 | 0 | 0 | 1 |
| High | 13 | 3 | 1 | 0 | 0 | 17 |
| Medium | 15 | 2 | 2 | 1 | 1 | 21 |
| Low | 2 | 0 | 0 | 0 | 0 | 2 |
| **Total** | **31** | **5** | **3** | **1** | **1** | **41** |

**The pattern.** The suite is strong where it checks state: the SQL guards, claim ordering, the triage domain, the audit chain, and dispatch through crashes. It is weak where it should check what a person sees, receives or is protected by: the landmark on a responder's screen, a masked foreign number, the ward a search starts from, a one-step severity drop, a swapped screen label, the alert that should page someone when the coordinator line is dead. It never runs the built binaries or a real provider, and its evidence gates accept a pointer to a test that never ran. The merge gate that should stop all of this failed open.

**Phases of the fix plan.** EV-1 a fail-closed merge gate and a canonical `ParsePhone`; EV-2 the dead-man alert rules; EV-3 safety invariants as properties; EV-4 complete and bound clinical evidence; EV-5 what a person sees and receives; EV-6 the binaries in CI; EV-7 the database's privilege and record-kind gates; EV-8 evidence gates that require a passing run; EV-9 provider contracts; EV-10 latency and failover; EV-11 mutation, flakes, race and the schedule; EV-12 drills, clocks and timelines; EV-13 supply chain. Classes (b) to (e) are listed under [Recommendations](#recommendations-to-the-owners) and the plan's deferred list.

## Regulatory status, as verified on 2026-11-14

The auditors verified two facts that change how the evidence is planned. Both were re-verified for this document.
- **IEC 62304 Edition 2 is not published.** The IEC webstore lists IEC 62304:2006+AMD1:2015, consolidated edition 1.1, published 2015-06-26, stability date 2028, and no second edition: https://webstore.iec.ch/en/publication/22794. VDE (22 July 2026) describes Edition 2 as a working draft with the next committee draft expected in August 2026: https://www.vde.com/topics-en/health/expertise/iec-62304-edition-2-changes. Vendor claims of an August 2026 publication are unconfirmed, and forecasts range from 2027 to 2029. If the PI and sponsor adopt IEC 62304 (V-32), it is edition 1.1.
- **The EU AI Act's Annex III date moves to 2 December 2027.** Regulation (EU) 2026/1744 of 8 July 2026, the Digital Omnibus on AI, entered into force on 27 July 2026. Its Article 1 point 40(b) applies Chapter III Sections 1 to 3 from 2 December 2027 to systems classified high-risk under Article 6(2) and Annex III: https://artificialintelligenceact.eu/ai-act-explorer/digital-omnibus/. Annex III point 5(d) names emergency-call evaluation and triage. Whether the Act reaches Sauti at all (deployment in Uganda; a deterministic table written by people may fall outside the Article 3(1) definition) is a legal question for the DPO and the PI (V-33); the spec adopts the Act as documentation practice.

Every other standard below is cited with the URL and version its source audit verified on 2026-11-14. Where an audit could not open a page, the entry says so.

## Findings: Critical and High

Each entry gives the sources, the evidence on `main`, the failure scenario, the standard, and the decision with its phase.

### V-01 · Critical · (a), (d) for paging · Tier 3 is unalerted, its wiring is untested, and the planned rule cannot see a dead signal
- **Sources:** EO-1, EO-7. **Overlaps:** F-67 (UX-8 Task 8.3, the rules file).
- **Evidence:**
  - No rule file exists in the repository.
  - Tiers 1 and 2 are placed by ivr-controller; tier 3 is the dead-man gauge in intake-gw, which survives an ivr-controller outage ([ADR-0029](../adr/ADR-0029-interaction-standards-for-voice-ussd-sms.md) rule 8).
  - The goroutine that updates the gauges in `run()` (`cmd/intake-gw/main.go:177-191`) is reached by no test. With its update call removed, `go test ./cmd/intake-gw/` passes (mutant M3); the tests exercise the gauge methods only.
  - The rule UX-8 Task 8.3 plans, oldest unacknowledged age above 600 s, never fires when intake-gw is down (the series is absent) or when no instance leads (every instance exports 0). The leader gauge built for exactly this case is read by no planned rule.
  - dispatch-worker exports its last-pass age as 0 on a standby (`cmd/dispatch-worker/main.go:116`) and when it does not lead (`:136-142`), so "no leader anywhere" reads as healthy, and the overdue gauge freezes on a failed read (`:132-134`).
- **Failure scenario:** ivr-controller is down. A red incident escalates "no eligible responder". Tiers 1 and 2 live in the dead process, so no coordinator is rung. Tier 3 is either unwired by a refactor that passed CI, or alerted by a rule blind to a stale signal. The caller waits, and nobody is told.
- **Standard:** Prometheus, unit testing for rules (latest docs, © 2014–2026): https://prometheus.io/docs/prometheus/latest/configuration/unit_testing_rules/ · Prometheus `absent()`: https://prometheus.io/docs/prometheus/latest/querying/functions/ · prometheus-operator Watchdog runbook: https://runbooks.prometheus-operator.dev/runbooks/general/watchdog
- **Decision:** EV-2. Extract the leader's term (the dispatch watchdog, the gauge loop and the sweeper) into a function whose test reads the exported gauges and fails under mutant M3 and with the gauge loop's call removed; EV-6's binary smoke test reads the gauges from the running intake-gw, so a leader callback that never starts the loop fails too. Add a dispatch leader gauge; commit the rules file with staleness-aware rules (signal stale, dispatch leader missing, an always-firing Watchdog) and `promtool test rules` cases in CI. UX-8 Task 8.3 then adds its burn-rate rules to the same file. Routing the page and the Watchdog to a receiver off the monitoring host is P0 (R-16). The Watchdog cannot report an outage of the Prometheus and Alertmanager that send it, so that receiver is a dead man's switch that pages a person when the Watchdog stops arriving, proven by a drill that stops Alertmanager.

### V-02 · High · (a) · The merge gate is untracked scratch scripts, and it fails open

**Status:** fixed in EV-1: `tools/mergegate`, [ADR-0042](../adr/ADR-0042-fail-closed-merge-gate.md).
- **Sources:** EC-2.
- **Evidence:** the gate was three scripts in a session scratchpad, outside Git, run by no CI. Run offline against a stub `gh`, it merged in five of six scenarios that should stop it:
  - `gh pr checks` failing ("no checks reported", or an API error) read as an empty list: merged;
  - a check list missing "governance and living docs" and "unit tests (race)": merged, because no expected list was compared;
  - the thread query rate-limited: merged on a stale earlier result;
  - `reviewDecision` unreadable (HTTP 502): merged, because empty was read as "not changes requested";
  - CodeRabbit never quiet for 30 minutes: merged once the quiet rule lapsed;
  - control, a failing governance check: refused.

  It merged without `--match-head-commit`, so a push after the checks could merge unchecked commits. Its thread judge read prose by regex and settled "this looks good, but the failed-Dial path … remain unverified" (the regex knew "remains", not "remain"), "is addressed; however the coordinator dial is not escalated…", and a drive-by "ok" from any GitHub user on this public repository. It posted the CodeRabbit resolve command, which closes every CodeRabbit thread and can approve. On PR #13, 15 CodeRabbit replies arrived after the merge, several of them "not marking this addressed".
- **Failure scenario:** an API hiccup or CodeRabbit's hourly quota coincides with a merge. A PR merges while its integration job is still running, or over an objection such as "the failed-Dial path remains unverified", which was a real UX-1 objection about calls whose every coordinator dial fails.
- **Standard:** OpenSSF OSPS Baseline v2026.08.28, OSPS-QA-03.01 (status checks must pass before merge): https://baseline.openssf.org/versions/2026-08-28-checklist.md · NIST SP 800-218 SSDF v1.1, PO.4: https://csrc.nist.gov/pubs/sp/800/218/final
- **Decision:** EV-1, the first phase, which also lifts the merge freeze. A Python gate under `tools/`, unit-tested in the governance job against a stub `gh`. It fails closed on any `gh` error or unreadable answer; requires every expected check by name, and the ruleset to require them too; requires CodeRabbit's review of the head commit and a quiet window; judges each thread by its last word; never posts a command to CodeRabbit or dismisses a review; and merges with `gh pr merge --match-head-commit`. Recorded in ADR-0042.

### V-03 · High · (c), (a) for the record · Protection is partly restored: nobody but the author reviews, and the docs say otherwise

**Status:** protection restored (ruleset 27260742); code-owner review and a second approver remain owner decision O-1, and the docs now say so (EV-1).
- **Sources:** EC-1, EG-2, EA-9, EC-3, EC-6. **Overlaps:** F-76 (R-12).
- **Evidence:**
  - Ruleset 27260742 (above) requires checks and resolved threads but `required_approving_review_count: 0` and `require_code_owner_review: false`.
  - `.github/CODEOWNERS` assigns every path, `/triage-rules/`, `/config/` and `/tools/amendgate/` included, to the account that writes the PRs.
  - Across merged PRs #2 to #14: zero human approvals; CodeRabbit 4 approvals, 12 dismissals (11 within 41 seconds). The only collaborator cannot approve their own PRs.
  - CodeRabbit's open-source tier gives 1 to 10 reviews an hour by star count; a rate-limited PR gets a passing check titled "Review rate limited". PR #18 was never reviewed; PR #17's head was not.
  - Thread resolution is required, but a thread can be resolved by its author over an objection: PR #13 has 26 of 26 threads resolved, 8 of them ending in a CodeRabbit objection.
  - The docs claim controls that do not operate: `docs/MODEL_CARD.md:36` (a co-investigator approves rule changes "(CODEOWNERS)"), `docs/runbooks/database-migrations.md:52`, `docs/how-to/add-a-migration.md:14`, `docs/explanation/security-model.md:18`, and [ADR-0014](../adr/ADR-0014-configuration-and-amendment-gate.md), whose named compensating control is code-owner review.
  - An amendment record is self-asserted: `tools/amendgate/check.go:79-85` accepts any non-empty reference and a date. The study window is inactive (`config/study.yaml:4`), so this bites only once the study starts.
- **Failure scenario:** a subtle triage-ordering or timer defect written by the agent is missed by CodeRabbit, or never reviewed because the quota ran out. It merges with no human having read it. An ethics committee reading the model card believes a clinician signed off a rule change that nobody clinical saw.
- **Standard:** OSPS Baseline v2026.08.28, OSPS-QA-07.01 (one non-author human approval; level 3): https://baseline.openssf.org/versions/2026-08-28-checklist.md · OpenSSF Scorecard Code-Review (bot reviews do not count): https://github.com/ossf/scorecard/blob/main/docs/checks.md · GitHub rulesets: https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets · CodeRabbit rate limits (read 2026-11-14): https://docs.coderabbit.ai/management/rate-limits
- **Decision:** the owner's (O-1, O-2). Until a second human can approve, every merge needs the owner's explicit decision, recorded on the PR. CodeRabbit stays advisory: its review is required by the gate, its check is not. (a) parts: EV-1 makes the gate refuse when the ruleset no longer requires the expected checks (a deleted ruleset fails the next merge), and marks each doc claim above "not enforced until O-1"; ADR-0042 supersedes ADR-0014 in part on the compensating control. Signed amendment records are deferred until before the study window opens (plan, deferred list).

### V-04 · High · (a) · The safety invariants are proven by single examples
- **Sources:** ES-1, EA-11, ES-11, ES-10 (the import rule).
- **Evidence:**
  - **The severity ratchet.** `internal/incident/fold_test.go:110-119` and `internal/platform/pg/opstore/opstore_integration_test.go:219-231` each test red to green only; no other test names `ErrSeverityLowered`. Three mutants that allow a one-step drop pass every test: the Go guard at `internal/incident/fold.go:65` weakened by one rank; `severity_raised` allowed to lower (`fold.go:155`); and the SQL guard at `db/operational/migrations/0018_review_pr10.sql:107` weakened by one rank. A control mutant that disables the SQL guard is caught, so the tests reach the SQL.
  - The append function has been copied whole into six migrations; M3's 0029 makes a seventh. Each copy is a chance to weaken the guard that only the red-to-green example would notice.
  - **The fold accepts what SQL refuses.** A classification with no valid severity passes the fold (`fold.go:62`, mutant I1 lived through the unit, `cmd/sautictl` and opstore suites). Parity is tested in one direction only.
  - **Fail-safe red.** `internal/triage/fuzz_test.go` asserts only that the severity is one of three. A mutant that checks the domain of two inputs out of four classifies an out-of-domain `specific_danger` green with no rule error, and passes the unit tests and 15 s of fuzzing.
  - Nothing structural stops a future package (the P2 speech assist) from importing `internal/triage`.
- **Failure scenario:** a later migration, M3's Tool 2 scene severity or an M5 coordinator action, weakens the guard to allow a one-step drop. A red incident becomes yellow: one responder at a time for 90 s each instead of three in parallel, and no 3-minute red escalation. CI stays green, and AIR-001 and its threat entry still read "verified".
- **Standard:** NASA/TM-2001-210876, MC/DC (May 2001): https://ntrs.nasa.gov/citations/20010057789 · NIST AI RMF 1.0 Playbook, MEASURE 2.6 (the system fails safely): https://airc.nist.gov/airmf-resources/playbook/measure/
- **Decision:** EV-3. Table tests over every (from, to) pair in Go and SQL; a fold fuzz test that the rank never falls after any event sequence; fold-rejects-what-SQL-rejects parity; the fuzz oracle requires red with a rule error for any out-of-domain input; an archcheck rule that only intake, the wiring and `sautictl` import triage. EV-3 should merge before M3 Part A, so the seventh copy meets the new tests.

### V-05 · High · (a), (b) · The clinical scenarios cover 7 of 30 input classes, only an engineer-written oracle catches under-triage, and the totality check is vacuous
- **Sources:** ES-2; EA-10 (the triage survivor); ES-12 (the model card's wording).
- **Evidence:**
  - Seven bundle mutants that under-triage (child with danger unknown to green; trauma with a screen-5 danger sign to green; and five more) all survive `triage-rules/scenarios.yaml` and are killed only by `oracle()` (`internal/triage/triage_test.go:76-86`), a Go `switch` written by an engineer from the same text as the bundle. When Tool 1 arrives (O11), an engineer must rewrite it, most likely from the new bundle, and the two encodings then share every mistake.
  - The scenarios have 7 rows, no unique-cause pair for danger yes against no, and no row for danger unknown with a screen-5 yes.
  - Totality (`internal/triage/triage.go:249-253`) is satisfied by any table that ends with `when: "true"` (`triage-rules/tool1.yaml:35-37`), so a deleted rule falls through to green, not red. `docs/MODEL_CARD.md` §3 ("only a signed, total table runs") and §4 ("independent oracle") read stronger than they are.
  - The refusal of an empty or duplicate domain value (`triage.go:212`) is untested.
- **Failure scenario:** the approved Tool 1 bundle carries a plausible slip, an age-band clause on the red row. The engineer updates the oracle to match. All seven scenarios pass, the clinicians sign, and an elderly trauma patient with a danger sign is classified green on every call.
- **Standard:** NASA/TM-2001-210876 (MC/DC), as above · EU AI Act Art. 9(8), testing against prior defined metrics: https://artificialintelligenceact.eu/article/9/ (as design practice; see the regulatory note).
- **Decision:** EV-4. Expected severities for every combination of the declared domain become signed data, separate from the rules, under their own signature context and their own trusted keys, which the rules' signer does not hold; a test fails when any combination lacks an expectation or disagrees with the bundle; the Go oracle is deleted. The co-investigators author the real expectations independently of the rules, and decide whether "otherwise" stays green (P-2).

### V-06 · High · (a), (b) · What the caller reads is bound to what triage reads by nothing
- **Sources:** ES-3.
- **Evidence:** with the labels of the danger screen swapped in `config/gated/tool1.yaml:24-28` ("Yes" on value `no`), every unit test and the targeted e2e and wiring scenarios pass. `CheckTriageDomain` (`internal/intake/machine.go:423-448`) compares value sets only, and the e2e tests drive by digit. The bundle's signature covers rules over codes, not the catalogue that maps screens to codes. The Luganda labels are `PENDING-TRANSLATION`, so the clinicians who sign the rules may be unable to read the labels that decide the codes. The IVR recordings carry the same hazard.
- **Failure scenario:** a translation or editing slip puts "Yes" on value `no`. Every caller who presses "Yes" to the danger question is recorded as `danger = no`. A red call becomes green on every call, and CI and start-up both pass.
- **Standard:** IEC 62366-1:2015+AMD1:2020, edition 1.1, stability date 2028 (use-related risk; user interface specification): https://webstore.iec.ch/en/publication/67220
- **Decision:** EV-4. The signed bundle carries a hash of each screen's text per language: its question (for screen 5, each category's question with its category key) and its (value, label) list. Start-up and a unit test refuse a catalogue that differs; one e2e test walks Tool 1 by label text. The recordings are bound the same way: each recording's ID already names its screen and code (`danger-yes`), so what can differ is the audio. The bundle carries a hash of the approved recordings' digests, production ivr-controller refuses unapproved or mismatched recordings, and `sautictl prompts verify` checks a deployed sounds directory against them. Clinicians and translators sign questions, labels, recordings and codes together, with the back-translation recorded (P-3).

### V-07 · High · (b), (a) interim · A yellow or "I do not know" call dropped before the ward reaches no person
- **Sources:** ES-4; G-1 (PR #18, filed Medium). **Overlaps:** R-9 and R-3 of the UX audit.
- **Evidence:** `AbandonmentEscalation` (`internal/intake/sweeper.go:125-138`) escalates a danger yes, a red classification, or a ward given. A probe showed a poisoning classified yellow, a danger-unknown call classified yellow, and a danger-unknown call dropped at screen 5 each end with a flag only. `TestSweeperBeforeTheWardStillOnlyFlags` (`internal/intake/sweeper_test.go:235-245`) pins the flag-only result for a green call; no test states the yellow outcome as a decision. No list shows these calls to anyone.
- **Failure scenario:** a mother reports a child's poisoning, answers five screens, and the operator's session limit drops her at the ward screen. The incident is yellow, flagged abandoned, and nobody is rung.
- **Standard:** ISO 14971:2019, clauses 7 and 8 (risk control; residual risk), confirmed 2025; iso.org refused the fetch, so this rests on a search result citing https://www.iso.org/standard/72704.html · NENA-STA-020.2-2026 §5.4 (re-contact abandoned calls): https://cdn.ymaws.com/www.nena.org/resource/resmgr/standards/standards1/NENA-STA-020.2-2026_911CallP.pdf
- **Decision:** the PI decides, with R-9 and the amendment gate, whether a classified yellow or danger-unknown call abandoned before the ward is escalated, and what a coordinator does with it before M5 (R-3) (P-1). It is a go-live blocker until decided. Interim (a), EV-5, with no protocol change and no new record kind: `sautictl escalations` also lists open incidents flagged abandoned with no escalation, references only, most severe first, and intake-gw exports their count by severity, so the coordinator's handover reviews them each shift.

### V-08 · High · (a) · Nothing checks the place a responder's menu sends them to
- **Sources:** EA-1.
- **Evidence:** in protocol mode the alert SMS carries no location (`internal/platform/channels/sms.go:203`), so the menu's accepted and assigned screens (`config/gated/responder.yaml:35-36`) are where a USSD responder reads where to go. The values come from `internal/respond/ussd.go:216-235`. The menu tests assert the reference, the ward and "bridge line", never the landmark or the category; the voice path does assert the landmark (`internal/platform/ivr/alerts_test.go:282`). Mutant R1 (the screen names another landmark of the gazetteer) lived through every unit test, the wiring scenarios and e2e; R2 (wrong category) lived too. Gremlins efficacy for `internal/respond` is 64 %.
- **Failure scenario:** a refactor of `values()` makes the accept screen name the wrong landmark, or none. A rider who accepted a red trauma call rides to the wrong market while the caller waits.
- **Standard:** ISO/IEC/IEEE 29119-4:2021, equivalence partitioning of outputs (committee page; iso.org refused the fetch): https://committee.iso.org/standard/79430.html · NIST SSDF v1.1 PW.8.2: https://csrc.nist.gov/pubs/sp/800/218/final
- **Decision:** EV-5. A table test over every responder screen, both languages, with a gazetteer of at least two landmarks and two categories: the text names the incident's own place and category and no other. The menu path of scenario 1 asserts the landmark.

### V-09 · High · (a) · The masking tests check one number in one form
- **Sources:** EA-2, EG-16. **Overlaps:** F-72 (UX-8 Task 8.7).
- **Evidence:** `internal/pii/pii_test.go` checks one Ugandan number for one substring. The foreign-number branch of the mask (`internal/pii/pii.go:82-83`) is executed by no test. `TestNoCallerNumberInLogs` dials one Ugandan number. Four mutants lived through all unit tests and the e2e and wiring suites: the Ugandan mask showing the last four digits (P1); a foreign number rendered in full (P2); a bare 9-digit number logged in clear (`internal/platform/logging/redact.go:59`, L1); integer attributes never redacted (`redact.go:78`, L2). `pii.Phone` has no `fmt.Formatter`, so `%d`, `%t` and `%p` print the raw string, and an unexported `pii.Phone` field printed with `%v` shows the full number (latent: no production struct holds one). The redactor misses numbers written with dots, brackets, slashes, underscores or a no-break space (`redact.go:27`).
- **Failure scenario:** a developer "improves" the mask to show the last digits so coordinators can tell callers apart, or breaks the foreign branch. A Kenyan SIM calling near the border has its number rendered into an SMS template, a screen or a `sautictl` listing. Every test passes, and "never reveal a caller's number" is broken in production.
- **Standard:** NIST SSDF v1.1 PW.8.2 (above) · ISO/IEC/IEEE 29119-4:2021 boundary values (above) · OWASP ASVS 5.0.0 V16 (security logging; chapter-level citation): https://github.com/OWASP/ASVS/releases
- **Decision:** EV-5. A property test that no digit run escapes any fmt verb, JSON or slog rendering of Ugandan, foreign and zero numbers; a `Format` method that always writes the mask; a test that no struct holds an unexported `pii.Phone`; redaction boundary and separator tests; the e2e scan gains a foreign caller. UX-8 Task 8.7 keeps its capture handler and UUID work on the same regex.

### V-10 · High · (a) · A ward-level incident can be searched for around the wrong ward
- **Sources:** EA-5.
- **Evidence:** `Engine.centres` (`internal/dispatch/engine.go:285-291`) searches every zone of the incident's ward when only the ward is known. Its only test (`internal/dispatch/engine_test.go:103-118`) has one ward, so it cannot tell "the incident's ward" from "the first ward". Mutant D3 (`&&` to `||`, so the first ward with zones is used) lived through dispatch, wiring and e2e. Ward-level dispatch is a real path: an unmatched typed landmark falls back to the ward.
- **Failure scenario:** a caller in the second ward types a landmark the gazetteer does not know. Ring 1 is drawn around the first ward. The responders nearest the patient are never alerted.
- **Standard:** ISO/IEC/IEEE 29119-4:2021 (two partitions are the minimum that separates the cases), above.
- **Decision:** EV-5. Two wards in the unit test, the incident in the second; a wiring scenario in which the second ward's responder is alerted first.

### V-11 · High · (a) · No test runs the built binaries in CI
- **Sources:** EA-3, EO-4, EO-12, EG-8 (the `sautictl` binary).
- **Evidence:** unit coverage of the commands is 9 to 27 %. The leadership loop that starts the dispatch engine (`cmd/dispatch-worker/main.go:166-178`) is reached by no test; wiring and e2e assemble components in-process. Mutant M1 (the leader parks and never dispatches) lived through every test in `./cmd/...`, e2e and wiring. The demo's black-box suite on PR #15 runs the real binaries but skips in CI when no embedded PostgreSQL is cached (`t.Skip` in its `pgBin` helper, still at PR #15's head 2319a8b); CI logged it passing in 1.5 s against 58 s locally. Its 30 s windows pass a 15 s dispatch stall. The production refusal holds on the built artifacts (all four exit 1 under `SAUTI_ENV=production`), but only the audit ran them.
- **Failure scenario:** a change to flags, environment parsing or the leader callback means the worker leads and never claims a due incident. Every test passes. Callers complete Tool 1 and no responder is alerted; the first signal is a coordinator noticing.
- **Standard:** NIST SSDF v1.1 PW.8.2 (test the executable as built), above · IEC 62304:2006+AMD1:2015 §5.6, edition 1.1 (software integration testing): https://webstore.iec.ch/en/publication/22794 · Principles of Chaos Engineering (experiments run continuously): https://principlesofchaos.org/
- **Decision:** EV-6. An integration smoke test builds the binaries, starts intake-gw, dispatch-worker and outbox-relay against one bootstrapped cluster, walks a scenario through the real USSD route, expects the first alert and a growing ledger, runs the real `sautictl` verifiers, sends SIGTERM, and scans every child's output for the number. A CI step proves the production refusal on the built binaries. PR #15 must make its suite fail, not skip, under CI before it merges (plan, conditions on open PRs).

### V-12 · High · (a), (c), (d) · The fakes stand in for Asterisk and Africa's Talking with no contract check, and the fallback dialplan is checked as text
- **Sources:** EA-4.
- **Evidence:** `sim.FakeARI` (`internal/platform/sim/ari.go:17-31`) re-implements Asterisk's playback, channel and event semantics from the documentation; every IVR, bridge, voice-alert and e2e IVR test runs against it. The SMS fake answers hand-written JSON (`internal/platform/africastalking/sms_test.go:18-45`). Nothing is recorded from a real provider: the only `testdata` directory is triage's. The two voice rules that matter most, "Stasis down reaches a coordinator" and "the caller ID is masked before any dial", are tested by parsing `telephony/asterisk/extensions.conf` as text (`internal/platform/ivr/dialplan_test.go:47`, `:161`). The trunk's connected-line setting, which ADR-0034 relies on, is visible to no test. Asterisk 24 LTS was released on 2026-11-05.
- **Failure scenario:** a dialplan edit that is textually right and semantically wrong, a `Goto` to a missing label, passes. With ivr-controller down, Asterisk hangs up on callers instead of ringing a coordinator. Or Africa's Talking changes a status code, the fake keeps the old shape, and alerts settle as sent.
- **Standard:** Fowler, "ContractTest" (2011-01-12): https://martinfowler.com/bliki/ContractTest.html · Asterisk versions: https://docs.asterisk.org/About-the-Project/Asterisk-Versions/ · Africa's Talking's API reference returned 403 to the auditor: **unverified**.
- **Decision:** EV-9. A CI job runs real Asterisk on the committed configuration and observes the fallback dial and its caller ID; recorded ARI events become golden files that `FakeARI` must match; recorded Africa's Talking sandbox responses become the fakes' bytes once the operator records them (O-3). Connected-line behaviour on the real trunk is a P0 staging drill.

### V-13 · High · (a), (b) for the numbers · Latency has no indicator, and a latency regression passes every test
- **Sources:** EO-2; ES-8 (the service-level hand-off). **Overlaps:** F-67 (UX-8 Tasks 8.1, 8.2, 8.8), R-10.
- **Evidence:** the metrics package has counters and gauges only (`internal/platform/metrics/metrics.go:19-22`). Mutant: the USSD budget at `internal/platform/africastalking/ussd.go:74` raised from 800 ms to 60 s passes the adapter and e2e suites; no test sets the budget. Mutant: the dispatch poll at `cmd/dispatch-worker/main.go:58` raised from 1 s to 60 s passes; integration tests drive `Engine.RunOnce`, never the binary's interval.
- **Failure scenario:** a store call ignores its context and a slow database pushes a caller past the operator's session timeout: the caller sees the operator's failure, not Sauti's voice-line screen. Or a slower poll delays every first alert beyond the 10 s target. CI is green in both cases.
- **Standard:** Google SRE Workbook, Alerting on SLOs: https://sre.google/workbook/alerting-on-slos/ · Implementing SLOs: https://sre.google/workbook/implementing-slos/
- **Decision:** EV-10. A test that the adapter answers within the budget when the port hangs; the binary's poll interval becomes a shared constant used by a wiring test that requires T3 − T2 under 10 s from database times; a first-alert histogram. The numbers are PENDING the PI (P-7, R-10). UX-8 keeps the USSD and voice counters.

### V-14 · High · (a), (d) · Failover is tested by killing one session; restore and backup integrity are tested nowhere
- **Sources:** EO-5; EO-5's schedule part goes to V-23.
- **Evidence:** scenario 14 is "verified" in `docs/traceability.md:72`. Its test terminates the `op_dispatch` backends on one server (`internal/platform/wiring/dispatch_integration_test.go:430-431`) and expires the lease by hand (`:445`). No test stops the server, refuses connections for a while, or moves the leader. [ADR-0012](../adr/ADR-0012-test-database-environments.md) promised Testcontainers in M1 for failover tests that kill a database mid-test; `go.mod` has none. No Patroni, pgBackRest, restore script or backup check exists.
- **Failure scenario:** on the first real switchover, 10 to 30 s of refused connections make the pool, the leader campaigns or the at-most-once claim behave differently. Alerts are lost or duplicated, and a coordinator is the first to notice. A restore that has never run fails on the day it is needed.
- **Standard:** pgBackRest command reference, `verify` (page updated 2026-10-17): https://pgbackrest.org/command.html · AWS Well-Architected REL12-BP05, game days: https://docs.aws.amazon.com/wellarchitected/latest/framework/rel_testing_resiliency_game_days_resiliency.html
- **Decision:** EV-10 (a): the engine survives a database restart mid-cascade, with its own embedded server, exactly-once delivery per rung and a leader again within 30 s; the traceability row is corrected to what is proven. P0 (d): a Patroni switchover under load and a monthly restore with `pgbackrest verify` and both verifiers, each writing a dated drill record.

### V-15 · High · (a) · A `verified_by` pointer passes when its test is skipped, commented out, behind an unused build tag, or not a test
- **Sources:** EG-1, EA-6.
- **Evidence:** `tools/freshness/pointers.py:20-37` resolves a pointer when `^func TestX\(` matches anywhere in the named file; `governance/compliance_check.py` and `threat-model/validate_threats.py` reuse it, and nothing reads test output. `governance/tests/test_freshness.py:110-113` asserts that a method in a plain class, which `unittest` never collects, resolves. Probes: with the Go severity ratchet deleted and its first `verified_by` test skipped, all three gates and the whole unit suite pass; the same test commented out passes; a failing test behind `//go:build drill` added to a threat passes, because CI never uses that tag. Today all 326 pointers run in CI and none skips. A running test is not a guarding one either: T-005's three pointers stay green under the masking mutants of V-09.
- **Failure scenario:** a test is skipped as "flaky" during an incident fix while the code it guards regresses. The risk register and threat model still report the control as verified, and an auditor under ISO/IEC 42001 trusts evidence that no longer runs.
- **Standard:** ISO/IEC 42001:2023 clause 9.1 (evaluate the effectiveness of controls; read from the publisher's preview, iso.org refused): https://assets.vde-verlag.de/iec-normen/preview-pdf/info_isoiec42001%7Bed1.0%7Den.pdf · NIST AI RMF 1.0 MEASURE 2.1 (AI 100-1, 2023-01-26; a revision is announced, none published): https://airc.nist.gov/airmf-resources/airmf/5-sec-core/ · Go `test2json` (`"Action":"skip"`): https://pkg.go.dev/cmd/test2json
- **Decision:** EV-8. The integration job saves `go test -json` output; the freshness gate fails unless every Go pointer has a `pass` event for that test in that package; Python pointers must name a collected `TestCase` method and have a `pass` event too, which a standard-library result collector writes in the same form, so a test that calls `skipTest` does not count. Whether a test guards its control is the mutation signal of V-20.

### V-16 · High · (a), (c) · The DPO sign-off for new record categories (hard constraint 12) has no gate, and M3 adds four tables first
- **Sources:** EG-3.
- **Evidence:** `threat-model/threats.yaml:194-207` (T-012) states the open gap: no gate requires DPO approval of a new record category; its pointers test the media store and strict configuration decoding. None of the three gates reads `db/*/migrations`. The M3 plan adds operational tables in 0029 to 0032 (field events, verification, responder reports, handover slips) before the gate planned for M5.
- **Failure scenario:** an M3 migration adds a free-text column for a responder's note, which may hold a patient's or caller's name. It is a record kind the DPO never classified, with no retention category, so M6's destruction never selects it. The DPO learns of it from a data-subject request or a breach.
- **Standard:** Uganda Data Protection and Privacy Act 2019 (Act 9 of 2019, now Cap. 97) s.9: https://bills.parliament.ug/attachments/Data%20Protection%20and%20Privacy%20Act%202019.pdf · LINDDUN "Non-compliance": https://linddun.org/threat-types/
- **Decision:** EV-7, before M3 Part A merges. A record-category register lists every table and column of every store with its retention category and approval; an integration test compares it with the migrated schema in both directions, so a new column fails until the register names it in the same PR. A pending approval is allowed only for the tables and columns that exist when EV-7 merges, frozen as a baseline that may only shrink; a table or column added later fails until its row names a category, the approving role and a date, so M3 Part A's four tables wait for the DPO. The DPO owns and populates it (D-1).

### V-17 · High · (b), (e: M3, M6) · Under-triage is measured nowhere while the pilot runs, and the clinical metrics are undefined
- **Sources:** EG-4, ES-8.
- **Evidence:** `docs/MODEL_CARD.md` §4 calls telephone-against-scene agreement a study output, and the M3 plan stores scene severity beside the incident's for the analysis. The only `MEASURE` risk in `governance/ai_risk_manifest.yaml` is the flagged-off speech assist. Build Prompt §15 names agreement, red-to-physician and 24-to-48-hour status, but no document defines under-triage or over-triage, a reference standard, a threshold or a review trigger. Only reached incidents get a Tool 2 report, so the abandoned, merged and unreached calls (V-07's cohort) are unverified: partial-verification bias. The stack's alert-to-responder service level (Scalable MVP Stack, §Principles and service levels) has no histogram.
- **Failure scenario:** a placeholder category folds a time-critical class into `other`, which goes green. Responders keep finding red patients, and their Tool 2 reports record it from M3. Nothing surfaces the pattern until the study analysis, months later, and the pilot reports good agreement on the verified subset while missed red calls sit in the unverified denominator.
- **Standard:** NIST AI RMF 1.0 MEASURE 2.4 and MANAGE 4.1 (production monitoring): https://airc.nist.gov/airmf-resources/playbook/measure/ · STARD 2015 (diagnostic accuracy reporting, BMJ 2015;351:h5527): https://pmc.ncbi.nlm.nih.gov/articles/4623764 · EU AI Act Art. 15 (declared accuracy; consolidated text as at 27 July 2026): https://ai-act-service-desk.ec.europa.eu/en/ai-act/article-15
- **Decision:** the PI and Medical Director define the under-triage safety signal, its review cadence and threshold, the interval and accuracy definitions, the composite reference standard and the handling of unverified incidents, before M3's Tool 2 data exists (P-4). Then (e: M3) a count-only disagreement metric and a test that seeds one green incident with a red scene report; (e: M6) the metric SQL with fixtures that encode the definitions. Deferred in the plan until P-4.

### V-18 · High · (b), (e: M7) · No usability evidence for low-literacy callers on basic phones
- **Sources:** ES-5.
- **Evidence:** no use specification, use-error analysis, formative or summative test exists in `docs/` or `governance/`. The UX audit is an expert review without participants. The UX audit's F-88 and F-89 defer usability and field tests to M7, and the roadmap lists field tests among what this environment cannot prove; no milestone plans user-based evaluation. The screens where a misunderstanding changes triage inputs are screen 2 ("I do not know"), the category menu and screen 5.
- **Failure scenario:** stressed bystanders read "I do not know" as the safe answer, or press the category digit on screen 2. Calls are under-triaged in a way no software test can see.
- **Standard:** IEC 62366-1:2015+AMD1:2020 (above) · DECIDE-AI (2022), early clinical evaluation including human factors: https://www.equator-network.org/reporting-guidelines/reporting-guideline-for-the-early-stage-clinical-evaluation-of-decision-support-systems-driven-by-artificial-intelligence-decide-ai/
- **Decision:** the PI's (P-5): a use specification, hazard-related use scenarios drawn from V-06, V-07 and the UX audit's High findings, formative walkthroughs with the Community Advisory Board on the existing simulator before M7, and a summative simulated-use test on real handsets in M7. The software support, abandonment by screen and channel, is UX-8 Task 8.2's per-screen counters. Deferred in the plan.

## Findings: Medium and Low

Each entry gives the sources, the evidence as filed, the failure, the standard and the decision. Medium and Low evidence was read in the audits and spot-checked where it changes a decision; see [Unverified](#unverified).

**V-19 · Medium · (a) · The database's privilege tests are spot checks.** EG-6, EG-7. Evidence: the `search_path` test covers schema `op` only (`internal/platform/pg/opstore/opstore_integration_test.go:628-651`); deleting the pin from the linkage vault's SECURITY DEFINER function (`db/linkage/migrations/0001_vault.sql:39`) passes every test. Direct-write refusal is tested as `op_intake` only; a migration granting `INSERT, UPDATE, DELETE` on the incident tables to `op_dispatch`, `op_ivr` and `op_console` passed the whole `./internal/platform/pg/...` integration suite. `op.incidents` has no trigger guarding severity or T-times; the ratchet lives in the SECURITY DEFINER functions. Failure: an over-grant in a review-fix migration lets a compromised worker `UPDATE op.incidents SET severity = 'green'`, bypassing the ratchet, server time and the ledger; it needs a second fault. Standard: PostgreSQL 18, writing SECURITY DEFINER functions safely: https://www.postgresql.org/docs/18/sql-createfunction.html · OWASP ASVS 5.0.0 (least privilege; chapter level): https://github.com/OWASP/ASVS/releases. Decision: EV-7: every SECURITY DEFINER function in every store pins its `search_path`; runtime roles hold no `TEMPORARY` or `CREATE`; a committed role-by-table privilege matrix, columns included, is compared with `aclexplode` in both directions.

**V-20 · Medium · (a) · No mutation testing; measured efficacy runs from 48 % to 91 % across the safety packages.** EA-10. Evidence (gremlins v0.6.0, each package's own tests): triage 90.7 %, incident 88.4 %, location 83.6 %, audit 82.9 %, pii 82.4 % (mutant coverage 38.6 %), dispatch 79.3 %, africastalking 79.3 %, intake 78.9 %, respond 64.2 %, logging 48.3 %. Many survivors die in other packages' integration tests; the ones that matter are V-04, V-08, V-09 and V-10. Failure: coverage of 88 to 92 % hides oracles that never check what a person sees; each gap is found only when a reviewer guesses the mutant. Standard: Petrović et al., "Practical Mutation Testing at Scale", IEEE TSE 2021: https://research.google/pubs/practical-mutation-testing-at-scale-a-view-from-google/ · gremlins v0.6.0: https://github.com/go-gremlins/gremlins/releases/tag/v0.6.0. Decision: EV-11: pinned tool; a changed-lines job over the safety packages, report-only until M3 merges, then required at 80 % efficacy; a weekly full run with the survivor list (ADR-0041).

**V-21 · Medium · (a) · Fuzzing keeps nothing it finds, misses parsers, and `ParsePhone` is not canonical.** EA-7. Overlaps F-71 (UX-8 Task 8.6). Evidence: five targets for 20 s from seeds; no corpus or crasher is kept (`.github/workflows/ci.yml:88-98`); `FuzzEventDecode` has no oracle; the USSD text-delta parser is unreached; the responder menu, delivery reports, `ParsePhone` and witness notes are not fuzzed. A 15-line property found that `ParsePhone` is not idempotent in under a second (see the correction above). Failure: a CI fuzz run finds a crasher in the responder menu, the input is lost with the runner, the re-run passes, and the failure is put down to a flake. Standard: Go fuzzing (failing inputs belong in `testdata/fuzz`): https://go.dev/doc/security/fuzz/ · NIST SSDF v1.1 PW.8.2 (above). Decision: EV-1 fixes `ParsePhone` with a re-parse property and fuzz target; EV-11 keeps crashers and the fuzz cache, adds the USSD text-history property, and runs fuzzing for 10 minutes nightly; UX-8 Task 8.6 keeps the menu, delivery and witness targets.

**Status:** partly fixed in EV-1: `ParsePhone` is canonical, with a round-trip property test and fuzz seeds. Keeping fuzz finds and fuzzing the other parsers remain (EV-11).

**V-22 · Medium · (a) · No flaky-test handling, and the race detector fails open.** EA-8, EC-12. Evidence: `TestControllerOverAsteriskQueue` failed once under load and passed on re-run and 40 times alone (a real-clock 3 s wait); some integration tests assert after fixed sleeps; `scripts/ci/integration.sh` does not shuffle; no quarantine, retry policy, stress run or flake log. Both CI scripts add `-race` only when cgo is on and say nothing otherwise (`scripts/ci/unit.sh:7-9`, `scripts/ci/integration.sh:7-9`). On a runner image without gcc (the Forgejo runners of ADR-0008), "unit tests (race)" stays green with no race detection. Standard: Go race detector: https://go.dev/doc/articles/race_detector · Fowler, "Eradicating Non-Determinism in Tests": https://martinfowler.com/articles/nonDeterminism.html. Decision: EV-11: both scripts fail in CI without cgo; integration shuffles and prints its seed; fixed sleeps become polls; a nightly stress run opens an issue on failure, and that issue list is the quarantine.

**V-23 · Medium · (a), (d) · Nothing runs on a schedule.** EG-8 (the verifiers), EC-9, EO-5 (the nightly verifiers). Overlaps F-66 (UX-7 Task 7.4). Evidence: `.github/workflows/ci.yml` has no `schedule:` trigger; the spec's nightly `verify-ledger` and `verify-projections` run nowhere (`docs/runbooks/audit-ledger-verification-failure.md` says "P0 schedule"); govulncheck and OSV-Scanner never re-scan an unchanged `main`, and `build/tools.mod` is never scanned (both gates do catch a reachable vulnerable dependency, as a mutant showed). Failure: a Go standard-library advisory published after the last push goes unflagged until someone pushes. Standard: OSPS Baseline v2026.08.28, OSPS-VM-05.02 (above). Decision: EV-11: one scheduled workflow for the vulnerability scans (tools module included), long fuzzing, the stress run and the weekly mutation run; UX-7 Task 7.4 keeps Dependabot. Production verifier timers and the external witness are P0 (R-16). Deletion of an event before relay is the UX audit's F-47 (UX-5).

**V-24 · Medium · (a), (c) · Drill freshness is unenforced and all 19 runbooks are undrilled.** EO-6. Overlaps F-69. Evidence: every runbook reads "not yet drilled"; `tools/freshness/__init__.py` checks only that "Last reviewed:" appears; a runbook dated 1999 with its "Last drilled" row deleted passes. Failure: a runbook decays (a renamed metric, a moved lock key) and fails during a SEV-1, past the recovery objective. Standard: AWS Well-Architected REL12-BP05 (above) · ISO 22301:2019 §8.5: **unverified** (iso.org refused). Decision: EV-12: freshness parses both fields; a runbook that declares a drill cadence fails once `go_live` is set in `config/platform.yaml` and its last drill is older. The owner sets cadences and who runs game days (O-4).

**V-25 · Medium (was High) · (b), (c), (e: M7) · Synthetic probes and drills would create real emergencies.** EO-3. Evidence: an incident exists from screen 1; no code recognises a probe or drill call. At about 1.7 real incidents a day, availability can be measured only with synthetic traffic, which Build Prompt §14 requires every five minutes on two networks. Failure: M7 builds the probe without a marker; probe sessions merge with real calls, alert real responders and enter the study, and responders learn to ignore alerts. Standard: Google SRE Workbook, low-traffic services (above). Decision: the PI decides whether probe incidents are excluded from the study; the DPO whether a probe marker is a new record kind (P-8, D-4). M7 then builds a keyed-hash allowlist of test SIMs and a synthetic responder pool. Deferred.

**V-26 · Medium · (a), (d) · Nothing measures clock accuracy, and T-time order is unchecked across a failover.** EO-8. Overlaps F-86. Evidence: the 10 ms target has no indicator; `threat-model/threats.yaml` holds a planned P0 offset alarm; no constraint or check enforces T1 ≤ T2 ≤ T3, and T-times come from whichever node is primary. Failure: after a switchover the new primary is 40 s off; timers fire early or late and study intervals turn negative. Standard: RFC 8915 (NTS): https://datatracker.ietf.org/doc/rfc8915/ · chrony_exporter: https://github.com/SuperQ/chrony_exporter. Decision: EV-12: a database clock-offset gauge in dispatch-worker's existing ticker (it stamps nothing) with a promtool-tested rule; `verify-projections` reports T-times out of order. chrony monitoring is P0.

**V-27 · Medium · (a) · An incident's path can be rebuilt only by hand-joined SQL.** EO-10. Evidence: no traces; 17 of 91 log calls carry an incident key; `sautictl` has no timeline. Failure: after a delayed red, the Medical Director asks when each alert went out; the engineer reconstructs it from five tables and three logs under pressure and gets it wrong. Standard: OpenTelemetry Collector redaction processor (logs alpha): https://explorer.opentelemetry.io/collector/components/contrib/redactionprocessor. Decision: EV-12: `sautictl incident timeline`, database-time order, IDs only, with a test that no number appears in any form.

**V-28 · Medium · (a) · No fault test covers a half-open connection or a slow database.** EO-11. Overlaps F-19 (UX-2 Task 2.5). Evidence: the ARI fakes close the socket cleanly; no test adds latency to the database. Failure: Asterisk loses power and ivr-controller reports itself connected; a slow database pushes USSD answers past the budget. Standard: Toxiproxy toxics (latency, timeout): https://github.com/Shopify/toxiproxy. Decision: EV-10: a slow-read connection wrapper proves the voice-line screen within 1 s and no half-written incident; the black-hole ARI test lands with UX-2 Task 2.5's ping, whichever merges second.

**V-29 · Medium · (a) · A classification is accepted with no logic version, and nothing pins the approved bundle.** ES-9. Evidence: an empty logic version is sent as NULL (`internal/platform/pg/opstore/store.go:61-66`) and `NULL !~ pattern` does not raise (`db/operational/migrations/0018_review_pr10.sql:57`); a probe stored a yellow classification with no version. `config/gated/triage.yaml` names a file and keys, no digest, so an older or withdrawn bundle signed by a trusted key loads. AIR-002's pointers test neither. Failure: a new writer omits the version and the study cannot attribute classifications to a rule set; or a stale signed bundle is deployed after an amendment. Standard: NIST AI RMF MEASURE 2.4: https://www.nist.gov/itl/ai-risk-management-framework. Decision: EV-3: migration 0039 adds a trigger that refuses a classification without a version (the event-append function is not replaced); the fold mirrors it; `approved_sha256` in the gated triage configuration, refused on mismatch (ADR-0043).

**V-30 · Medium · (a), (c) · The registers cannot say "mitigated", "partial" or "accepted".** EG-5, EG-15, EG-11, EG-14. Overlaps F-73 (UX-8 Task 8.7). Evidence: threats and AI risks have no status, residual or acceptance field; `validate_threats.py` accepts either category, so LINDDUN names are never checked when STRIDE is present; `planned:` markers never expire; the traceability Status column is unchecked (R12 set to "Verified" with no proof passed); residual-risk acceptance lives in one unassigned issue (#16) that no file links; no review date is recorded. Failure: a go-live checklist built from these files passes T-012, T-013 and T-015, which rest on gaps or P0 controls. Standard: ISO/IEC 42001:2023 clauses 6.1.3, 7.5 and 9.2 (preview, above) · LINDDUN: https://linddun.org/threat-types/. Decision: EV-8: a status field with rules (mitigated allows no planned pointer; accepted needs a residual, a role and an issue), LINDDUN validated whenever present, Status parsed in the matrix, planned markers for merged milestones fail (this implements UX-8 Task 8.7's part), and a review date. The owners accept residual risks (D-5).

**V-31 · Medium · (a) · The AIBOM and the model card are never checked against what runs, and already disagree.** EG-9, ES-12. Evidence: `governance/ai_risk_manifest.yaml` lists the triage table as `planned`, `protocol-pending`, while intake-gw loads it at `tool1-placeholder-0.1`; `docs/MODEL_CARD.md` is headed M1 Part A under a column "Status (M0)" and cites only the Go test for a Go-and-SQL claim; a probe changed `ranking_version` and set an ISO clause to "banana" and `compliance_check.py` reported no problem. Failure: a re-parameterised ranking ships while the ethics committee and DPO read an AIBOM describing the old one. Standard: CycloneDX 1.6 (ECMA-424), `modelCard`: https://cyclonedx.org/docs/1.6/json/ · Model Cards (Mitchell et al.): https://arxiv.org/abs/1810.03993. Decision: EV-8: each AIBOM entry names its source key, and the gate compares version and status with it and with the model card's table; the clause is validated; the drift is corrected.

**V-32 · Medium · (b), (c), (a) for the mechanism · No hazard log, risk acceptability policy, safety case or software safety class.** ES-6, ES-7. Evidence: risks are scattered across the AI manifest, the threat model, the edge-case map and the UX audit, each on its own scale; G-1 alone was graded Medium and High by two documents; nothing links "Sauti does not delay or drop a live emergency" to its evidence and counter-evidence. Standard: ISO 14971:2019 (above) · ISO/TS 24971-2:2026: https://evs.ee/en/iso-ts-24971-2-2026 · IEC 62304 edition 1.1 and IEC 82304-1:2016 (https://webstore.iec.ch/en/publication/26120) · GSN Community Standard v3: https://scsc.uk/resources/citation_r1386.html. Decision: the Medical Director owns the acceptability policy; the PI and sponsor decide whether IEC 62304 (edition 1.1; edition 2 is unpublished) and IEC 82304-1 are adopted, which would make the software class C (P-6). The hazard-log mechanism, a YAML log checked like the AI manifest, waits for that policy. Deferred.

**V-33 · Medium · (e: P2), (b) · No evaluation plan for any AI component.** ES-10, EG-18. Evidence: the model card gives the speech model's error rate with no local test set or threshold; AIR-004 is `planned:P2`; no disclosure that an automated system is listening is planned. Standard: EU AI Act Annex III point 5(d): https://artificialintelligenceact.eu/annex/3/ · Articles 10, 15 and 50 (consolidated text as at 27 July 2026): https://ai-act-service-desk.ec.europa.eu/en/ai-act/article-15 · the Annex III date in the regulatory note above. Decision: P2, with an ethics amendment and a DPIA: shadow mode with aggregate counts only, acceptance and drift thresholds, and the disclosure prompt. The PI records the Act's applicability in an ADR (P-9). Now (a): EV-3's archcheck rule keeps a future assist package out of triage.

**V-34 · Medium · (c), (a) · Privacy threat coverage omits two LINDDUN categories, and automated decisions are assessed nowhere.** EG-10. Evidence: no threat covers Unawareness and unintervenability or Detecting; the repository cites 2 of the DPIA's 14 risks (by number); nothing mentions automated decision-taking, though triage classifies every caller. Standard: Uganda DPPA 2019 s.27 (above) · LINDDUN (above). Decision: the DPO decides whether triage is "solely" automated under s.27 and what notice and reconsideration apply (D-2). EV-8 adds the two threat entries, status open.

**V-35 · Medium · (a), (e: M7) · ASVS 5.0 Level 2 is claimed with no requirement-level evidence, and no black-box security test runs.** EG-12. Evidence: `docs/explanation/security-model.md` maps ASVS by chapter only; the penetration test is M7; the internet-facing surface is two token-protected callback routes. Standard: OWASP ASVS 5.0.0 (above) · OWASP WSTG: https://owasp.org/www-project-web-security-testing-guide/. Decision: EV-6's smoke test exercises the real callback route over TCP. A requirement-level checklist, which scopes the M7 test, is deferred until the M3 to M6 surface exists.

**V-36 · Medium · (a) · The deployed demo is built and scanned nowhere in CI.** EC-4, EC-11. Evidence: the public demo is built by the host from a manual upload of PR #15's worktree, which was changes-requested; its Dockerfile pins no base-image digest; no image scan exists. Data are synthetic (ADR-0039), so no caller path. Standard: SLSA v1.2, Build L1–L2: https://slsa.dev/spec/v1.2/build-track-basics · CVE-2026-33634 (a compromised scanner release; pin by SHA): https://nvd.nist.gov/vuln/detail/CVE-2026-33634. Decision: conditions on PR #15 (plan): a digest-pinned base, an image build and OSV image scan in CI, and a stage script that refuses a commit not on `main`.

**V-37 · Medium · (a) · The secret scan misses PostgreSQL connection strings.** EC-5. Evidence: gitleaks with default rules (`scripts/ci/secrets.sh`) missed two planted DSNs with passwords; GitGuardian's false positives are cleared in a dashboard outside Git. Failure: a real store credential is committed to this public repository and the alert is cleared out of habit. Standard: OSPS Baseline v2026.08.28 (above). Decision: EV-13: a committed gitleaks configuration with a DSN rule, proven by a self-test that plants a DSN at run time; false positives recorded in a versioned GitGuardian file.

**V-38 · Medium · (c) · Commits are unsigned.** EC-7. Evidence: 332 of 346 commits on `main` are unsigned; the 14 signed are GitHub's merge commits. Failure: agent and human commits cannot be told apart, and a token compromise produces indistinguishable commits. Standard: GitHub rulesets, required signatures (above). Decision: the owner (O-1): SSH signing with a separate key for the agent, then the ruleset rule.

**V-39 · Medium · (d) · No release, provenance or signing; reproducibility holds but nothing checks it.** EC-8. Evidence: no tags or releases; `go build -trimpath` gave identical hashes for all five binaries from two checkout paths. Failure: at go-live, nobody can prove the binary on a dispatch host came from a reviewed commit. Standard: SLSA v1.2 Build L2 (above). Decision: P0, with the Forgejo release workflow: two builds compared, an SBOM per binary, signed provenance.

**V-40 · Low · (a) · The SBOM holds no licences and no AI components.** EC-10, EG-17. Evidence: 0 of 17 components carry a licence although `scripts/ci/sbom.sh` asks for them; the module graph, not the binaries; a 30-day artifact; the AIBOM never reaches it. Standard: CycloneDX 1.6 (above); 1.7 is current (ECMA-424 2nd edition): https://ecma-international.org/publications-and-standards/standards/ecma-424/. Decision: EV-13: one SBOM per binary with licences, a check that none is missing, and the AIBOM entries as components. The version stays 1.6, as the global rules name it, until the owner moves to 1.7.

**V-41 · Low · (a), (c) · Pins are current but nothing keeps them current; no security policy.** EC-13, EC-14. Overlaps F-66. Evidence: all five action SHAs match their tags and every tool is the latest release; `sha_pinning_required` is false; no `SECURITY.md`. Standard: OpenSSF Scorecard checks (above). Decision: EV-13 adds `SECURITY.md` pointing at GitHub's private vulnerability reporting; the owner enables SHA pinning (O-1); Dependabot is UX-7 Task 7.4.

## Source mapping

| Source | Canonical | Source | Canonical | Source | Canonical |
|---|---|---|---|---|---|
| EA-1 | V-08 | ES-1 | V-04 | EC-1 | V-03 |
| EA-2 | V-09 | ES-2 | V-05 | EC-2 | V-02 |
| EA-3 | V-11 | ES-3 | V-06 | EC-3 | V-03 |
| EA-4 | V-12 | ES-4 | V-07 | EC-4 | V-36 |
| EA-5 | V-10 | ES-5 | V-18 | EC-5 | V-37 |
| EA-6 | V-15 | ES-6 | V-32 | EC-6 | V-03 |
| EA-7 | V-21 | ES-7 | V-32 | EC-7 | V-38 |
| EA-8 | V-22 | ES-8 | V-17, V-13 (part) | EC-8 | V-39 |
| EA-9 | V-03 | ES-9 | V-29 | EC-9 | V-23 |
| EA-10 | V-20, V-05 (part) | ES-10 | V-33, V-04 (part) | EC-10 | V-40 |
| EA-11 | V-04 | ES-11 | V-04 | EC-11 | V-36 |
| EO-1 | V-01 | ES-12 | V-31, V-05 (part) | EC-12 | V-22 |
| EO-2 | V-13 | EG-1 | V-15 | EC-13 | V-41 |
| EO-3 | V-25 | EG-2 | V-03 | EC-14 | V-41 |
| EO-4 | V-11 | EG-3 | V-16 | EG-10 | V-34 |
| EO-5 | V-14, V-23 (part) | EG-4 | V-17 | EG-11 | V-30 |
| EO-6 | V-24 | EG-5 | V-30 | EG-12 | V-35 |
| EO-7 | V-01 | EG-6 | V-19 | EG-13 | F-79 (R-5) |
| EO-8 | V-26 | EG-7 | V-19 | EG-14 | V-30 |
| EO-9 | F-83 (M7) | EG-8 | V-23, V-11 (part); F-47 | EG-15 | V-30 |
| EO-10 | V-27 | EG-9 | V-31 | EG-16 | V-09 |
| EO-11 | V-28 | | | EG-17 | V-40 |
| EO-12 | V-11 | | | EG-18 | V-33 |
| EO-13 | F-17 (UX-2 Task 2.3) | | | | |

Three sources restate a UX-audit finding with no new decision and map to it: EO-9 (load and soak, M7), EO-13 (duplicate admin ports, UX-2) and EG-13 (the breach clock, the DPO's under R-5). EG-8's pre-relay deletion is F-47 (UX-5).

## Recommendations to the owners

These need a person, not code. Each names who decides. The UX audit's R-1 to R-17 stand; the items below add to them.

**The PI and the Medical Director (class b).**
- **P-1 (PI, amendment gate): abandoned yellow and "I do not know" calls before the ward (V-07).** Escalate them, with a new reason, or accept the residual risk in writing with a test named for it. Decide with R-9 (ward-level dispatch) and R-3 (what a coordinator does before M5). Until then this is a go-live blocker, and the interim list of EV-5 is reviewed every shift.
- **P-2 (the emergency-medicine co-investigators): the expected-severity table (V-05).** Author the expected severity of every combination of the Tool 1 domain independently of the rules, and sign it with a key other than the rules' signer. Decide whether a combination no rule names stays green or becomes red.
- **P-3 (co-investigators and translators): labels and codes signed together (V-06).** Sign each screen's labels in both languages with the codes they select, with a recorded back-translation; the same for the IVR recordings.
- **P-4 (PI, Medical Director): clinical performance during the pilot (V-17).** Before M3's Tool 2 data exists: an under-triage safety signal (for example scene red after telephone green or yellow), its weekly review and the threshold that triggers a protocol review; the T1 to T5 interval definitions; under- and over-triage against a composite reference standard (Tool 2, the 24-to-48-hour status, red to physician contact); the handling of unverified incidents. It is a safety monitor, never a training signal.
- **P-5 (PI): usability engineering (V-18).** A use specification; formative walkthroughs with the Community Advisory Board on the simulator before M7; a summative simulated-use test with low-literacy users on real handsets in M7, with clinician-written vignettes.
- **P-6 (Medical Director, PI, sponsor): risk management (V-32).** A risk acceptability policy and an owner for one hazard log; whether IEC 62304 (edition 1.1) and IEC 82304-1 are adopted; a top safety goal, "no live emergency is delayed or dropped by Sauti", for a structured safety case.
- **P-7 (PI): the service-level numbers (V-13),** restating R-10: alert to first responder, USSD response, availability. The rules and tests are built with PENDING values.
- **P-8 (PI, with the DPO): synthetic probes and drills (V-25).** Whether probe and drill incidents are excluded from study metrics, before M7 builds them.
- **P-9 (PI): the EU AI Act (V-33).** Record whether the Act is applied, and to which components, in an ADR. Annex III obligations apply from 2 December 2027 under Regulation (EU) 2026/1744.

**The repository owner and the operator (class c).**
- **O-1 (owner): finish `main` protection (V-03, V-38, V-41).** One approving review from a human other than the author, with stale approvals dismissed and the last push approved, once a second account exists; code-owner review once the clinical owners have accounts; until then, an explicit merge decision by the owner on each PR. Add each new required CI job to the ruleset as the last step before its PR merges, after its first passing run (EV-2's rules job first; plan, "A new CI job"). Keep CodeRabbit's check out of the required list, because it passes when rate-limited; the gate requires its review. Enable SHA pinning for actions. Require signed commits once signing is set up, with a separate key for the agent.
- **O-2 (owner, PI): accounts for the clinical owners (V-03).** The PI names who owns `/triage-rules/`, `/config/gated/` and the amendment records; each gets an account and replaces the author's in `.github/CODEOWNERS`.
- **O-3 (operator): an Africa's Talking sandbox recording (V-12).** Record the SMS send responses in each documented status, a delivery report and a USSD callback in the sandbox, with dates, for the fakes to replay. Whether sandbox credentials may live in GitHub for a nightly re-recording is the owner's call.
- **O-4 (operator, PI): drills (V-24).** A cadence per SEV-1 runbook (failover monthly and switchover quarterly, as Build Prompt §14 asks) and who runs game days, restating R-1's roles.

**The DPO (class c).**
- **D-1: the record-category register (V-16).** Own it in CODEOWNERS and populate each table's and column's retention category before M3 Part A merges; M3's four tables are its first test.
- **D-2: automated decisions (V-34).** Whether triage is "solely" automated under DPPA s.27, what notice and reconsideration path applies, and which DPIA risks each threat answers (by number).
- **D-3: the breach clock,** restating R-5 (EG-13, F-79): the runbook says 72 hours where the Act says immediately.
- **D-4: probe markers (V-25),** with P-8: whether a probe or drill marker is a new record kind.
- **D-5 (with the operator): residual risks (V-30).** Accept in writing, or reject, each residual in issue #16, the aggregator's processing-location confirmation the DPIA requires before the first live dispatch, and the PDPO registration and its annual renewal, under a go-live milestone.

## Strengths

The audits agree on what already works, and the fixes build on it:
- **The SQL write path has teeth.** Disabling the severity guard, dropping urgency from the claim's selection or order, and updating or deleting ledger rows as the owner are each caught by a named test; the Go and SQL transition tables are parity-tested.
- **Dispatch's failure paths are tested through crashes.** Swallowed send errors and re-delivered attempts are each caught by two to five crash and failover tests.
- **The triage domain is checked exhaustively today.** All 150 combinations are compared with an oracle, and the bundle must be signed (ML-DSA-65, context-separated), total and approved before production starts. The weakness is who owns the oracle, not how much it covers (V-05).
- **Tamper evidence survives a determined owner.** A rewrite with a recomputed chain and deleted checkpoints is still caught by the witness, and an empty witness directory cannot report "ledger OK".
- **Deterministic unit tests.** Timer-heavy tests run in `testing/synctest`, the unit suite shuffles, and 30 shuffled repetitions found no failure.
- **Real PostgreSQL 18.6** in every integration test, digest-pinned in CI, each test on freshly bootstrapped isolated stores; the harness fails rather than skips.
- **Function privileges are checked structurally,** over every function of every store, including those a future migration adds.
- **Minimisation is checked by content:** a number placed in an event payload is searched for in every ledger row after relay.
- **The supply chain is pinned:** every action by SHA, the database image by digest, every tool by sha256; gosec, govulncheck, OSV-Scanner, a licence allowlist and full-history gitleaks on every PR; binaries reproduce bit for bit.
- **Time is server-side.** T-times come from `clock_timestamp()`, proven by `TestOpenIncidentSetsT1FromDatabase`.
- **The dead-man signal is designed for staleness.** Its leader gauge is 0 on a failed read and at the end of a term; only the rules and the wiring test are missing (V-01).
- **Today's pointers are honest.** All 326 `verified_by` pointers run in CI, and none skips (V-15 is about tomorrow).
- **Gaps were reported, not hidden.** PR #18 documents G-1 and names the test that pins it.

## Unverified

- **Full suites under every mutant.** The audits ran each mutant against the packages most likely to catch it, not the whole integration suite. A mutant marked as living could be killed elsewhere; for the person-facing ones, every package that renders the output was included.
- **`promtool`.** The rules V-01 proposes were argued from the Prometheus documentation; no rule file was executed, because the download was refused in the audit session. EV-2 runs them.
- **Providers.** Africa's Talking's API reference returned 403; the fakes were checked against help-centre pages only. The ARI model pages show no version.
- **Standards pages.** iso.org refused every fetch: ISO 14971, ISO/IEC/IEEE 29119-4, ISO 22301 and ISO/IEC 42001 rest on committee pages, publisher previews or search results. ISO 22301 §8.5 is unverified. The ACS-COT under- and over-triage benchmarks come from secondary sources and are not used as targets.
- **IEC 62304 edition 2 timing.** Not published (verified above); its date is unknown, with forecasts from 2027 to 2029.
- **The law in Uganda.** Whether Sauti is a medical device under the National Drug Authority, whether the EU AI Act reaches it, and whether the revised edition of the DPPA (Cap. 97) renumbers sections are questions for the DPO and sponsor.
- **Repository facts after the audits.** The ruleset was read for this document. Who deleted the earlier ruleset 27188855, and when, is unknown. CodeRabbit's hourly review allowance at zero stars is documented only as "1 to 10".
- **Medium and Low evidence** was taken from the audits and spot-checked where it changes a decision, not re-read line by line.

## Appendix A: the auditors' prompt

Each audit followed this prompt, with its lens and output file named in the task message. It is reproduced in full, with three edits: local scratch paths are shortened to `<scratchpad>`, the repository path to `<repository>`, and the edge-case reference, which is on PR #18 and not on `main`, loses its code formatting so that the freshness gate does not look for it.

> # Prompt: evaluation-mechanism audit of a high-stakes system (Sauti Dispatch)
>
> ## Role
> You are a senior quality-assurance engineer, safety-systems architect and independent auditor. You review how a life-critical system proves it works. Your subject is the system's *evaluation mechanisms*, not its features: its tests, gates, drills, metrics, reviews and evidence. Ask of each one: "If this broke in a way that could hurt a caller, would something we run catch it, before release and in production?"
>
> ## System context
> Sauti Dispatch is an emergency medical dispatch system for basic phones in Uganda.
> - **Channels:** callers report over USSD and voice IVR (Asterisk ARI). Responders are alerted by SMS, USSD and voice, and coordinators by voice.
> - **Code:** Go 1.27, PostgreSQL 18, five stores, and one SECURITY DEFINER write path. Triage is deterministic, and a severity can only ever rise. There is a hash-chained audit ledger.
> - **Merged so far:** milestones M0–M2 and UX-1 are on `main`. M3–M7 and the infrastructure (P0) are planned.
> - **Sources** (read them; do not guess):
>   - repo: `<repository>`. Read it on `main`; open PRs may change it.
>   - spec: `docs/superpowers/specs/2026-11-12-sauti-dispatch-design.md`
>   - ADRs: `docs/adr/`
>   - plans: `docs/superpowers/plans/`
>   - traceability: `docs/traceability.md`
>   - edge cases: docs/reference/edge-case-verification.md, on PR #18's branch `verify-1-edge-cases`
>   - UX audit: `docs/explanation/ux-audit-2026-11.md`
>   - CI: `.github/workflows/`, `scripts/ci/`
>   - governance: `governance/`, `threat-model/`, `tools/`
>   - model card: `docs/MODEL_CARD.md`
> - **Confidential governing texts:**
>   - a local, uncommitted text extract of the Build Prompt
>   - a local, uncommitted text extract of the Scalable MVP Stack (see its "Principles and service levels", "Security and compliance controls", "Resilience and disaster recovery", "Edge cases" and "AI-augmented triage" sections)
>
>   Read them for intent. Never quote them; cite by title and section.
>
> ## Your lens
> Defined in the task message. Stay inside it; note any out-of-lens issue in one line under "Hand-offs".
>
> ## Method
> 1. **Inventory.** List every evaluation mechanism in your lens: what it claims to prove, where it runs (locally, CI, staging, production), how often, and what it blocks.
> 2. **Test the tests.** For each mechanism, try to show it can pass while the property it guards is broken. A finding you prove by running code (`go test`, a mutant you introduce and revert, a script) outranks one you argue.
>    - Run things freely, but **leave the tree exactly as you found it** (`git stash` or revert every probe).
>    - Never push, and never touch the shared PostgreSQL on port 55432. The test scripts start their own embedded one.
> 3. **Compare with current practice.** Use web search to find the CURRENT (November 2026) version of each standard or practice you cite. Cite its URL, and note the version or date you saw. Do not cite from memory. If you cannot verify a source, say "unverified".
> 4. **Judge by consequence.** Rate each gap by the worst credible harm to a caller, responder or coordinator if the gap hides a defect:
>    - **Critical:** a defect that could delay or drop a live emergency, or expose a caller's identity, would reach production undetected.
>    - **High:** such a defect would be caught only by chance or late (in production, by a person).
>    - **Medium:** it weakens confidence, traceability or compliance evidence, with no direct patient-safety path.
>    - **Low:** hygiene.
> 5. **Classify each gap:**
>    - (a) fixable in code or CI now;
>    - (b) needs a protocol or clinical decision (the PI or Medical Director);
>    - (c) needs an owner decision (the DPO, the operator, or the repository owner, for example GitHub settings);
>    - (d) needs infrastructure (P0) or staging;
>    - (e) belongs to a later milestone (name it).
> 6. **Fix shape.** For each (a) gap, give a concrete, testable fix: the mechanism to add or change, where it lives, the command that runs it, and how you would prove it catches the defect (the mutant or fault it must catch). Prefer the smallest mechanism that closes the gap. Do not propose tools for their own sake.
>
> ## Hard rules for any fix you propose
> These are the Build Prompt §3 constraints:
> - server-side time only;
> - never reveal a caller's number;
> - no patient data on messaging apps;
> - the audit holds only incident and staff IDs;
> - no new record kind without the DPO;
> - every failure path ends with a person;
> - clinical wording is never invented;
> - the amendment gate is respected.
>
> Living documentation must follow: Diátaxis, MADR 4.0 ADRs, `verified_by` pointers, the model card and the AIBOM. Migrations are global: `main` has 0001–0033, and M3 reserves 0029–0032.
>
> ## Output
> Write your findings to the file named in the task message, as Markdown:
> - **Header:** lens, date, the commit you audited (`git rev-parse --short HEAD`), and the commands you ran.
> - **Inventory table:** mechanism, claims, runs where, blocks what, verdict (sound / weak / theatre).
> - **Findings**, most severe first. For each:
>   - `ID` (your lens prefix, then a number), title, severity, class;
>   - **Evidence:** `file:line`, plus the command you ran and its output (trimmed);
>   - **Failure scenario:** the defect that slips through, and its effect on a person;
>   - **Standard:** name, version and URL, as verified;
>   - **Fix:** mechanism, location, command, and the fault it must catch;
>   - **Effort:** S, M or L.
> - **Strengths:** mechanisms that are genuinely sound, with evidence.
> - **Unverified:** what you could not check, and why.
> - **Hand-offs:** one-liners for the other lenses.
>
> ## Definition of done
> Every mechanism in your lens appears in the inventory with a verdict. Every Critical or High finding has run evidence or a precise code citation. Every standard has a verified URL. No file in the repository is left changed.
>
> Your reply to the coordinator is at most 300 words: counts by severity and class, the top 5 findings, and anything you think the other lenses must know.
