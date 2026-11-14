# Evaluation Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the November 2026 evaluation audit so that every hard constraint is proven by a check that fails when the constraint breaks, starting with a merge gate that fails closed, which also lifts the merge freeze.

**Architecture:**
- **Evidence lives in the repository and comes from CI runs:** tests, `promtool` rule tests, signed expectation data, recorded provider fixtures and saved test results. No external platform.
- **Every surviving mutant becomes a regression test.** A task names the audit mutant its test must kill. For a test-only task, the implementer applies the mutant by hand, shows the new test failing, reverts, and records the run in the PR description.
- **The merge gate is a Python module under `tools/`,** in the style of `tools/freshness`, tested in the governance job against a stub `gh`.

**Tech Stack:**
- Go 1.27.4 and PostgreSQL 18.6; `testing/synctest` for timers; `sim.FakeARI`, `sim.USSD`; `internal/platform/pg/pgtest`.
- Python 3.12, standard library plus the hash-pinned PyYAML of `governance/requirements.txt`.
- `promtool` from a Prometheus release fetched by `scripts/tools/fetch.sh` (sha256-verified) (EV-2); gremlins pinned in `build/tools.mod` (EV-11); Asterisk built from a sha256-pinned source tarball (EV-9).
- No new Go runtime dependency.

**Spec:**
- `docs/explanation/evaluation-audit-2026-11.md`: findings V-01 to V-41, each with its sources, evidence, standard and decision. Read it with this plan.
- `docs/adr/ADR-0041-evaluation-strategy-and-quality-gates.md`: the eleven rules every task serves.
- `docs/superpowers/plans/2026-11-13-ux-hardening.md` and `docs/superpowers/plans/2026-11-13-m3-field.md`: the two open plans this one coordinates with (below).
- Build Prompt §3, §14, §15, §16. The governing documents are confidential: this plan, and every file it creates, cites them by section only.

## Global Constraints

**The hard constraints (Build Prompt §3), as they bind this plan:**
- **Deterministic triage (rule 3).** No task changes a rule, a severity or a classification path. EV-3 and EV-4 add checks only.
- **Server-side time (rule 4).** Tests compare T-times from the database's `recorded_at`, never from Go's clock. The clock-offset gauge (EV-12) stamps nothing. The merge gate's quiet window uses the local clock: it is tooling outside Sauti.
- **Never reveal a caller's number (rule 5).** Every new test that renders, logs or lists anything scans its output for each fixture number in every form (E.164, national, digits only). New CLI output prints references, never numbers. New metric labels are closed sets.
- **The audit holds IDs only (rule 8).** No task adds ledger content.
- **No new record kind without the DPO (rule 12).** No task adds a table or a column. Migration 0039 adds a trigger. The abandoned-call list (EV-5) reads existing flags. The record-category register (EV-7) is governance data about records, owned by the DPO; once it merges, a table or column added by any plan needs the DPO's approval in it.
- **Fail safe to a human (rule 14).** The dead-man rules (EV-2) and the abandoned-call list (EV-5) exist for it. Nothing here closes an incident.
- **Clinical wording is never invented.** No new caller or responder copy. EV-4's placeholder expectations are derived mechanically from the committed rule, marked `PENDING-MD-PI`, and refused by production until the co-investigators sign their own (P-2).
- **The amendment gate (rule 11).** EV-3 changes `config/gated/triage.yaml` and EV-4 re-signs `triage-rules/tool1.yaml`. With `study_window_active: false` the gate passes them; if the window is active when they land, each needs an amendment record for its exact content hash.

**Migrations.**
- `main` has 0001–0028 and 0033. M3 reserves 0029–0032; the UX plan reserves 0034–0038. This plan uses **0039** (EV-3), the only migration it needs.
- The UX plan sends its review fixes to "the next free number after 0038". If a UX review fix and EV-3 both claim 0039, the PR that merges second renumbers before it merges. A merged migration is never edited or renumbered.
- Never `CREATE OR REPLACE` `op.append_incident_event` or `op.append_event_core`: M3's 0029 replaces the core function once, and a later apply would undo this plan's change. EV-3 therefore guards with a trigger.
- Function conventions: `SECURITY DEFINER` where a role must act beyond its grants, `SET search_path = op, pg_temp`, `REVOKE ALL … FROM PUBLIC`, least-privilege `GRANT`.

**ADRs.** ADR-0030–0033 are M3's, 0035–0038 the UX plan's, 0039 the demo's (PR #15), 0040 the layout's (PR #17), and 0041 this plan's strategy. New records: ADR-0042 (EV-1), ADR-0043 (EV-3), ADR-0044 (EV-4), ADR-0045 (EV-7), ADR-0046 (EV-8), ADR-0047 (EV-9). Other phases change no decision and cite ADR-0041.

**Living docs, every phase.** Each phase ends with a docs task that updates:
- the audit entry of each finding it closes, with a status line below the decision (`**Status (date):** …`);
- `docs/traceability.md`, `threat-model/threats.yaml` and `governance/ai_risk_manifest.yaml` pointers;
- `docs/MODEL_CARD.md` when the triage evidence changes (EV-3, EV-4, EV-8);
- the runbooks it touches, with `Last reviewed:`;
- the ADR it names, indexed in `docs/adr/README.md`.

**Gates, every PR, before pushing:**
- `python3 -m tools.freshness --check`, `python3 governance/compliance_check.py`, `python3 threat-model/validate_threats.py`, `python3 -m unittest discover -s governance/tests -t . -q`;
- `scripts/ci/unit.sh`, `scripts/ci/integration.sh`, `scripts/ci/arch.sh`, `scripts/ci/sqlc-diff.sh`.

Then poll `gh pr checks <PR>` until nothing fails or is pending, answer every CodeRabbit finding (inline comments and review bodies), run `python3 -m tools.mergegate --repo mpairwe7/SautiCare <PR> --dry-run` (from EV-1 on), and **ask the user before merging**. Never post a command asking CodeRabbit to resolve or approve.

**A new CI job.** EV-1's `test_required_checks_match_the_ci_jobs` makes `tools/mergegate/required_checks.txt` list exactly the jobs of `.github/workflows/ci.yml`, and the gate refuses unless the ruleset requires every name on that list. A phase that adds a job to `ci.yml` (EV-2) merges in this order:
1. The PR adds the job to `ci.yml` and its name to `required_checks.txt`.
2. The job passes on the PR's head, and a dry run of the gate from a checkout of `main`, whose list does not name the job yet, passes.
3. The owner records the decision to merge, then adds the name to ruleset 27260742's required checks, with GitHub Actions as its source.
4. The gate runs and merges at once: from step 3 until the merge, GitHub blocks every other PR on a check that `main` does not run yet.

A job that is not required yet (EV-9's Asterisk job, EV-11's mutation job) goes in `.github/workflows/advisory.yml`, which the list does not cover. The gate still refuses any failed check run, listed or not, so a job meant only to report sets `continue-on-error: true` on its step and writes its result to the job summary. When the owner makes a job required, it moves into `ci.yml` by the four steps above.

**Delivery.** One PR per phase, each under 100 files, each a working tested slice. Merge commits.

| Phase | Branch | Base | Findings | Tasks | Est. files | Migration | ADR |
|---|---|---|---|---|---|---|---|
| EV-1 | `ev-1-merge-gate` | `ev-0-audit` | V-02, V-03 (a), V-21 (`ParsePhone`) | 5 | ~19 | — | 0042 |
| EV-2 | `ev-2-dead-man-rules` | `main` | V-01 | 4 | ~18 | — | — |
| EV-3 | `ev-3-safety-invariants` | `main` | V-04, V-29, V-33 (archcheck) | 7 | ~28 | 0039 | 0043 |
| EV-4 | `ev-4-clinical-evidence` | `ev-3-…` | V-05, V-06 | 4 | ~30 | — | 0044 |
| EV-5 | `ev-5-person-facing` | `main` | V-08, V-09, V-10, V-07 (interim) | 6 | ~24 | — | — |
| EV-6 | `ev-6-binaries` | `main` | V-11, V-35 (route) | 3 | ~6 | — | — |
| EV-7 | `ev-7-db-gates` | `main` | V-19, V-16 | 4 | ~14 | — | 0045 |
| EV-8 | `ev-8-evidence-gates` | `main` | V-15, V-30, V-31, V-34 (a) | 5 | ~23 | — | 0046 |
| EV-9 | `ev-9-provider-contracts` | `main` | V-12 | 4 | ~22 | — | 0047 |
| EV-10 | `ev-10-latency-failover` | `main` | V-13, V-14 (a), V-28 | 6 | ~16 | — | — |
| EV-11 | `ev-11-ci-depth` | `main` | V-20, V-21 (rest), V-22, V-23 | 6 | ~18 | — | — |
| EV-12 | `ev-12-drills-clocks` | `ev-2-…` | V-24, V-26, V-27 | 4 | ~20 | — | — |
| EV-13 | `ev-13-supply-chain` | `main` | V-37, V-40, V-41 (a) | 4 | ~10 | — | — |

**Order across the three plans.**
1. EV-1 is the first merge after the freeze (below). EV-0, this audit's PR, merges through EV-1's gate.
2. EV-2, EV-3, EV-4 and EV-7 merge before M3 Part A: M3's 0029 is the seventh copy of the append function, which EV-3's tables must meet, and M3's four tables must be the register's first test (EV-7): M3 Part A cannot merge until the DPO's approval rows for them are in the register.
3. EV-5 and EV-6 before go-live; the rest in the order above. The UX phases continue in parallel.

**Coordination with the UX plan.** Where both plans touch one thing, one owner builds it and the other drops or extends it:

| Item | Built by | The other plan |
|---|---|---|
| `build/prometheus/sauti-rules.yml` and the `promtool` CI job | EV-2 | UX-8 Task 8.3 adds the burn-rate and relay rules to the same file and job |
| The `Histogram` type (UX-8 Task 8.1) | UX-8 Task 8.1, or EV-10 Task 10.3 if it lands first, as Task 8.1 specifies | the other reuses it |
| The service-level page `docs/reference/slos.md` | whichever of EV-10 and UX-8 lands first | the other extends it |
| `FuzzParsePhone` | EV-1 Task 1.4 (in `internal/pii`) | UX-8 Task 8.6 drops it |
| Planned-marker expiry (F-73) | EV-8 Task 8.2 | UX-8 Task 8.7 drops that part |
| The redaction regex at `internal/platform/logging/redact.go:27` | EV-5 Task 5.3 (separators) and UX-8 Task 8.7 (UUIDs) | the second to land rebases; both tests stay |
| The black-hole ARI test | UX-2 Task 2.5 (with its ping) | EV-10 Task 10.5 adds it only if UX-2 has merged without it |
| The daily vulnerability scan (F-66) | EV-11 Task 11.5 | UX-7 Task 7.4 keeps Dependabot only |

**Not in this plan** (see [Deferred](#deferred-with-the-reason)): the PI's, DPO's and owner's decisions (P-1 to P-9, D-1 to D-5, O-1 to O-4 in the audit), P0 infrastructure, and the later-milestone parts of V-17, V-18, V-25, V-33 and V-39.

## Review Focus

1. **The gate meets an answer it cannot read.** Cases: any `gh` call exits non-zero or times out; a JSON body is empty or truncated; GitHub reports `UNKNOWN` mergeability; CodeRabbit was rate-limited and reviewed an older commit. Expected: refuse with the reason, exit 2, write nothing. Tests: Task 1.1 `test_an_unreadable_pr_refuses`, `test_an_unreadable_check_list_refuses`; Task 1.2 `test_a_head_without_a_coderabbit_review_refuses`, `test_a_thread_query_error_refuses`.
2. **A polite objection.** Cases: CodeRabbit's last word says "looks good, but … remain unverified" or "is addressed; however …"; a stranger replies "ok"; the author resolved a thread over an objection. Expected: the thread is open, it is reopened if resolved, and the merge is refused. Tests: Task 1.2 `test_judge_thread`, `test_open_threads_are_reopened_and_refuse`.
3. **A seventh copy of the append function weakens the ratchet by one step.** Expected: a table test fails on every lowering pair, in Go and in SQL. Tests: Task 3.1 `TestNoEventLowersSeverity`; Task 3.2 `TestAppendRejectsEverySeverityLowering`.
4. **Tier 3's signal is stale, not high.** Cases: intake-gw is down (series absent); no instance leads (all export 0); two dispatch leaders. Expected: a staleness alert fires within 2 minutes. Tests: Task 2.3's `promtool` cases.
5. **A foreign caller's number in a new message path.** Expected: no rendering of a `pii.Phone`, through any fmt verb, JSON or slog handler, shows a run of digits. Tests: Task 5.2 `TestNoDigitEscapesAnyRendering`.

---

## Phase EV-1: a fail-closed merge gate, and a canonical `ParsePhone`

**Branch** `ev-1-merge-gate` from `ev-0-audit`. **Findings** V-02, V-03 (the gate's ruleset check and the doc corrections), V-21 (`ParsePhone`). **ADR** 0042.

**How EV-1 itself merges.** Merges are frozen until this gate exists, so EV-1 bootstraps:
1. The owner reviews the EV-1 PR (V-03; record the decision on the PR).
2. With EV-1's branch checked out, run `python3 -m tools.mergegate --repo mpairwe7/SautiCare <EV-0 PR>`. It merges EV-0 (this audit).
3. Rebase EV-1 on `main`, wait for CI, answer CodeRabbit, and run the same command on the EV-1 PR.

From then on every merge goes through `tools/mergegate`.

### Task 1.1: The gate's runner, and the verdicts on the PR, the ruleset, `main` and the checks

**Files:**
- Create: `tools/mergegate/__init__.py`, `tools/mergegate/__main__.py`, `tools/mergegate/required_checks.txt`
- Create: `governance/tests/stub_gh.py` (not a test module: `unittest` collects only `test*.py`), `governance/tests/test_mergegate.py`

**Interfaces:**
- Produces, in `tools/mergegate/__init__.py`:
  - `class Refused(Exception)`: a reason not to merge. `main()` prints `mergegate: REFUSED: <reason>` to stderr and returns 2.
  - `def gh(*args: str) -> str`: runs `shlex.split(os.environ.get("MERGEGATE_GH", "gh")) + list(args)` with a 120 s timeout. A non-zero exit, a timeout or an `OSError` raises `Refused` naming the arguments and stderr.
  - `def gh_json(*args: str) -> Any`: `json.loads(gh(...))`; empty or invalid output raises `Refused`.
  - `def gh_lines(*args: str) -> list[Any]`: one JSON value per non-empty line, for `--paginate --jq '.[] | …'`; an invalid line raises `Refused`.
  - `def expected_checks() -> list[str]`: the lines of `required_checks.txt`, ignoring blanks and `#` comments; an empty list raises `Refused`.
  - `def check_pr(repo: str, pr: int) -> dict`: returns `{"head": str, "title": str, "author": str}`.
  - `def check_ruleset(repo: str, expected: list[str]) -> None`
  - `def check_main_green(repo: str) -> None`
  - `def check_checks(repo: str, pr: int, expected: list[str]) -> None`
  - `def main(argv: list[str]) -> int`: `--repo OWNER/NAME PR [--dry-run]`. Returns 0 when it merged (or, with `--dry-run`, would merge) and 2 when it refused. It never returns 1, and catches only `Refused`: any other exception is a bug and propagates.
- `tools/mergegate/__main__.py`: `raise SystemExit(main(sys.argv[1:]))`.

**Rules:**
- **PR:** `gh pr view <pr> --repo R --json number,state,isDraft,baseRefName,headRefOid,mergeable,title,author`. Require `state` OPEN, `isDraft` false, `baseRefName` main, `mergeable` MERGEABLE (UNKNOWN refuses with "re-run"), and `headRefOid` matching `^[0-9a-f]{40}$`.
- **Ruleset:** `gh api repos/R/rules/branches/main`. Require a `required_status_checks` rule whose contexts include every expected name, and a `pull_request` rule with `required_review_thread_resolution: true`. Otherwise refuse: "main protection is missing or weaker than expected: <what>".
- **`main` is green:** `gh api repos/R/branches/main --jq .commit.sha`, then `gh run list --repo R --branch main --event push --workflow ci --limit 1 --json headSha,status,conclusion`. Require the run's `headSha` to equal `main`'s, `status` completed and `conclusion` success.
- **Checks:** `gh pr checks <pr> --repo R --json name,bucket`. A non-zero exit refuses (gh exits 8 while checks are pending). Every expected name must appear, and every instance of it must be in bucket `pass`. Any check in bucket `fail`, `cancel` or `pending` refuses, whether expected or not. `skipping` is allowed only for names not in the expected list. No check is exempt by PR number.
- `required_checks.txt` holds the nine CI job names exactly as ruleset 27260742 lists them.

**The stub `gh`.** `stub_gh.py` reads a JSON scenario from `STUB_GH_SCENARIO`: a list of `{"argv": [tokens], "exit": int, "stdout": str, "stderr": str}`. It answers with the first rule whose tokens all appear in its own arguments in order, and appends its arguments as one JSON line to `STUB_GH_LOG`. With no matching rule it exits 99 with "stub gh: no rule for …", so an unexpected call fails closed. A helper in the test module builds a clean scenario (every answer a mergeable PR, head `"a" * 40`, title "T", author "dev") that each test overrides one rule of. Tests run `python3 -m tools.mergegate --repo o/r 9999` as a subprocess with `MERGEGATE_GH` set to `<sys.executable> governance/tests/stub_gh.py` and `MERGEGATE_NOW` set to a fixed UTC time.

- [ ] **Step 1: Write the failing tests** in `governance/tests/test_mergegate.py`:
  - `test_a_clean_pr_merges_with_the_head_pinned`: exit 0; the log holds `["pr", "merge", "9999", "--repo", "o/r", "--merge", "--match-head-commit", "a"*40, "--subject", "Merge PR #9999: T"]`.
  - `test_an_unreadable_pr_refuses`: `pr view` exits 1 with "HTTP 502". Exit 2; no `pr merge` in the log.
  - `test_an_unreadable_check_list_refuses`: `pr checks` exits 1 with "no checks reported". Exit 2.
  - `test_a_missing_required_check_refuses`: the check list lacks "governance and living docs". Exit 2, and stderr names it.
  - `test_a_failing_or_pending_check_refuses`, with subtests: an expected check in `fail`; an expected check in `pending`; "GitGuardian Security Checks" in `fail`. Each exits 2.
  - `test_a_weakened_ruleset_refuses`, with subtests: `[]`; required checks without "unit tests (race)"; `required_review_thread_resolution: false`. Each exits 2.
  - `test_red_main_refuses`, with subtests: `conclusion` failure; the run's `headSha` older than `main`'s. Each exits 2.
  - `test_required_checks_match_the_ci_jobs`: the `name` of every job in `.github/workflows/ci.yml` (read with `yaml.safe_load`) equals the set of lines in `required_checks.txt`, with no duplicates.

  Run `python3 -m unittest governance.tests.test_mergegate -v`. Expected: FAIL (no module `tools.mergegate`).
- [ ] **Step 2:** Implement the interfaces above.
- [ ] **Step 3:** Run the same command. Expected: these tests PASS (Task 1.2's tests do not exist yet).
- [ ] **Step 4:** Commit `feat(tools): a fail-closed merge gate: PR, ruleset, main and checks (EV-1 Task 1)`.

### Task 1.2: CodeRabbit's review of the head, the quiet window, and each thread judged by its last word

**Files:** Modify `tools/mergegate/__init__.py`. Test `governance/tests/test_mergegate.py`.

**Interfaces:**
- Consumes Task 1.1's `gh`, `gh_json`, `gh_lines`, `Refused`.
- Produces:
  - `REVIEWER = "coderabbitai"` (the GraphQL login; the REST login is `"coderabbitai[bot]"`), `QUIET = timedelta(minutes=5)`.
  - `def now() -> datetime`: `MERGEGATE_NOW` parsed as ISO 8601 if set, else UTC now.
  - `def check_review(repo: str, pr: int, head: str) -> None`
  - `def judge_thread(thread: dict, pr_author: str) -> bool`: True means settled.
  - `def check_threads(repo: str, pr: int, pr_author: str, dry_run: bool) -> None`

**Rules:**
- **Review of the head:** `gh api repos/R/pulls/<pr>/reviews --paginate --jq '.[] | {user: .user.login, commit_id, state, submitted_at}'`. Require at least one review by `coderabbitai[bot]` with `commit_id` equal to the head. Of its reviews in state APPROVED or CHANGES_REQUESTED, the latest must not be CHANGES_REQUESTED. A rate-limited push leaves no review of the head, so it refuses.
- **Quiet window:** the newest of CodeRabbit's inline comments (`pulls/<pr>/comments`) and issue comments (`issues/<pr>/comments`), by `created_at` and `updated_at`, and of its reviews' `submitted_at`, must be at least `QUIET` before `now()`. Otherwise refuse: "CodeRabbit was active <n> s ago; re-run after five quiet minutes". The gate does not wait: the operator re-runs it.
- **Threads:** a GraphQL query with variables `owner`, `name`, `n` and `after`, paginating `reviewThreads(first: 100, after: $after)` and reading for each thread `id`, `isResolved`, `resolvedBy { login }`, and `comments(first: 100) { pageInfo { hasNextPage } nodes { author { login } body url } }`. A thread whose comments have a next page refuses (fail closed rather than judge a partial thread).
- **`judge_thread`:** a thread is settled when:
  1. it `isResolved` and `resolvedBy.login == REVIEWER` (CodeRabbit's own verdict); or
  2. its last comment's author is the PR's author; or
  3. its last comment's author is `REVIEWER`, its body matches `CONCEDES` and it matches no `OBJECTS`.

  Every other thread is open: no reply yet, a CodeRabbit word that is not a clear concession, a last word from anyone else (Copilot, a stranger), and a thread resolved by someone else over an objection. Start from `CONCEDES = r"✅ Addressed in commit|Review thread resolved|\bLGTM\b|\b(is|are|has been|have been) (now )?(addressed|resolved)\b"` and `OBJECTS = r"(?i)\b(not|no longer|still|remains?|however|but|unverified|missing|unresolved|rate.limit\w*)\b|please|\?"`; the tests below decide.
- **Writes:** a settled thread that is not resolved is resolved with `gh api graphql -f query='mutation($id: ID!) { resolveReviewThread(input: {threadId: $id}) { thread { isResolved } } }' -f id=<thread id>`. An open thread that is resolved is reopened with the same shape and `unresolveReviewThread`, so GitHub's own rule blocks a merge from the web page too. A failed mutation refuses. `--dry-run` makes no mutation. Any open thread refuses, listing each one's first comment URL.

- [ ] **Step 1: Write the failing tests:**
  - `test_a_head_without_a_coderabbit_review_refuses`, with subtests: no CodeRabbit review; a review of an older commit only (the rate-limited case).
  - `test_changes_requested_refuses`: the latest decisive CodeRabbit review requests changes.
  - `test_activity_inside_the_quiet_window_refuses`, with subtests: an inline comment created 4 minutes before `MERGEGATE_NOW`; an issue comment updated 1 minute before.
  - `test_a_thread_query_error_refuses`: the GraphQL call exits 1 with "HTTP 403: API rate limit exceeded".
  - `test_a_thread_with_a_second_page_of_comments_refuses`.
  - `test_judge_thread`, a table calling `judge_thread` directly:

    | Thread | Expected |
    |---|---|
    | one CodeRabbit comment, unresolved | open |
    | CodeRabbit, then the author | settled |
    | CodeRabbit last: "this looks good, but the failed-Dial path and the busy branch remain unverified" | open |
    | CodeRabbit last: "The coordinator fallback is addressed; however the coordinator dial is not escalated when every attempt fails." | open |
    | last word "ok" from `someone-else` | open |
    | resolved, `resolvedBy` coderabbitai | settled |
    | resolved by the author, CodeRabbit's objection last | open |
    | CodeRabbit last: "✅ Addressed in commit abc1234" | settled |
  - `test_open_threads_are_reopened_and_refuse`: a thread resolved over an objection. The log holds an `unresolveReviewThread` call carrying `-f id=<its id>`; exit 2.
  - `test_a_failed_resolve_refuses`: the resolve mutation exits 1. Exit 2, and no `pr merge`.
  - `test_the_resolve_mutation_passes_the_thread_id`: the logged query contains `$id` and the arguments contain `id=PRRT_1`.

  Run `python3 -m unittest governance.tests.test_mergegate -v`. Expected: the new tests FAIL.
- [ ] **Step 2:** Implement. `main()` calls the checks in this order: PR, ruleset, `main` green, checks, review and quiet window, threads.
- [ ] **Step 3:** Run the same command. Expected: PASS.
- [ ] **Step 4:** Commit `feat(tools): the gate requires a review of the head and judges threads by their last word (EV-1 Task 2)`.

### Task 1.3: The merge pins the head; no approval by command; a dry run

**Files:** Modify `tools/mergegate/__init__.py`. Test `governance/tests/test_mergegate.py`.

**Rules:**
- After the threads, re-read `headRefOid`. If it moved, refuse: "the head moved during the run".
- Merge with `gh pr merge <pr> --repo R --merge --match-head-commit <head> --subject "Merge PR #<pr>: <title>"`. A non-zero exit refuses. On success print `mergegate: merged #<pr> at <head[:7]>`.
- The gate never calls `pr comment`, `pr review`, any `dismissals` endpoint, `--admin` or `--auto`.
- `--dry-run` performs every read, prints `mergegate: would merge #<pr> at <head[:7]>` and returns 0, with no mutation and no merge.

- [ ] **Step 1: Write the failing tests:**
  - `test_a_push_during_the_run_refuses`: the second `pr view` returns another head. Exit 2, no `pr merge`.
  - `test_a_refused_merge_refuses`: `pr merge` exits 1 ("Head branch was modified"). Exit 2.
  - `test_dry_run_writes_nothing`: exit 0; the log holds no `graphql` mutation and no `pr merge`.
  - `test_the_gate_never_asks_for_or_dismisses_a_review`: runs every scenario of the module's other tests and asserts that no logged call contains `comment`, `review` as a `gh pr` subcommand, `dismissals`, `--admin` or `--auto`.

  Run `python3 -m unittest governance.tests.test_mergegate -v`. Expected: the new tests FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run the same command, then `scripts/ci/governance.sh` (with `PYTHON` and `AMENDGATE_BASE` set as in CI). Expected: PASS, and the governance job runs these tests.
- [ ] **Step 4:** Commit `feat(tools): the gate merges only the head it judged (EV-1 Task 3)`.

### Task 1.4: `ParsePhone` is canonical (V-21)

**Files:** Modify `internal/pii/pii.go:44-66` and `scripts/ci/fuzz-smoke.sh`. Test `internal/pii/pii_test.go`; create `internal/pii/fuzz_test.go`.

**Interfaces:** `pii.ParsePhone(s string) (Phone, error)` keeps its signature. New invariant: if `ParsePhone(s)` returns `p`, then `ParsePhone(p.E164())` returns `p`.

- [ ] **Step 1: Write the failing tests:**
  - `TestParsePhoneIsCanonical`: for every input of `TestParsePhoneUganda` plus `"+254712345678"` and `"+1 202 555 0143"`, re-parsing `E164()` gives the same `Phone`. `"0000000000"`, `"0012345678"` and `"+2560077212345"` return `ErrInvalidPhone`.
  - `FuzzParsePhoneCanonical`: seeded with those inputs; whenever `ParsePhone(s)` succeeds, `ParsePhone(p.E164())` succeeds and is equal.

  Run `go test ./internal/pii/ -run Canonical -count=1`. Expected: FAIL on `"0000000000"`.
- [ ] **Step 2:** In both Ugandan branches, refuse a national number whose first digit is `0`: a national significant number never starts with the trunk prefix.
- [ ] **Step 3:** Run the same command, then `go test -run='^$' -fuzz='^FuzzParsePhoneCanonical$' -fuzztime=60s ./internal/pii/`. Expected: PASS, no failing input. Add the target to `scripts/ci/fuzz-smoke.sh`.
- [ ] **Step 4:** Commit `fix(pii): a parsed phone number re-parses to itself (EV-1 Task 4; EA-7)`.

### Task 1.5: EV-1 docs: ADR-0042, the merge how-to, and the claims that were not true

**Files:**
- Create: `docs/adr/ADR-0042-fail-closed-merge-gate.md` (index it), `docs/how-to/merge-a-pull-request.md`.
- Modify: `docs/adr/ADR-0014-configuration-and-amendment-gate.md` (front matter only: `status: accepted; superseded in part by ADR-0042`); `docs/MODEL_CARD.md:36`, `docs/runbooks/database-migrations.md:52` (and its `Last reviewed:`), `docs/how-to/add-a-migration.md:14`, `docs/explanation/security-model.md:18`: each CODEOWNERS claim gains "(not enforced until the owner's decision O-1 in the evaluation audit)"; `threat-model/threats.yaml` (a new threat, "a change merges unreviewed or over a failing gate", STRIDE Tampering, `verified_by` the gate's tests); `docs/explanation/evaluation-audit-2026-11.md` (status lines on V-02, V-03, V-21).
- Test: `governance/tests/test_mergegate.py::test_the_docs_name_no_enforced_codeowners_approval`: no file under `docs/` claims CODEOWNERS approval without the "not enforced" qualifier, while ruleset 27260742's `require_code_owner_review` is false. The test reads a constant in `tools/mergegate/__init__.py`, `CODE_OWNER_REVIEW_ENFORCED = False`, which the owner flips with O-1.

**ADR-0042 decides:** the gate's rules (Tasks 1.1 to 1.3); the ruleset as a second gate, and the gate's check of it; CodeRabbit's review required and its status check not; no approval by command; the owner's recorded decision on each PR until a second human can approve (O-1). It supersedes ADR-0014 in part: the compensating control for "the amendment gate cannot protect its own code" is the ruleset plus this gate, and code-owner review stays the owner's decision.

**The how-to:** run the gate with `--dry-run`; read each refusal; answer each open thread in the thread, never by resolving it; re-run after five quiet minutes; then run without `--dry-run` once the owner has recorded the merge decision.

- [ ] **Step 1:** Write `test_the_docs_name_no_enforced_codeowners_approval`. Run `python3 -m unittest governance.tests.test_mergegate -v`. Expected: FAIL on the four claims.
- [ ] **Step 2:** Write the docs.
- [ ] **Step 3:** Run the test and all four gates. Expected: PASS and OK.
- [ ] **Step 4:** Commit `docs: ADR-0042, the merge how-to, CODEOWNERS claims qualified (EV-1 Task 5)`. Push, open the PR, and merge as described at the top of this phase.

---

## Phase EV-2: the last automated path is alerted

**Branch** `ev-2-dead-man-rules` from `main`. **Findings** V-01. **ADR** none (ADR-0041 rule 8; ADR-0029 rule 8 already names tier 3).

### Task 2.1: The intake-gw leader term is a function, and tests prove the binary runs its gauge loop

**Files:** Modify `cmd/intake-gw/main.go:172-191`. Test `cmd/intake-gw/main_test.go`.

**Interfaces:**
- `func runEscalationGauges(ctx context.Context, store escalationAges, g *escalationGauges, every time.Duration)`: the loop at `:177-191`, where `escalationAges` is the store interface `gauges.update` already takes and `escalationGauges` the existing type at `cmd/intake-gw/main.go:217-253` (rename only if the names differ).
- `func leaderTerm(ctx context.Context, t term) error`: the body of `RunAsLeader`'s callback today. It starts the dispatch watchdog and `runEscalationGauges`, and returns the sweeper's result. `term` holds the store, the gauges, the interval, and the watchdog and the sweeper as `func(context.Context) error`. The callback becomes one line: `return leaderTerm(lctx, t)`.

- [ ] **Step 1:** Write `TestTheLeaderTermExportsTheGauges` in a `synctest` bubble: call `leaderTerm` with a fake store reporting 2 unacknowledged escalations, the oldest 700 s, a sweeper that blocks until its context ends and a watchdog that does nothing; run for one tick; the registry's text holds `sauti_escalation_gauges_leader 1`, `sauti_escalations_unacknowledged 2` and `sauti_escalation_oldest_unacknowledged_seconds 700`; cancel the context; the text holds `sauti_escalation_gauges_leader 0`. Run `go test ./cmd/intake-gw/ -run Gauges -count=1`. Expected: FAIL (no such function).
- [ ] **Step 2:** Extract both functions. Apply by hand the audit's mutant M3 (the update call removed) and a second mutant (the `runEscalationGauges` call removed from `leaderTerm`): the test must fail on each. Revert. The one-line callback is covered by EV-6 Task 6.1, which reads the gauges from the running binary.
- [ ] **Step 3:** Run the same command. Expected: PASS.
- [ ] **Step 4:** Commit `test(intake-gw): the leader term exports the dead-man gauges (EV-2 Task 1; EO-1)`.

### Task 2.2: dispatch-worker says who leads, and when its read fails

**Files:** Modify `cmd/dispatch-worker/main.go:110-145` and `:166-178`. Test `cmd/dispatch-worker/main_test.go`.

**Interfaces:**
- `func lead(ctx context.Context, g *dispatchGauges, run func(context.Context) error) error`: sets `sauti_dispatch_leader` to 1, runs `run`, and resets it to 0 in a deferred call. `RunAsLeader`'s callback calls it.
- `sauti_dispatch_overdue_read_ok`: 1 after a successful overdue read, 0 after a failed one; a standby exports 1.

- [ ] **Step 1:** Write `TestTheLeaderGaugeFollowsTheTerm` (1 while `run` blocks, 0 after it returns, including on a panic-free error) and `TestAFailedOverdueReadIsVisible` (a store whose read fails sets the read gauge to 0 and leaves the overdue gauge unchanged). Run `go test ./cmd/dispatch-worker/ -run 'Leader|Overdue' -count=1`. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run the same command. Expected: PASS.
- [ ] **Step 4:** Commit `feat(dispatch-worker): leader and read-health gauges (EV-2 Task 2; EO-7)`.

### Task 2.3: The rules, tested by `promtool` in CI

**Files:**
- Create: `build/prometheus/sauti-rules.yml`, `build/prometheus/sauti-rules.test.yml`, `scripts/tools/install-promtool.sh` (calls `scripts/tools/fetch.sh promtool <version> <release tarball URL> <sha256> <member>`), `scripts/ci/promtool.sh`.
- Modify: `.github/workflows/ci.yml` (a job named "alert rules (promtool)"), `tools/mergegate/required_checks.txt` (the new name), `governance/tests/test_runbooks.py`.

**Pinning.** The current Prometheus 3.5 LTS release for linux-amd64. Take its sha256 from the release's `sha256sums.txt` on GitHub, check it against the downloaded file, and record the version in `docs/reference/dependencies.md`.

**Rules** (group `sauti-dead-man`; each alert except Watchdog carries `annotations.runbook`, a path under `docs/runbooks/`):

| Alert | Expression | For |
|---|---|---|
| `SautiEscalationUnacknowledged` | `max(sauti_escalation_oldest_unacknowledged_seconds) > 600` | 1m |
| `SautiEscalationSignalStale` | `max(sauti_escalation_gauges_leader) < 1 or absent(sauti_escalation_gauges_leader)` | 2m |
| `SautiDispatchLeaderMissing` | `sum(sauti_dispatch_leader) != 1 or absent(sauti_dispatch_leader)` | 1m |
| `SautiDispatchOverdueUnreadable` | `min(sauti_dispatch_overdue_read_ok) < 1` | 2m |
| `Watchdog` | `vector(1)` | — |

- [ ] **Step 1: Write the failing rule tests** in `sauti-rules.test.yml`, each case firing or staying quiet as listed:
  - age 700 for 3m, leader 1: `SautiEscalationUnacknowledged` fires; `SautiEscalationSignalStale` quiet.
  - no intake-gw series: `SautiEscalationSignalStale` fires at 2m.
  - leader 0 on both instances, age 0: `SautiEscalationSignalStale` fires; `SautiEscalationUnacknowledged` quiet.
  - age 0, leader 1: both quiet.
  - dispatch leaders 1 and 0: quiet. 0 and 0, 1 and 1, and absent: `SautiDispatchLeaderMissing` fires.
  - read gauge 0 for 3m: `SautiDispatchOverdueUnreadable` fires.
  - `Watchdog` fires at every evaluation.

  Also write `test_every_alert_names_a_runbook_that_exists` in `governance/tests/test_runbooks.py`. Run `scripts/ci/promtool.sh` and `python3 -m unittest governance.tests.test_runbooks -v`. Expected: FAIL (no rules).
- [ ] **Step 2:** Write the rules, the install script and the CI job (`scripts/tools/install-promtool.sh && scripts/ci/promtool.sh`; `promtool check rules` then `promtool test rules`).
- [ ] **Step 3:** Run both commands. Expected: `SUCCESS`, and PASS.
- [ ] **Step 4:** Commit `feat(ops): dead-man alert rules with promtool tests in CI (EV-2 Task 3; EO-1)`.

### Task 2.4: EV-2 docs

**Files:**
- Create: `docs/runbooks/escalation-signal-stale.md` (from `_template.md`: intake-gw down or leaderless while escalations may be waiting; check `sautictl escalations`; restart order).
- Modify: `docs/runbooks/ivr-controller-down.md` (tier 3 now pages on `SautiEscalationUnacknowledged`; staleness pages on its own), `docs/runbooks/dispatch-worker-down.md` (the leader and read gauges and their alerts), `docs/reference/dependencies.md` (promtool), `docs/traceability.md` (hard constraint 14: the rule tests), `threat-model/threats.yaml` (the dead-man mitigation's `verified_by`), the audit's V-01 status line.
- [ ] **Step 1:** Write the docs; every runbook keeps the template sections.
- [ ] **Step 2:** Run the four gates. Expected: OK.
- [ ] **Step 3:** Commit `docs: dead-man alerts in the runbooks (EV-2 Task 4)`. Push, open the PR, and **ask before merging**. Merge by the four steps of "A new CI job" (Global Constraints): the owner adds "alert rules (promtool)" to the ruleset after the job passes on the head and before the gate merges, because the gate refuses a listed check that the ruleset does not require (O-1).

---

## Phase EV-3: safety invariants are properties

**Branch** `ev-3-safety-invariants` from `main`. **Findings** V-04, V-29, V-33 (the import rule). **Migration** 0039. **ADR** 0043. Merge before M3 Part A.

### Task 3.1: The fold never lowers a severity and refuses what SQL refuses

**Files:** Test `internal/incident/fold_test.go`; create `internal/incident/fuzz_test.go`. Modify `internal/incident/fold.go` only if a test fails on `main`.

- [ ] **Step 1: Write the failing tests:**
  - `TestNoEventLowersSeverity`: for each `from` and `to` in green, yellow and red, and for `EvClassified` and `EvSeverityRaised` (the latter on a projection with the source in `Absorbed`), apply the event to a projection at `from`. When `to` ranks lower, `Apply` returns an error matching `ErrSeverityLowered` or `ErrIllegalTransition`, and the returned projection's severity is `from`.
  - `FuzzFoldSeverityNeverFalls(f, data []byte)`: decode `data` into a sequence of events drawn from the types in `FixtureLifecycle()`, with severities from the three plus `""` and `"purple"`; apply them in order; after every successful `Apply`, the severity's rank is not below the previous one.
  - `TestApplyRejectsMalformedPayloads`: for each event type whose payload the SQL write path validates (`incident.classified`: severity missing, `""`, `"purple"`; `incident.flagged`: flag missing, `""`; `incident.severity_raised`: severity missing, source missing), `Apply` returns an error.

  Apply by hand the audit's mutants at `fold.go:65` (a one-rank drop allowed), `fold.go:155` (`==` for `<=`), `fold.go:62` (`||` to `&&`, mutant I1) and `fold.go:74`. Each must fail at least one test. Revert. Run `go test ./internal/incident/ -count=1`.
- [ ] **Step 2:** Run the fuzz target for 60 s: `go test -run='^$' -fuzz='^FuzzFoldSeverityNeverFalls$' -fuzztime=60s ./internal/incident/`. Expected: no failing input; any found becomes a table case first.
- [ ] **Step 3:** Add `FuzzFoldSeverityNeverFalls` to `scripts/ci/fuzz-smoke.sh`.
- [ ] **Step 4:** Commit `test(incident): severity never falls, for every pair and every sequence (EV-3 Task 1; ES-1, EA-11)`.

### Task 3.2: The SQL write path, every pair, and parity in both directions

**Files:** Test `internal/platform/pg/opstore/opstore_integration_test.go`, `internal/platform/pg/opstore/tool1_integration_test.go`.

- [ ] **Step 1: Write the failing tests:**
  - `TestAppendRejectsEverySeverityLowering`: the 3 × 2 table of Task 3.1 through the real write path; `incident.classified` as `op_intake`, `incident.severity_raised` through the merge path that `merge_integration_test.go::TestSeverityRaisedParity` already sets up. Every lowering returns `ErrSeverityLowered` or `ErrIllegalTransition`.
  - `TestFoldRejectsWhatSQLRejects`: for each payload in `TestSQLRejectsWhatFoldRejects`'s corpus that the SQL refuses, `incident.Apply` also returns an error.

  Apply by hand the audit's mutant M3 (`0018_review_pr10.sql:107`, `- 1` added to the rank comparison, in an overlay or a scratch copy): the first test must fail. Revert. Run `go test -tags integration ./internal/platform/pg/opstore/ -run 'SeverityLowering|FoldRejects' -count=1`.
- [ ] **Step 2:** Expected after any fix: PASS.
- [ ] **Step 3:** Commit `test(opstore): every severity lowering is refused in SQL; parity both ways (EV-3 Task 2; ES-1, EA-11)`.

### Task 3.3: Out-of-domain input is red with a rule error, as a property

**Files:** Modify `internal/triage/fuzz_test.go`, `internal/triage/triage_test.go`.

- [ ] **Step 1: Write the failing tests:**
  - `FuzzClassifyNeverPanics` gains its oracle: if any input is not in `e.Values(name)`, require `Severity == "red"` and `RuleError`; otherwise require `!RuleError`. Add the seed `f.Add("no", "trauma", "age_18_59", "maybe")`.
  - `TestRuntimeErrorIsRed` covers every input name, `age_band` and `specific_danger` included.
  - `TestLoadRejectsNonTotalTable` gains "empty value" and "duplicate value" cases for a domain list (`internal/triage/triage.go:212`).

  Apply by hand the audit's mutant at `triage.go:308` (the domain checked for the first two inputs only): `go test ./internal/triage/` must fail. Revert.
- [ ] **Step 2:** Run `go test ./internal/triage/ -count=1`. Expected: PASS.
- [ ] **Step 3:** Commit `test(triage): out-of-domain input is red with a rule error, for every input (EV-3 Task 3; ES-11)`.

### Task 3.4: A classification needs a logic version (migration 0039)

**Files:** Create `db/operational/migrations/0039_classification_needs_logic_version.sql`. Modify `internal/incident/fold.go` (the `EvClassified` case). Test `internal/incident/fold_test.go`, `internal/platform/pg/opstore/opstore_integration_test.go`; fix fixtures that classify without a version.

**Interfaces:**
- The migration adds `op.refuse_unversioned_classification()`, a trigger function, and a `BEFORE INSERT … FOR EACH ROW` trigger on `op.incident_events` that raises `'illegal transition: a classification needs a logic version' USING ERRCODE = 'SA422'` when the new row is an `incident.classified` event with a NULL or empty logic version. Use the column names the table has (`0001_core.sql`); follow `0006_tool1_projection.sql`'s trigger for the partitioned table. It replaces no function. Its header comment cites this plan, ES-9 and ADR-0043.
- The fold returns `ErrIllegalTransition` for an `EvClassified` event whose `LogicVersion` is empty.

- [ ] **Step 1:** Write `TestApplyRequiresLogicVersionOnClassification` (unit) and `TestAppendRequiresLogicVersionOnClassification` (integration: an append of `incident.classified` with an empty logic version returns `ErrIllegalTransition`; the audit's probe stored it). Run `go test ./internal/incident/ -run LogicVersion -count=1` and `go test -tags integration ./internal/platform/pg/opstore/ -run LogicVersion -count=1`. Expected: FAIL.
- [ ] **Step 2:** Write the migration and the fold check. Run `bin/sqlc generate && bin/sqlc diff` (expected: no output).
- [ ] **Step 3:** Run both commands, then `scripts/ci/unit.sh` and `scripts/ci/integration.sh`. Expected: PASS.
- [ ] **Step 4:** Commit `feat(db): a classification needs a logic version (EV-3 Task 4; migration 0039; ES-9)`.

### Task 3.5: Only the approved bundle runs

**Files:** Modify `config/gated/triage.yaml` (`approved_sha256:` the sha256 of `triage-rules/tool1.yaml`), the triage configuration struct and its loader under `internal/config/`, and `internal/platform/wiring/intake.go:69-86`. Test `internal/platform/wiring/intake_test.go` (create it if absent).

**Rules:** when `approved_sha256` is set, `BuildIntake` refuses a bundle whose bytes hash differently, in every environment; in production it also refuses an empty `approved_sha256`. The signature still proves who signed; the digest pins which signed bundle is approved, so an older or withdrawn bundle signed by the same key is refused.

- [ ] **Step 1:** Write `TestBuildIntakeRefusesAnUnapprovedBundle` (a validly dev-signed bundle with one byte changed in a comment, re-signed) and `TestProductionRequiresAnApprovedDigest`. Run `go test ./internal/platform/wiring/ -run 'Approved|Unapproved' -count=1`. Expected: FAIL.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** Run the same command and the unit suite. Expected: PASS.
- [ ] **Step 4:** Commit `feat(triage): only the approved bundle digest runs (EV-3 Task 5; ES-9)`.

### Task 3.6: Only intake, the wiring and `sautictl` import triage

**Files:** Modify `tools/archcheck/rules.go`, `tools/archcheck/check.go`, and the rule list in `tools/archcheck/main.go`'s comment (R5). Test `tools/archcheck/check_test.go`.

- [ ] **Step 1:** Write `TestOnlyIntakeWiringAndSautictlImportTriage`: a fake package `internal/assist` that imports `internal/triage` gives an R5 violation; `internal/intake`, `internal/platform/wiring` and `cmd/sautictl` give none; test files are exempt. Run `go test ./tools/archcheck/ -count=1`. Expected: FAIL.
- [ ] **Step 2:** Implement R5.
- [ ] **Step 3:** Run the same command and `scripts/ci/arch.sh`. Expected: PASS, 0 violations.
- [ ] **Step 4:** Commit `feat(archcheck): R5, only intake classifies (EV-3 Task 6; ES-10)`.

### Task 3.7: EV-3 docs: ADR-0043

**Files:**
- Create: `docs/adr/ADR-0043-approved-bundle-and-logic-version.md` (index it; supersedes ADR-0019 in part: an approved digest pins the bundle; a classification without a logic version is refused in SQL and in the fold).
- Modify: `governance/ai_risk_manifest.yaml` (AIR-001 and AIR-002 `verified_by`), `threat-model/threats.yaml` (the "severity lowered or triage silently altered" entry), `docs/MODEL_CARD.md` §3 (cite the Go and SQL tables; the approved digest; correct the claim that classifications carry the catalogue fingerprint), `docs/traceability.md` (hard constraint 3; R14), `docs/reference/configuration.md` (`approved_sha256`), `docs/reference/events.md` (the logic version is required), `docs/runbooks/triage-rule-error.md` (start-up refusal on an unapproved digest), the audit's V-04 and V-29 status lines.
- [ ] **Step 1:** Write the docs.
- [ ] **Step 2:** Run the four gates. Expected: OK.
- [ ] **Step 3:** Commit `docs: ADR-0043, model card, risk pointers (EV-3 Task 7)`. Push, open the PR, gate, and **ask before merging**.

---

## Phase EV-4: the clinical evidence covers the table and binds what the caller reads

**Branch** `ev-4-clinical-evidence` from `ev-3-safety-invariants` (it changes the approved digest Task 3.5 adds). **Findings** V-05, V-06. **ADR** 0044.

### Task 4.1: Expected severities as signed data; the engineers' oracle is deleted

**Files:**
- Create: `triage-rules/expected.yaml`, `triage-rules/expected.yaml.sig`.
- Modify: `internal/triage/triage.go` (load and check expectations), `internal/triage/triage_test.go` (delete `oracle()` and `TestRuleTableFromBuildPrompt`), `cmd/sautictl/main.go` (`triage sign --expectations`), `config/gated/triage.yaml` (`expectation_keys:`) and the triage configuration struct and loader under `internal/config/`, `internal/platform/wiring/intake.go` (production refuses pending or dev-signed expectations), every pointer to `TestRuleTableFromBuildPrompt` (`docs/MODEL_CARD.md`, `governance/ai_risk_manifest.yaml`, `threat-model/threats.yaml`, `docs/traceability.md`).

**Interfaces:**
- `type Expectation struct { When map[string]string; Severity string }`: each key is an input name; the value `"*"` matches any value.
- `type Expectations struct { Status string; BundleVersion string; Rows []Expectation }`.
- `func LoadExpectations(data, sig []byte, keys map[string][]byte) (Expectations, error)`: verifies under the context string `"sauti-triage-expectations-v1"`, distinct from the rules' context, using the existing `Sign`/`verify` code path. `keys` is the decoded `expectation_keys`, a trust set separate from the rules' `trusted_keys`: a context separates the documents, not the signers, so only separate keys stop the rules' signer from signing the expectations that check its rules. The configuration loader refuses a public key listed in both sets, and production refuses a `dev-` expectation key as it does a `dev-` rules key.
- `func (e *Engine) CheckExpectations(x Expectations) []string`: one problem for each combination of `e.Domain()` that matches no row, matches more than one row, or whose classification differs from its row; and one if `x.BundleVersion` differs from the bundle's.

**The placeholder data.** `expected.yaml` has `status: pending`, `authored_by: PENDING-MD-PI` and rows derived mechanically from the committed rule, one per region of the rule, non-overlapping. It is signed with a second development key (`dev-expectations-…`), so the development rules key cannot sign it either. It is not clinical authorship: the co-investigators replace it with their own (P-2), signed with a key in `expectation_keys` that the rules' signer does not hold.

- [ ] **Step 1: Write the failing tests:**
  - `TestBundleMatchesClinicalExpectations`: the repository's bundle and expectations give no problem.
  - `TestExpectationsCatchUnderTriage`: a table of the audit's bundle mutants applied to an in-memory copy and re-signed with the dev key: child with danger unknown to green (U1); trauma with a screen-5 danger sign to green (U2); obstetric with danger unknown to green (U3); elderly with a screen-5 danger sign to not red (U4); danger yes with age unknown to green (U5); danger unknown with screen-5 yes to green (U6); an over-triage of "other" (O1); a row's severity changed (K1); a row deleted (K2). Each gives at least one problem.
  - `TestExpectationsMustCoverTheDomain`: with one row removed, the problem names the uncovered combination.
  - `TestLoadExpectationsRejectsTheRulesContext`: a signature made under the rules' context is refused.
  - `TestARulesKeyCannotSignExpectations`: expectations signed under the expectations' context with the rules' key are refused, because that key is not in `expectation_keys`.
  - `TestConfigRefusesAKeyTrustedForBoth`: a public key listed in both `trusted_keys` and `expectation_keys` fails the configuration load.

  Run `go test ./internal/triage/ -run Expectations -count=1`. Expected: FAIL.
- [ ] **Step 2:** Implement; sign the placeholder with the dev key (`sautictl triage sign --expectations`); delete `oracle()` and `TestRuleTableFromBuildPrompt`; repoint their pointers to `TestBundleMatchesClinicalExpectations`.
- [ ] **Step 3:** Run the same command, `scripts/ci/unit.sh` and the three gates. Expected: PASS and OK.
- [ ] **Step 4:** Commit `feat(triage): expected severities as signed data cover the whole domain (EV-4 Task 1; ES-2)`.

### Task 4.2: The questions and labels a caller reads are inside the signature

**Files:**
- Modify: `triage-rules/tool1.yaml` (each input gains `screen_sha256: {en: <hex>, lg: <hex>}`), `triage-rules/tool1.yaml.sig` (re-signed with the dev key), `config/gated/triage.yaml` (the new `approved_sha256`), `internal/triage/triage.go`, `internal/intake/machine.go:423-448` (`CheckTriageDomain`), `cmd/sautictl/main.go` (`triage screens`).
- Test: `internal/intake/machine_test.go`, `internal/triage/triage_test.go`, `tests/e2e/m1_intake_test.go`.

**Interfaces:**
- `func ScreenDigest(prompts, options [][2]string) string` in `internal/triage`: the hex sha256 of each `(key, question)` pair, then a `0x1d`, then each `(value, label)` pair in catalogue order; each field is followed by `0x1f` and each pair by `0x1e`. Screens 2 to 4 have one question with the key `""`; screen 5 has each category-specific question from `specific_danger.prompts` with its key (`acute_medical_child` included), in key order, so a reworded question or two categories' questions swapped changes the digest. It takes plain strings, so `internal/triage` still imports nothing from `internal/config`.
- `func (e *Engine) ScreenSHA256(input, lang string) string`.
- `CheckTriageDomain` also compares, for screens 2 to 5 and each language, `ScreenDigest` of the catalogue's questions and options with the bundle's value, and reports "<input> screen (<lang>) differs from the signed bundle".
- `sautictl triage screens --catalogue config/gated/tool1.yaml` prints each input's digests for the signer to paste.

- [ ] **Step 1: Write the failing tests:**
  - `TestCatalogueScreensMatchTheSignedBundle`, with subtests on a copy of the catalogue: the danger screen's "Yes" and "No" labels swapped; the danger question reworded; the trauma and obstetric questions of screen 5 swapped. Each makes `CheckTriageDomain` return an error naming the input and `en`; the committed catalogue passes.
  - `TestScreenDigestDependsOnEveryField`: order, value, label, question key and question text each change the digest.
  - `TestScenario3ByLabel` (e2e, integration): walks Tool 1 choosing each option by its label text through a helper `walkByLabel(t, …, label string)` that finds the digit in the rendered screen, and reaches yellow as scenario 3 requires.

  Run `go test ./internal/intake/ ./internal/triage/ -run 'Screen' -count=1`. Expected: FAIL.
- [ ] **Step 2:** Implement; compute and paste the digests; re-sign; update `approved_sha256`.
- [ ] **Step 3:** Run the same command, `go test -tags integration ./tests/e2e/ -run ByLabel -count=1`, and the unit suite. Expected: PASS.
- [ ] **Step 4:** Commit `feat(triage): the signed bundle binds each screen's questions and labels (EV-4 Task 2; ES-3)`.

### Task 4.3: What a voice caller hears is inside the signature too

`ivr.Sounds` (`internal/platform/ivr/sounds.go`) names each recording after its screen and the option's value (`sauti/en/danger-yes`), and `TestPromptCatalogueIsComplete` keeps `telephony/prompts/catalogue.yaml` in step, so which code a recording stands for is fixed in code. What can differ is the audio: a "No" recorded as `danger-yes` makes a voice caller give the opposite answer, and Task 4.2's digest cannot see it. The recordings are not in the repository and not yet made.

**Files:**
- Modify: `telephony/prompts/catalogue.yaml` (each sound gains `sha256:`, empty until the recording is made and approved; the `-update` path keeps existing digests), `internal/platform/ivr/` (load the catalogue; the check), `triage-rules/tool1.yaml` (each input gains `recordings_sha256: {en: <hex>, lg: <hex>}`), `triage-rules/tool1.yaml.sig`, `config/gated/triage.yaml` (the new `approved_sha256`), `cmd/sautictl/main.go` (`prompts verify`), `cmd/ivr-controller/main.go`.
- Test: `internal/platform/ivr/catalogue_test.go`, `cmd/sautictl/` tests, `cmd/ivr-controller/` tests.

**Rules:**
- `recordings_sha256` is `ScreenDigest`'s encoding over the `(sound ID, sha256)` pairs of the screen's question and option recordings, so the clinicians and translators who sign the bundle sign which audio voices which code (P-3).
- ivr-controller compares the catalogue with the bundle at start-up, as intake-gw compares the screens, in every environment. In production it also refuses a `placeholder` catalogue or a screen 2 to 5 recording with no `sha256`.
- `sautictl prompts verify --catalogue telephony/prompts/catalogue.yaml --sounds <dir>` hashes each file the catalogue names and fails on a missing file or a different digest, naming each. The P0 deployment runs it on the Asterisk host before Asterisk starts (deferred list).

- [ ] **Step 1: Write the failing tests:**
  - `TestRecordingsAreBoundToTheBundle`: a catalogue whose `danger-yes` digest differs from the one the bundle binds is refused, naming `danger` and the language; the committed catalogue passes.
  - `TestPromptsVerifyRefusesASwappedRecording`: a temporary sounds directory with the `danger-yes` and `danger-no` files swapped fails, naming both.
  - `TestProductionRefusesUnapprovedRecordings`: a `placeholder` catalogue, and an approved one with an empty `sha256` on a screen 2 to 5 recording, each stop ivr-controller under `SAUTI_ENV=production`.

  Run `go test ./internal/platform/ivr/ ./cmd/sautictl/ ./cmd/ivr-controller/ -run 'Recording' -count=1`. Expected: FAIL.
- [ ] **Step 2:** Implement; paste the digests; re-sign; update `approved_sha256`.
- [ ] **Step 3:** Run the same command and the unit suite. Expected: PASS.
- [ ] **Step 4:** Commit `feat(ivr): the signed bundle binds the approved recordings (EV-4 Task 3; ES-3)`.

### Task 4.4: EV-4 docs: ADR-0044

**Files:**
- Create: `docs/adr/ADR-0044-signed-expectations-and-label-binding.md` (index it; supersedes ADR-0019 in part: expectations are signed clinical data under their own context and keys; the bundle binds questions, labels and recordings; production refuses pending expectations and unapproved recordings; the catch-all's severity is the co-investigators' choice, P-2).
- Modify: `docs/MODEL_CARD.md` §3 and §4 (the expectations replace the "independent oracle"; screen and recording binding; what production refuses), `governance/ai_risk_manifest.yaml`, `threat-model/threats.yaml`, `docs/traceability.md` (hard constraint 3), `docs/reference/tool1-flow.md`, `docs/reference/cli-sautictl.md`, `docs/runbooks/triage-rule-error.md` (the screen and recording mismatch refusals), the audit's V-05 and V-06 status lines.
- [ ] **Step 1:** Write the docs.
- [ ] **Step 2:** Run the four gates. Expected: OK.
- [ ] **Step 3:** Commit `docs: ADR-0044, model card, triage reference (EV-4 Task 4)`. Push, open the PR, gate, and **ask before merging**.

---

## Phase EV-5: what a person sees and receives

**Branch** `ev-5-person-facing` from `main`. **Findings** V-08, V-09, V-10, and V-07's interim. **ADR** none (ADR-0041 rule 1).

### Task 5.1: Every responder screen names the incident's own place and category (V-08)

**Files:** Test `internal/respond/ussd_test.go`, `internal/platform/wiring/scenarios_integration_test.go`.

- [ ] **Step 1:** Write `TestEveryResponderScreenNamesTheIncidentsPlace`: a gazetteer ward with two landmarks and a catalogue with two categories; for the accepted, assigned and declined screens, in `en` and `lg`, the text contains the incident's landmark and category labels and neither of the other's. Extend `TestScenario1RedTraumaThroughTheChannels` on the menu path to assert the landmark. Apply mutants R1 (`internal/respond/ussd.go:231`, `==` to `!=`) and R2 (`:223`) by hand: each fails a test. Revert.
- [ ] **Step 2:** Run `go test ./internal/respond/ -run Place -count=1` and `go test -tags integration ./internal/platform/wiring/ -run Scenario1 -count=1`. Expected: PASS.
- [ ] **Step 3:** Commit `test(respond): responder screens name the incident's own place (EV-5 Task 1; EA-1)`.

### Task 5.2: No digit of a number escapes any rendering (V-09)

**Files:** Modify `internal/pii/pii.go`. Test `internal/pii/pii_test.go`; create `tools/archcheck/fields_test.go`.

**Interfaces:** `func (p Phone) Format(f fmt.State, verb rune)` and the same for `PersonName`: write the mask for every verb.

- [ ] **Step 1: Write the failing tests:**
  - `TestNoDigitEscapesAnyRendering`: for a Ugandan, a `+254`, a `+1` and a zero `Phone`, every verb in `v +v #v s q x X d t p e f g b o`, a struct with an exported `Phone` field, a slice, a map, `json.Marshal`, and slog through both the text and JSON handlers: the output contains no run of three or more digits after the country code. Mutants P1 and P2 (`internal/pii/pii.go:81` and `:83`) must fail it.
  - `TestNoStructHoldsAnUnexportedPhone` in `tools/archcheck/fields_test.go`: parse every non-test Go file under `internal/` and `cmd/` with `go/parser`; a struct field of type `pii.Phone` or `*pii.Phone` with a lower-case name fails, because fmt cannot call methods on an unexported field.

  Run `go test ./internal/pii/ ./tools/archcheck/ -count=1`. Expected: FAIL on `%d`, `%t` and `%p`.
- [ ] **Step 2:** Add the `Format` methods.
- [ ] **Step 3:** Run the same command. Expected: PASS.
- [ ] **Step 4:** Commit `fix(pii): every rendering of a phone or a name is masked (EV-5 Task 2; EA-2)`.

### Task 5.3: Redaction at its boundaries and with every separator; the e2e scan sees a foreign caller

**Files:** Modify `internal/platform/logging/redact.go:27`. Test `internal/platform/logging/redact_test.go`, `tests/e2e/m1_intake_test.go:224-240`.

- [ ] **Step 1: Write the failing tests:**
  - `TestRedactionBoundaries`: exactly 9 digits (`772123456`) is redacted; `int64` and `uint64` attributes of 9 or more digits are redacted; a nested struct's number is redacted; an 8-digit control is kept. Mutants L1 (`redact.go:59`, `>=` to `>`) and L2 (`:78`) must fail it.
  - `TestRedactionSeparators`: `0772.123.456`, `(0772) 123456`, `0772/123/456`, `0772_123_456` and `0772` NBSP `123` NBSP `456` are each redacted.
  - `FuzzRedactNeverLeavesNineDigits`: a random 10-digit number with none, one or several separators from that set between any two digits; once every separator character is removed from the output, no run of 9 or more digits remains. Checking the raw output alone would pass a number spaced out digit by digit and left whole.
  - `TestNoCallerNumberInLogs` also dials a `+254` caller and scans every captured line with a regex for the fixture numbers' digits in any grouping.

  Run `go test ./internal/platform/logging/ -count=1`. Expected: FAIL.
- [ ] **Step 2:** Widen the separator class to `[\s.\-/()_\x{00A0}]`, still counting at least 9 digits.
- [ ] **Step 3:** Run the same command and `go test -tags integration ./tests/e2e/ -run NoCallerNumber -count=1`. Expected: PASS. Add the fuzz target to `scripts/ci/fuzz-smoke.sh`.
- [ ] **Step 4:** Commit `fix(logging): redact numbers at the boundary and with any separator (EV-5 Task 3; EA-2, EG-16)`.

### Task 5.4: A ward-level search starts from the incident's ward (V-10)

**Files:** Test `internal/dispatch/engine_test.go:103-118`, `internal/platform/wiring/dispatch_integration_test.go`.

- [ ] **Step 1:** Extend `TestCentresForAWardLevelLocation` to two wards with zones, the incident in the second; assert the second ward's zones are returned. Write `TestWardLevelIncidentAlertsTheWardsResponder` (integration): the test gazetteer's two wards, a responder at a stage in each, an unmatched free-text landmark in the second ward; the first alert goes to the second ward's responder. Apply mutant D3 (`internal/dispatch/engine.go:288`, `&&` to `||`): both fail. Revert.
- [ ] **Step 2:** Run `go test ./internal/dispatch/ -run Centres -count=1` and `go test -tags integration ./internal/platform/wiring/ -run WardLevel -count=1`. Expected: PASS.
- [ ] **Step 3:** Commit `test(dispatch): ward-level search starts from the incident's ward (EV-5 Task 4; EA-5)`.

### Task 5.5: Every abandoned call is in front of a person (V-07 interim)

**Files:** Modify `db/operational/queries/incidents.sql` (a read query), the generated `opdb` code (`bin/sqlc generate`), `internal/platform/pg/opstore/`, `cmd/sautictl/escalations.go`, and `cmd/intake-gw/main.go` (the existing gauge loop). Test `cmd/sautictl/escalations_integration_test.go`, `cmd/intake-gw/main_test.go`.

**Interfaces:**
- `ListAbandonedUnescalated`: open incidents with an `abandoned` flag event and no `incident.escalated` event, returning reference, severity (or none), the danger answer, the last screen answered and the flag's `recorded_at`, most severe first. It reads existing columns only, as `op_readonly` for `sautictl` and through intake-gw's existing store role for the gauge. If a role lacks a grant it needs, stop and ask: that would be migration 0040.
- `sautictl escalations` prints a second section, "Abandoned, not escalated", references only.
- `sauti_abandoned_unescalated{severity}`, with the closed label set `green`, `yellow`, `red`, `unclassified`, set in the gauge loop of Task 2.1 (or today's loop if EV-2 has not merged).

- [ ] **Step 1:** Write `TestEscalationsListsAbandonedUnescalatedCalls` (a yellow incident flagged abandoned at the ward screen is listed with its reference; a danger-yes abandoned call, which is escalated, is not; no fixture phone digits appear in the output) and `TestAbandonedUnescalatedGauge`. Run them. Expected: FAIL.
- [ ] **Step 2:** Implement; `bin/sqlc generate && bin/sqlc diff` (no output).
- [ ] **Step 3:** Run them. Expected: PASS.
- [ ] **Step 4:** Commit `feat: abandoned, unescalated calls are listed and counted (EV-5 Task 5; ES-4 interim)`.

### Task 5.6: EV-5 docs

**Files:** Modify `docs/runbooks/abandoned-danger-callback.md` (a per-shift review of the "Abandoned, not escalated" section, and what the coordinator does under R-3 until the PI decides P-1), `docs/reference/cli-sautictl.md`, `docs/traceability.md` (hard constraint 5's masking pointers; scenario 1), `threat-model/threats.yaml` (T-005's `verified_by`: the new masking and redaction tests), the audit's V-07, V-08, V-09 and V-10 status lines.
- [ ] **Step 1:** Write the docs.
- [ ] **Step 2:** Run the four gates. Expected: OK.
- [ ] **Step 3:** Commit `docs: person-facing evidence and the abandoned list (EV-5 Task 6)`. Push, open the PR, gate, and **ask before merging**.

---

## Phase EV-6: the binaries run in CI

**Branch** `ev-6-binaries` from `main`. **Findings** V-11, and V-35's black-box route. **ADR** none (ADR-0041 rule 6).

### Task 6.1: A smoke test of the built binaries

**Files:** Create `tests/smoke/binaries_integration_test.go` (build tag `integration`, so the existing integration job runs it).

**Behaviour:**
1. `go build -o <tmp> ./cmd/...`.
2. Bootstrap one cluster through `pgtest` and give each binary the DSNs and environment its `main` reads (`SAUTI_ENV=test`, simulated delivery, development keys; read each `cmd/*/main.go` for the list).
3. Start intake-gw, dispatch-worker and outbox-relay as child processes, capturing stdout and stderr; wait for each `/readyz`. Read intake-gw's admin `/metrics` until it holds `sauti_escalation_gauges_leader 1`, within 20 s.
4. Register one responder through the real `sautictl` binary, or the store if no command exists.
5. Walk scenario 3 with `sim.USSD` against intake-gw's real callback route, path token included.
6. Within 30 s expect an `alert.issued` event and a simulated SMS log line naming only the alert ID, and a ledger that grows.
7. Run the built `sautictl verify-ledger` and `verify-projections` against the cluster: exit 0.
8. Send SIGTERM to each child: each exits 0 within 10 s.
9. Scan every child's captured output for each fixture number in every form: none.

- [ ] **Step 1:** Write `TestBinariesDispatchAScenarioEndToEnd`. Apply by hand the audit's mutant M1 (`cmd/dispatch-worker/main.go:177`: the leader parks instead of running the engine), a leader key shared by two binaries, and intake-gw's leader callback no longer starting the gauge loop (EV-2 Task 2.1's `leaderTerm` not called): each fails. Revert. Run `go test -tags integration ./tests/smoke/ -count=1`. Expected after the test is written: PASS on `main`, FAIL on each mutant.
- [ ] **Step 2:** Commit `test(smoke): the built binaries dispatch a scenario end to end (EV-6 Task 1; EA-3, EO-4, EG-8)`.

### Task 6.2: The production refusal is proven on the artifacts

**Files:** Create `scripts/ci/prod-refusal.sh`. Modify `.github/workflows/ci.yml` (a step in the `unit` job, so no new required check).

- [ ] **Step 1:** Write the script: build the four service binaries; run each under `env -i PATH="$PATH" HOME="$HOME" SAUTI_ENV=production` with a 10 s timeout; require exit 1 and `not production ready` on stderr. Delete one `ProductionReady` call by hand: the script fails. Revert.
- [ ] **Step 2:** Run `scripts/ci/prod-refusal.sh`. Expected: four refusals, exit 0.
- [ ] **Step 3:** Commit `ci: prove the production refusal on the built binaries (EV-6 Task 2; EO-12)`.

### Task 6.3: EV-6 docs

**Files:** Modify `docs/traceability.md` (scenario 3 through the binaries; hard constraint 14), `docs/explanation/security-model.md` (the callback route is exercised over TCP), the audit's V-11 status line.
- [ ] **Step 1:** Write the docs; run the four gates (OK); commit `docs: binary smoke evidence (EV-6 Task 3)`. Push, open the PR, gate, and **ask before merging**.

---

## Phase EV-7: the database refuses what no test asked about

**Branch** `ev-7-db-gates` from `main`. **Findings** V-19, V-16. **ADR** 0045. Merge before M3 Part A.

### Task 7.1: Every SECURITY DEFINER function pins its path; runtime roles create nothing

**Files:** Test `internal/platform/pg/opstore/opstore_integration_test.go:628-651`, `internal/platform/pg/schemas_integration_test.go`.
- [ ] **Step 1:** Widen `TestSecurityDefinerFunctionsPinSearchPath` to every store, looping over `schemaOf` (`internal/platform/pg/schemas_integration_test.go`), requiring `search_path=<schema>, pg_temp` on each `prosecdef` function. Write `TestRuntimeRolesCannotCreateObjects`: for every store and runtime role, `has_database_privilege(role, db, 'TEMPORARY')` and `has_schema_privilege(role, schema, 'CREATE')` are false. The audit's mutant (the pin deleted from `db/linkage/migrations/0001_vault.sql:39`) and `TEMPORARY` granted to all roles in `internal/platform/pg/bootstrap/bootstrap.go:101` must each fail one. Run `go test -tags integration ./internal/platform/pg/ ./internal/platform/pg/opstore/ -run 'PinSearchPath|CannotCreate' -count=1`. Expected: PASS on `main`, FAIL on each mutant.
- [ ] **Step 2:** Commit `test(pg): search_path pinned and no CREATE or TEMPORARY, in every store (EV-7 Task 1; EG-6)`.

### Task 7.2: The table-privilege matrix

**Files:** Create `db/privileges.yaml`. Test `internal/platform/pg/schemas_integration_test.go`.

**Format:** `<store>: {<role>: {<schema.table>: [privileges], <schema.table>(<column>): [privileges]}}`, generated once from the migrated stores and reviewed.
- [ ] **Step 1:** Write `TestTablePrivilegesMatchTheMatrix`: for every store, `aclexplode(c.relacl)` over every table and `pg_attribute.attacl` for column grants, compared with `db/privileges.yaml` in both directions. The audit's mutant 2 (`GRANT INSERT, UPDATE, DELETE ON op.incidents, op.incident_events TO op_dispatch, op_ivr, op_console`) and a column grant `UPDATE (severity) ON op.incidents TO op_console` must each fail it. Run `go test -tags integration ./internal/platform/pg/ -run PrivilegesMatch -count=1`. Expected: FAIL (no file), then PASS once the matrix is generated and reviewed.
- [ ] **Step 2:** Commit `test(pg): every table grant matches a reviewed matrix (EV-7 Task 2; EG-7)`.

### Task 7.3: The record-category register

**Files:** Create `governance/record-categories.yaml`, `governance/record-categories-baseline.txt`. Modify `governance/compliance_check.py`. Test `internal/platform/pg/schemas_integration_test.go`, `governance/tests/test_compliance_check.py`.

**Format:** `<store>: {<schema.table>: {retention: <category or PENDING-DPO>, approved_by: <role or pending>, approved_on: <date or null>, columns: [<names>]}}`. The engineers list every table and column; the retention categories stay `PENDING-DPO` until the DPO fills them (D-1). No category name is copied from the confidential records policy.

**The baseline.** `record-categories-baseline.txt` lists, one `<store> <schema.table>` or `<store> <schema.table>(<column>)` per line, the tables and columns that exist when EV-7 merges. Pending approval is allowed only for them:
- A row whose `retention` is `PENDING-DPO`, whose `approved_by` is `pending` or whose `approved_on` is null fails unless its table and each of its columns are in the baseline. So a table or column added after EV-7 fails until its row names a category, the approving role and a date, starting with M3 Part A's four tables.
- The baseline may only shrink. `compliance_check.py` reads the copy at the base commit (`git show <base>:governance/record-categories-baseline.txt`, the base taken from `AMENDGATE_BASE` as the amendment gate takes it) and fails on any line the branch adds; with no copy at the base (EV-7's own PR) it passes. The DPO deletes a line as its row is approved.
- The approval is recorded, not proven: a signed DPO approval waits for the DPO's account and code-owner review (O-1, O-2, D-1).
- [ ] **Step 1:** Write `TestEveryColumnIsInTheDPORegister`: migrate all five stores and compare `information_schema.columns` with the register in both directions. A migration adding `op.probe(x text)`, and a column removed from the register, must each fail it. Add `test_register_rows_name_a_category_and_an_approval`, `test_a_pending_row_outside_the_baseline_fails` (a pending `op.probe` row fails; the same row with a category, a role and a date passes) and `test_the_baseline_only_shrinks` (a line added against the base copy fails; a line removed passes) to the compliance tests. Run both. Expected: FAIL, then PASS once the register lists today's schema.
- [ ] **Step 2:** Commit `feat(governance): every column is in the DPO's register (EV-7 Task 3; EG-3)`.

### Task 7.4: EV-7 docs: ADR-0045

**Files:** Create `docs/adr/ADR-0045-privilege-matrix-and-record-register.md` (index it; ADR-0006 superseded in part: the matrix and the register are tested data, changed in the same PR as the migration). Modify `threat-model/threats.yaml` (T-012: the gap is closed for detection; approval stays the DPO's), `docs/traceability.md` (R2; hard constraints 9 and 12), `docs/runbooks/database-migrations.md` and `docs/how-to/add-a-migration.md` (update the matrix and the register in the same PR), the audit's V-16 and V-19 status lines.
- [ ] **Step 1:** Write the docs; run the four gates (OK); commit `docs: ADR-0045 (EV-7 Task 4)`. Push, open the PR, gate, and **ask before merging**.

---

## Phase EV-8: evidence gates that require a passing run

**Branch** `ev-8-evidence-gates` from `main`. **Findings** V-15, V-30, V-31, V-34 (a). **ADR** 0046.

### Task 8.1: A pointer counts only if its test passed

**Files:** Modify `scripts/ci/integration.sh` (also writes `go test -json` to `dist/test-events.json`), `.github/workflows/ci.yml` (the integration job sets up Python from the governance job's pinned steps, runs the governance suite through `tools.freshness.pyevents` and runs the check), `tools/freshness/__init__.py`, `tools/freshness/__main__.py`, `tools/freshness/pointers.py`, `governance/tests/test_freshness.py:110-113`. Create `tools/freshness/pyevents.py`.

**Interfaces:** `python3 -m tools.freshness --ran dist/test-events.json dist/py-test-events.json`. It collects every non-planned pointer from `docs/traceability.md`, `threat-model/threats.yaml` and `governance/ai_risk_manifest.yaml`, maps each Go pointer's file to its package import path, and fails unless that package has an event `{"Action": "pass", "Test": <name>}`. A `skip`, a `fail` or no event fails, naming the pointer. A Python pointer must name a method of a `unittest.TestCase` subclass in a file under `governance/tests/` (checked with `ast`), and must also have a `pass` event, because a method that calls `skipTest` or carries a skip decorator satisfies the `ast` check. `python3 -m tools.freshness.pyevents dist/py-test-events.json` runs `unittest` discovery over `governance/tests` with a `unittest.TestResult` subclass and writes one event per test in `test2json`'s shape: `Action` `pass` only for a success, `skip` for a skip, and `fail` for a failure, an error, a failed subtest, an expected failure or an unexpected success; `Package` the test file's path; `Test` the method name.
- [ ] **Step 1:** Write `test_a_skipped_test_fails_the_run_check`, `test_a_test_that_did_not_run_fails`, `test_a_method_outside_a_testcase_fails` (replacing the case at `test_freshness.py:110-113`), `test_a_skipped_python_test_fails_the_run_check` (a `TestCase` method that calls `self.skipTest`, run through `pyevents`, fails its pointer) and `test_a_passing_test_satisfies_the_run_check`, each on a small events file. Run `python3 -m unittest governance.tests.test_freshness -v`. Expected: FAIL.
- [ ] **Step 2:** Implement. The integration script runs `-tags integration ./...`, a superset of the unit tests, so one events file covers both tag sets.
- [ ] **Step 3:** Run the tests, then `scripts/ci/integration.sh && python3 -m tools.freshness.pyevents dist/py-test-events.json && python3 -m tools.freshness --ran dist/test-events.json dist/py-test-events.json`. Expected: PASS, every pointer passed. Re-create the audit's probes A (a `t.Skip`), B (a commented-out test) and C (a test behind `//go:build drill`), and a fourth, a pointed-to Python test that calls `self.skipTest`, on a scratch branch: each fails the check, naming the pointer.
- [ ] **Step 4:** Commit `feat(freshness): a pointer counts only if its test passed in CI (EV-8 Task 1; EG-1, EA-6)`.

### Task 8.2: Status means something; planned markers expire; reviews are dated

**Files:** Modify `threat-model/threats.yaml`, `governance/ai_risk_manifest.yaml`, `threat-model/validate_threats.py`, `governance/compliance_check.py`, `tools/freshness/__init__.py` (the traceability Status column). Create `governance/current_milestone` (one line, the last merged milestone) and `governance/linddun.txt` (the seven canonical names). Test `governance/tests/test_validate_threats.py`, `governance/tests/test_compliance_check.py`, `governance/tests/test_freshness.py`.

**Rules:**
- Every threat and AI risk has `status`: `mitigated` (no planned pointer), `partial` or `open` (at least one `planned:` pointer with a milestone), or `accepted` (`residual:`, `accepted_by:` a role, and `acceptance:` an issue URL; issue #16's items use it).
- `linddun` is validated whenever present; the five non-canonical values (T-017, T-020, T-022, T-026, T-031) are normalised.
- A `planned:Mn` marker fails when `n` is at or below `governance/current_milestone` (F-73, as UX-8 Task 8.7 specified; that task drops this part).
- In `docs/traceability.md`, `Verified` needs a resolving pointer and no remaining text; `Partial` needs both; `Planned` needs none.
- `threats.yaml` and the manifest carry `reviewed_on` and `reviewed_by` (a role); freshness fails when `reviewed_on` predates the last merged milestone's merge date.
- [ ] **Step 1:** Write the tests: the audit's T-099 and T-100 probes fail; a `mitigated` threat with a planned pointer fails; `accepted` without an issue fails; R12 marked `Verified` with no proof fails; a stale `reviewed_on` fails. Run the three test modules. Expected: FAIL.
- [ ] **Step 2:** Implement; set every entry's honest status; replace stale planned markers with the test that now proves them, or an honest later milestone.
- [ ] **Step 3:** Run the tests and the four gates. Expected: PASS and OK.
- [ ] **Step 4:** Commit `feat(governance): status, expiry and review dates the gates can check (EV-8 Task 2; EG-5, EG-11, EG-14, EG-15)`.

### Task 8.3: The AIBOM and the model card match what runs

**Files:** Modify `governance/ai_risk_manifest.yaml` (each AIBOM entry gains `source: <file>:<yaml key>`, for example `triage-rules/tool1.yaml:version`), `governance/compliance_check.py`, `docs/MODEL_CARD.md` (header, column title, statuses). Test `governance/tests/test_compliance_check.py`.

**Rules:** the gate reads each `source` and fails when `version` differs; an entry whose source exists and has no `flag` must be `status: active`; the model card's component table names each `bom-ref` with the same status word; `iso42001_clause` must be in a list of the standard's clause and Annex A identifiers kept in `governance/`.
- [ ] **Step 1:** Write a test for each line of the audit's probe (`ranking_version` changed; zone ranking `planned`; clause "banana"; a model card status that differs). Run them. Expected: FAIL.
- [ ] **Step 2:** Implement, and correct today's drift: the triage table is active at `tool1-placeholder-0.1`.
- [ ] **Step 3:** Run them and the four gates. Expected: PASS and OK.
- [ ] **Step 4:** Commit `feat(governance): the AIBOM and model card are checked against what runs (EV-8 Task 3; EG-9, ES-12)`.

### Task 8.4: The privacy threats the model lacks

**Files:** Modify `threat-model/threats.yaml`.
- [ ] **Step 1:** Add a threat for LINDDUN Unawareness and unintervenability (data-subject rights, including reconsideration of an automated decision under DPPA s.27) and one for Detecting (an SMS or call-back reveals to a household that an emergency call was made). Each has `status: open`, its mitigations as they stand, and pointers to existing tests or `planned:` markers. The DPO's decision (D-2) sets their final mitigations. Run `python3 threat-model/validate_threats.py`. Expected: OK.
- [ ] **Step 2:** Commit `docs(threats): unawareness and detecting (EV-8 Task 4; EG-10)`.

### Task 8.5: EV-8 docs: ADR-0046

**Files:** Create `docs/adr/ADR-0046-run-proven-evidence-and-status.md` (index it; ADR-0016 superseded in part: a pointer is evidence only when its test passed in that CI run; a register's status is checked). Modify `docs/explanation/security-model.md` and `docs/MODEL_CARD.md` where they describe the gates, and the audit's V-15, V-30, V-31 and V-34 status lines.
- [ ] **Step 1:** Write the docs; run the four gates and the run check (OK); commit `docs: ADR-0046 (EV-8 Task 5)`. Push, open the PR, gate, and **ask before merging**.

---

## Phase EV-9: the fakes answer as the providers do

**Branch** `ev-9-provider-contracts` from `main`. **Findings** V-12. **ADR** 0047.

### Task 9.1: Asterisk executes the dialplan in CI

**Files:** Create `scripts/tools/install-asterisk.sh` (the Asterisk major that ADR-0023 names, built from the official source tarball, sha256-pinned through `scripts/tools/fetch.sh`, cached by `actions/cache` keyed on that sha256), `tests/contract/asterisk_contract_test.go` (build tag `contract`), `scripts/ci/contract.sh`. Create or modify `.github/workflows/advisory.yml` (a job named "provider contracts (Asterisk)" on every PR, not yet required; after a week without a flake the owner moves it into `ci.yml` by the steps of "A new CI job").

**Behaviour:** start Asterisk with `telephony/asterisk/*.conf` and no ARI application connected; originate `Local/<did>@<inbound context>` with a test caller ID; observe through AMI that a `Dial` reaches the coordinator endpoint with `CALLERID(num)` equal to `SAUTI_CALLER_ID` before any dial, and that the retries and the closing message follow ADR-0034.
- [ ] **Step 1:** Write `TestStasisDownReachesACoordinatorWithTheCallerMasked`. A `Goto` to a missing label in the fallback context, and `Set(CALLERID(num)=…)` moved after the first `Dial`, must each fail it. Run `scripts/ci/contract.sh`. Expected: PASS on `main`, FAIL on each fault.
- [ ] **Step 2:** Commit `test(contract): Asterisk executes the fallback dialplan (EV-9 Task 1; EA-4)`.

### Task 9.2: `FakeARI` replays what Asterisk sends

**Files:** Create `internal/platform/sim/testdata/ari/*.json` (recorded by `tests/contract/asterisk_contract_test.go` with `-record`, each file noting its Asterisk version and date). Test `internal/platform/sim/ari_test.go`.
- [ ] **Step 1:** In the contract test, run the real `ari.Client` through answer, play, stop a playback and hang up, and save the event stream. Write `TestFakeARIMatchesRecordedAsteriskEvents`: the fake's events for the same calls have the recorded types, order and field sets. A fake that emits `PlaybackFinished` before `PlaybackStarted` must fail it.
- [ ] **Step 2:** Run `go test ./internal/platform/sim/ -run Recorded -count=1`. Expected: PASS.
- [ ] **Step 3:** Commit `test(sim): FakeARI matches recorded Asterisk events (EV-9 Task 2; EA-4)`.

### Task 9.3: Africa's Talking recorded fixtures (blocked on O-3)

**Precondition:** the operator has recorded, in the sandbox, an SMS send in each documented status, a delivery report and a USSD callback (O-3). Until then this task does not start; recording a fixture by hand from the documentation is not allowed.

**Files:** Create `internal/platform/africastalking/testdata/*` (each with its recording date). Modify `internal/platform/africastalking/sms_test.go:18-45` and the USSD tests so the fakes serve exactly those bytes.
- [ ] **Step 1:** Write `TestSMSFakeServesRecordedResponses` and `TestUSSDCallbackMatchesTheRecordedForm`. Run `go test ./internal/platform/africastalking/ -count=1`. Expected: PASS once the fixtures exist.
- [ ] **Step 2:** Commit `test(africastalking): fakes replay recorded sandbox responses (EV-9 Task 3; EA-4)`.

### Task 9.4: EV-9 docs: ADR-0047

**Files:** Create `docs/adr/ADR-0047-provider-contract-tests.md` (index it; recorded fixtures, the Asterisk job, how to re-record). Modify `docs/reference/dependencies.md` (Asterisk source pin), `docs/runbooks/ivr-controller-down.md` (the fallback is now executed in CI), `docs/traceability.md`, the audit's V-12 status line.
- [ ] **Step 1:** Write the docs; run the four gates (OK); commit `docs: ADR-0047 (EV-9 Task 4)`. Push, open the PR, gate, and **ask before merging**.

---

## Phase EV-10: latency is measured and failover is exercised

**Branch** `ev-10-latency-failover` from `main`. **Findings** V-13, V-14 (a), V-28. **ADR** none.

### Task 10.1: The USSD adapter answers within its budget

**Files:** Test `internal/platform/africastalking/ussd_test.go`.
- [ ] **Step 1:** Write `TestATAnswersTheVoiceLineWithinTheBudget`: the port blocks until its context ends; the reply arrives in under 1 s and is `END` with the configured error text. The audit's mutant M1 (`ussd.go:74`, 60 s) must fail it. Run `go test ./internal/platform/africastalking/ -run Budget -count=1`. Expected: PASS on `main`.
- [ ] **Step 2:** Commit `test(africastalking): the reply arrives within the budget (EV-10 Task 1; EO-2)`.

### Task 10.2: The poll the binary uses is the poll the test uses

**Files:** Modify `internal/dispatch/` (export `const DefaultPoll = time.Second`), `cmd/dispatch-worker/main.go:58`. Test `internal/platform/wiring/dispatch_integration_test.go`.
- [ ] **Step 1:** Write `TestFirstAlertWithinTenSecondsOfT2`: `Engine.Run` with `dispatch.DefaultPoll` on a red incident; T3 − T2, both read from `recorded_at`, is under 10 s. With `DefaultPoll` at 60 s, and at 15 s, it fails. Run `go test -tags integration ./internal/platform/wiring/ -run FirstAlert -count=1`. Expected: PASS.
- [ ] **Step 2:** Commit `test(dispatch): first alert within 10 s of T2 at the binary's poll (EV-10 Task 2; EO-2)`.

### Task 10.3: A first-alert histogram and the service-level page

**Files:** Modify `internal/platform/metrics/metrics.go` (UX-8 Task 8.1's `Histogram`, exactly as specified there, unless it has merged), `internal/dispatch/engine.go` (observe T3 − T2 from database times at settle), `cmd/dispatch-worker/main.go`. Create or extend `docs/reference/slos.md` (each Build Prompt §14 target, PENDING the PI, P-7). Test `internal/platform/metrics/metrics_test.go`, `internal/dispatch/engine_test.go`.
- [ ] **Step 1:** Write `TestHistogramExposition` (if Task 8.1 has not merged) and `TestFirstAlertLatencyIsObservedFromDatabaseTimes`. Run them. Expected: FAIL.
- [ ] **Step 2:** Implement `sauti_dispatch_first_alert_seconds`.
- [ ] **Step 3:** Run them. Expected: PASS. Commit `feat(metrics): first-alert latency histogram (EV-10 Task 3; EO-2, ES-8)`.

### Task 10.4: The engine survives a database restart mid-cascade

**Files:** Test `internal/platform/wiring/dispatch_integration_test.go` (its own embedded PostgreSQL, not the shared `pgtest` cluster, so it also runs in CI).
- [ ] **Step 1:** Write `TestEngineSurvivesADatabaseRestartMidCascade`: `Engine.Run` on a yellow cascade; stop the server for 10 s; start it on the same data directory. Each rung is delivered exactly once, the projection does not drift, and a leader holds the lock again within 30 s. A claim retried after the restart (a duplicate alert) must fail it. Run `go test -tags integration ./internal/platform/wiring/ -run Restart -count=1`. Expected: PASS.
- [ ] **Step 2:** Correct `docs/traceability.md:72` to "verified for a session kill and a server restart; a Patroni switchover is P0". Commit `test(wiring): dispatch survives a database restart (EV-10 Task 4; EO-5)`.

### Task 10.5: A slow database, and a half-open ARI stream

**Files:** Test `internal/intake/` or `internal/platform/africastalking/` integration tests (a pgx `DialFunc` wrapper that delays each read by 900 ms), `internal/platform/ari/client_test.go` (only if UX-2 Task 2.5 merged without the black-hole test).
- [ ] **Step 1:** Write `TestASlowDatabaseStillAnswersWithinTheBudget`: the USSD reply is the voice-line screen within 1 s, and the incident is not half-written (the `TestEveryWriteFailureConverges` style). If needed, write `TestABlackHoledStreamFailsReadiness`: a proxy that stops forwarding without closing; readiness fails within three heartbeat intervals. Run them. Expected: PASS.
- [ ] **Step 2:** Commit `test: slow database and half-open stream faults (EV-10 Task 5; EO-11)`.

### Task 10.6: EV-10 docs

**Files:** Modify `docs/reference/slos.md`, `docs/runbooks/dispatch-worker-down.md` (the histogram), `docs/traceability.md` (scenario 14), the audit's V-13, V-14 and V-28 status lines.
- [ ] **Step 1:** Write the docs; run the four gates (OK); commit `docs: latency and failover evidence (EV-10 Task 6)`. Push, open the PR, gate, and **ask before merging**.

---

## Phase EV-11: mutation, flakes, race and the schedule

**Branch** `ev-11-ci-depth` from `main`. **Findings** V-20, V-21 (rest), V-22, V-23. **ADR** none (ADR-0041 rule 3; the tool goes in `docs/reference/dependencies.md`).

### Task 11.1: The race detector is required in CI

**Files:** Modify `scripts/ci/unit.sh`, `scripts/ci/integration.sh`. Test `governance/tests/test_ci_scripts.py`.
- [ ] **Step 1:** Write `test_race_detector_is_required_in_ci`: each script run with `CI=true` and `CGO_ENABLED=0` exits 1 with "race detector required in CI" before running any test. Run it. Expected: FAIL.
- [ ] **Step 2:** Add as each script's first check: `if [ "${CI:-}" = true ] && [ "$(go env CGO_ENABLED)" != 1 ]; then echo "race detector required in CI" >&2; exit 1; fi`.
- [ ] **Step 3:** Run it. Expected: PASS. Commit `ci: the race detector cannot silently drop out (EV-11 Task 1; EA-8, EC-12)`.

### Task 11.2: Integration shuffles; fixed sleeps become polls

**Files:** Modify `scripts/ci/integration.sh` (`-shuffle=on`; the seed is printed), `internal/platform/pg/relay_integration_test.go:201-205`, `internal/platform/wiring/voice_integration_test.go:97-100`, `internal/platform/sim/ari.go` (a per-event channel), `internal/platform/ivr/asterisk_test.go` (`TestControllerOverAsteriskQueue` waits on events, not a 3 s clock).
- [ ] **Step 1:** Run `go test -count=50 -run TestControllerOverAsteriskQueue ./internal/platform/ivr/` under load (`stress -c 8`, or a parallel `go test ./...`). Expected: at least one failure on `main`.
- [ ] **Step 2:** Make the changes. Run the same command. Expected: 50 passes. Run `scripts/ci/integration.sh` twice with different seeds. Expected: PASS.
- [ ] **Step 3:** Commit `test: shuffle integration; wait on events, not clocks (EV-11 Task 2; EA-8)`.

### Task 11.3: Fuzz findings are kept; the USSD text history is a property

**Files:** Modify `.github/workflows/ci.yml` (the fuzz job uploads `**/testdata/fuzz/**` on failure and caches the fuzz cache). Create `internal/intake/fuzz_text_test.go`.
- [ ] **Step 1:** Write `FuzzUSSDTextHistory`: feeding cumulative `a*b*c` texts writes one event per new answer and returns `ErrInputHistory` for any text that does not extend the last. A `newInput` mutant that drops the `r.Text[l] != '*'` check (`internal/intake/service.go:293-309`) must fail it. Run `go test -run='^$' -fuzz='^FuzzUSSDTextHistory$' -fuzztime=60s ./internal/intake/`. Expected: no failure.
- [ ] **Step 2:** Add it to `scripts/ci/fuzz-smoke.sh`. Commit `test(intake): the USSD text history is a fuzzed property; crashers are kept (EV-11 Task 3; EA-7)`.

### Task 11.4: Mutation testing on the safety packages

**Files:** Modify `build/tools.mod` and `build/tools.sum` (gremlins, the latest release, verified at task time), `docs/reference/dependencies.md`. Create `scripts/ci/mutation.sh`. Create or modify `.github/workflows/advisory.yml` (a job named "mutation (changed lines)").

**Behaviour:** `scripts/ci/mutation.sh <base>` runs gremlins over the lines the PR changes in the packages ADR-0041 rule 3 lists, with logical and loop-control operators on, and fails below 80 % test efficacy. Check the flag names against the pinned release's help before use. The job is report-only until M3 merges: its step sets `continue-on-error: true` and writes the efficacy and the survivors to the job summary, because the gate refuses a failed check run even when it is not required. Then the owner moves it into `ci.yml` and makes it required by the steps of "A new CI job". The weekly full run is Task 11.5.
- [ ] **Step 1:** On a scratch branch that re-applies mutant R1's line change as a "refactor" of `internal/respond/ussd.go` (with EV-5's test absent), run the script. Expected: R1 reported as lived and the script exits non-zero (the job summary shows it). Revert.
- [ ] **Step 2:** Run it on this branch against `main`. Expected: no changed safety lines, PASS.
- [ ] **Step 3:** Commit `ci: mutation testing on changed safety lines (EV-11 Task 4; EA-10)`.

### Task 11.5: The schedule

**Files:** Create `.github/workflows/nightly.yml` (`schedule:` and `workflow_dispatch`; `permissions: contents: read, issues: write`, the only workflow with a write scope).

**Jobs:** the vulnerability scans of `scripts/ci/vuln.sh` plus OSV-Scanner over `build/tools.mod`; `FUZZTIME=10m scripts/ci/fuzz-smoke.sh`; `go test -race -count=20 -shuffle=on -tags integration` over `./internal/platform/ivr/ ./internal/dispatch/ ./internal/platform/wiring/`; weekly, the full gremlins run over the safety packages with the survivor list uploaded. Any failure opens one issue labelled `nightly` with the job link; the open `nightly` issues are the quarantine list.
- [ ] **Step 1:** Write the workflow. Trigger it with `gh workflow run nightly.yml`, once on `main` (expected: green) and once on a scratch branch whose `go.mod` pins `golang.org/x/net` at v0.32.0 with a reachable `html.Parse` (expected: the vulnerability job red and an issue opened).
- [ ] **Step 2:** Commit `ci: a nightly schedule for scans, long fuzzing, stress and mutation (EV-11 Task 5; EC-9, EG-8, EA-8)`.

### Task 11.6: EV-11 docs

**Files:** Modify `docs/reference/dependencies.md`, `docs/explanation/security-model.md` (the scheduled scans), the audit's V-20 to V-23 status lines.
- [ ] **Step 1:** Write the docs; run the four gates (OK); commit `docs: CI depth and schedule (EV-11 Task 6)`. Push, open the PR, gate, and **ask before merging**.

---

## Phase EV-12: drills, clocks and timelines

**Branch** `ev-12-drills-clocks` from `ev-2-dead-man-rules` (it adds to the rules file). **Findings** V-24, V-26, V-27. **ADR** none.

### Task 12.1: Drill freshness is checked

**Files:** Modify `tools/freshness/__init__.py`, `config/platform.yaml` (`go_live: false`), `docs/runbooks/_template.md` (`Last drilled:` and an optional `Drill every: <days>`). Test `governance/tests/test_freshness.py`.

**Rules:** `Last reviewed:` is an ISO date no older than 180 days; `Last drilled:` is an ISO date or `not yet drilled`; with `go_live: true`, a runbook that declares `Drill every: N` fails when it is undrilled or last drilled more than N days ago.
- [ ] **Step 1:** Write the tests: the audit's mutant M4 (`Last reviewed` 1999-01-01, `Last drilled` deleted) fails; a stale drill fails only when `go_live` is true. Run them. Expected: FAIL.
- [ ] **Step 2:** Implement; date every runbook's fields as they stand.
- [ ] **Step 3:** Run the tests and the four gates. Expected: PASS and OK. Commit `feat(freshness): runbook review and drill dates are checked (EV-12 Task 1; EO-6)`.

### Task 12.2: The database clock offset is measured; T-time order is verified

**Files:** Modify `cmd/dispatch-worker/main.go` (the existing 5 s ticker), `build/prometheus/sauti-rules.yml` and its test file, `cmd/sautictl/` (`verify-projections` reports T-times out of order). Test `cmd/dispatch-worker/main_test.go`, `cmd/sautictl/` tests.

**Interfaces:** `sauti_db_clock_offset_seconds`: the store's `Now` minus the local midpoint of the round trip. It stamps nothing. Rule `SautiDatabaseClockOffset`: `abs(sauti_db_clock_offset_seconds) > 0.02` for 5m.
- [ ] **Step 1:** Write `TestTheClockOffsetGaugeReadsTheDatabase` (a fake store one second ahead: the gauge reads about 1), the rule's promtool cases, and `TestVerifyProjectionsReportsTimesOutOfOrder`. Run them. Expected: FAIL.
- [ ] **Step 2:** Implement. Run them. Expected: PASS. Commit `feat: database clock offset and T-time order checks (EV-12 Task 2; EO-8)`.

### Task 12.3: `sautictl incident timeline`

**Files:** Modify `cmd/sautictl/main.go`, `db/operational/queries/` (read queries), the generated code. Test `cmd/sautictl/` integration tests.

**Interfaces:** `sautictl incident timeline --dsn <op_readonly> --ref <reference>`: events, alerts, attempts, delivery reports, bridge and coordinator calls in database-time order, IDs only.
- [ ] **Step 1:** Write `TestTimelineOrdersScenario1ByDatabaseTime` (T1 < T2 < T3 < T4) and `TestTimelinePrintsNoNumber` (no fixture number in any form; a variant that prints the opened phone must fail it). Run them. Expected: FAIL.
- [ ] **Step 2:** Implement; `bin/sqlc generate && bin/sqlc diff` (no output). Run them. Expected: PASS. Commit `feat(sautictl): incident timeline (EV-12 Task 3; EO-10)`.

### Task 12.4: EV-12 docs

**Files:** Modify `docs/reference/cli-sautictl.md`, the runbooks that gain `Drill every` (as the owner sets cadences, O-4; until then none declares one), `docs/runbooks/projection-drift.md` (T-time order), the audit's V-24, V-26 and V-27 status lines.
- [ ] **Step 1:** Write the docs; run the four gates (OK); commit `docs: drills, clocks, timeline (EV-12 Task 4)`. Push, open the PR, gate, and **ask before merging**.

---

## Phase EV-13: the supply chain's remaining gaps

**Branch** `ev-13-supply-chain` from `main`. **Findings** V-37, V-40, V-41 (a). **ADR** none.

### Task 13.1: The secret scan finds database credentials

**Files:** Create `.gitleaks.toml` (`[extend] useDefault = true`, a `postgres-uri-password` rule matching `postgres(?:ql)?://[^:/\s"'@]+:([^@\s"'<>$]{8,})@` with entropy 3.0, the development placeholder allowlisted), `.gitguardian.yaml` (false positives recorded in Git, not in a dashboard). Modify `scripts/ci/secrets.sh` (a self-test first: it writes a DSN with a random password into a temporary repository at run time and requires gitleaks to find it; no secret is committed).
- [ ] **Step 1:** Run `scripts/ci/secrets.sh` before the config. Expected: the self-test FAILS (the default rules miss the DSN).
- [ ] **Step 2:** Add the config. Run it. Expected: the self-test passes and the full history has no leak.
- [ ] **Step 3:** Commit `ci: gitleaks finds PostgreSQL credentials (EV-13 Task 1; EC-5)`.

### Task 13.2: One SBOM per binary, with licences and the AI components

**Files:** Modify `scripts/ci/sbom.sh`, `.github/workflows/ci.yml` (upload every SBOM). Test: the script's own assertions.
- [ ] **Step 1:** Switch to `cyclonedx-gomod app -licenses -json -main ./cmd/<each>`, CycloneDX 1.6; fail when `jq '[.components[] | select(.licenses == null)] | length'` is not 0; append each `aibom_inventory` entry as a component (`type`, `version`, `supplier.name`, `modelCard` for models) and fail if any `bom-ref` is missing. Run `scripts/ci/sbom.sh`. Expected: PASS, every component licensed.
- [ ] **Step 2:** Commit `ci: per-binary SBOMs with licences and AI components (EV-13 Task 2; EC-10, EG-17)`.

### Task 13.3: A security policy

**Files:** Create `SECURITY.md`: report through GitHub's private vulnerability reporting (no personal address), what is in scope (the repository, the public demo), the response timeline the owner sets.
- [ ] **Step 1:** Write it; commit `docs: SECURITY.md (EV-13 Task 3; EC-14)`.

### Task 13.4: EV-13 docs

**Files:** Modify `docs/reference/dependencies.md`, `docs/explanation/security-model.md`, the audit's V-37, V-40 and V-41 status lines.
- [ ] **Step 1:** Write the docs; run the four gates (OK); commit `docs: supply-chain evidence (EV-13 Task 4)`. Push, open the PR, gate, and **ask before merging**.

---

## Conditions on open pull requests

These belong to the open PRs' own branches, not to an EV phase. The gate's review of each PR checks them.
- **PR #15 (demo).** Before it merges: `pgBin` calls `t.Fatal` instead of `t.Skip` when `CI` is set; a CI job without `SAUTI_TEST_PG_DSN` fills the embedded PostgreSQL cache and runs the edge suite; the suite asserts T3 − T2 ≤ 10 s from the board's event times instead of a 30 s wait; `NoFullNumberAnywhere` also scans the children's log; the Dockerfile pins `golang` by digest; a CI job builds the image and runs an OSV image scan; the stage script refuses a commit that is not an ancestor of `origin/main` (V-11, V-36). Its GitGuardian false positive goes into `.gitguardian.yaml` (EV-13 Task 1) rather than the dashboard.
- **PR #18 (edge cases).** G-1 is re-graded High and cross-referenced to V-07 and P-1.
- **PR #17 (layout).** No condition from this audit beyond the gate.

## Deferred, with the reason

| Finding | What | Why not now | Unblocked by |
|---|---|---|---|
| V-03 | Human approval, code-owner review, clinical accounts | A person, not code | The owner (O-1, O-2) |
| V-03 (EG-2, part) | Amendment records signed with a PI key pinned at the base | The study window is inactive | Before the window opens |
| V-07 | Escalating abandoned yellow and danger-unknown calls | A protocol change | The PI (P-1); EV-5 gives the interim |
| V-06 (part) | Recording and approving the IVR audio; `sautictl prompts verify` in the deployment | Not yet recorded; no Asterisk host | The co-investigators and translators (P-3); P0 |
| V-12 (part) | The carrier's connected-line behaviour | Needs the real trunk | P0 staging drill |
| V-14 (part) | Patroni switchover under load; monthly restore and `pgbackrest verify` | No cluster exists | P0 |
| V-17 | The under-triage monitor and the clinical metrics | Definitions are clinical | The PI and Medical Director (P-4); then M3 (count) and M6 (metric SQL) |
| V-18 | Usability engineering | User studies | The PI (P-5); M7 |
| V-23 (part), V-01 (part) | Production verifier timers, the external witness, paging receivers, and a dead man's switch off the monitoring host that pages a person when the Watchdog stops, with a drill that stops Alertmanager | Infrastructure | P0 (R-16) |
| V-25 | Synthetic probes and drill markers | A study and record-kind decision | The PI and DPO (P-8, D-4); M7 |
| V-32 | Hazard log, safety case, IEC 62304 class | Needs the acceptability policy | The Medical Director, PI and sponsor (P-6) |
| V-33 | Shadow-mode evaluation and disclosure for speech assist | Speech assist is P2 | P2, with an amendment and a DPIA; the PI's ADR on the Act (P-9) |
| V-34 (part) | DPIA risk numbers on each threat; s.27 notice | The DPO's mapping and decision | The DPO (D-2) |
| V-35 | An ASVS requirement-level checklist | Most of the surface (M3–M6) does not exist yet | Before the M7 penetration test |
| V-36 | Demo image build and scan | The files live on PR #15 | PR #15's conditions |
| V-38 | Signed commits | Key set-up and a ruleset rule | The owner (O-1) |
| V-39 | Release, provenance, signing | Needs Forgejo and Harbor | P0 |
| F-83 (EO-9) | Load and soak | Field-scale evidence | M7, as the UX plan places it |
| EG-13 | The breach clock | Legal | The DPO (D-3, R-5) |

## Coverage check

Every class (a) finding has a task; the rest are in the deferred list.

| Finding | Task |
|---|---|
| V-01 | 2.1, 2.2, 2.3, 2.4 |
| V-02 | 1.1, 1.2, 1.3, 1.5 |
| V-03 | 1.1 (ruleset check), 1.5 (docs); deferred (owner) |
| V-04 | 3.1, 3.2, 3.3, 3.6 |
| V-05 | 4.1 |
| V-06 | 4.2, 4.3; deferred (recordings, P0) |
| V-07 | 5.5 (interim); deferred (P-1) |
| V-08 | 5.1 |
| V-09 | 5.2, 5.3 |
| V-10 | 5.4 |
| V-11 | 6.1, 6.2; PR #15 conditions |
| V-12 | 9.1, 9.2, 9.3 |
| V-13 | 10.1, 10.2, 10.3 |
| V-14 | 10.4; deferred (P0) |
| V-15 | 8.1 |
| V-16 | 7.3 |
| V-17, V-18 | deferred |
| V-19 | 7.1, 7.2 |
| V-20 | 11.4, 11.5 |
| V-21 | 1.4, 11.3; UX-8 Task 8.6 |
| V-22 | 11.1, 11.2, 11.5 |
| V-23 | 11.5; deferred (P0) |
| V-24 | 12.1 |
| V-25 | deferred |
| V-26 | 12.2 |
| V-27 | 12.3 |
| V-28 | 10.5; UX-2 Task 2.5 |
| V-29 | 3.4, 3.5 |
| V-30 | 8.2 |
| V-31 | 8.3 |
| V-32, V-33 | deferred (V-33's import rule: 3.6) |
| V-34 | 8.4; deferred (D-2) |
| V-35 | 6.1 (route); deferred (checklist) |
| V-36 | PR #15 conditions |
| V-37 | 13.1 |
| V-38, V-39 | deferred |
| V-40 | 13.2 |
| V-41 | 13.3; owner (O-1); UX-7 Task 7.4 |

**Migrations.**

| Number | Task | Content |
|---|---|---|
| 0039 | 3.4 | A trigger refuses a classification without a logic version |

No task replaces `op.append_incident_event` or `op.append_event_core`, and none edits a merged file.

**ADRs.** ADR-0041 (EV-0, this plan's strategy), ADR-0042 (1.5), ADR-0043 (3.7), ADR-0044 (4.4), ADR-0045 (7.4), ADR-0046 (8.5), ADR-0047 (9.4).
