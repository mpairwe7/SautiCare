---
status: accepted
date: 2026-11-14
decision-makers: Sauti engineering (repository owner)
consulted: the November 2026 evaluation audit (V-02, V-03, owner decision O-1); ADR-0014; ADR-0041
informed: PI, Medical Director, Data Protection Officer, technical operator
---
# ADR-0042: A fail-closed merge gate in the repository

## Context and Problem Statement

Until now, pull requests reached `main` through scripts kept outside the repository. The [evaluation audit](../explanation/evaluation-audit-2026-11.md) (V-02) ran those scripts against a stub `gh`, and they merged in 5 of 5 fault cases:
- `gh` errors;
- required jobs missing;
- a stale thread verdict after a rate limit;
- an unreadable review decision;
- a reviewer that never fell quiet.

Their thread-resolve call had also been silently broken by shell expansion. On the same day, `main` was found to have no protection at all (V-03). It is now restored as ruleset 27260742 on the owner's instruction.

[ADR-0014](ADR-0014-configuration-and-amendment-gate.md) relied on branch protection plus code-owner review to protect the amendment gate's own code. Code-owner review is not enforced: CODEOWNERS lists only the author.

What decides that a change may merge, and how is that decision itself made trustworthy?

## Decision Drivers

* A gate that nobody watches must fail closed: an unreadable answer is a no.
* CodeRabbit is the only reviewer besides the author. Its findings must be settled on its own last word, not on ours.
* CodeRabbit's open-source quota is about one review an hour, and its status check passes even when it is rate-limited. The check proves nothing, but its review does.
* The gate is code that guards code, so it is versioned, reviewed and tested like the rest (ADR-0041).
* The owner decided O-1 on 2026-11-14: a PR is mergeable once the CI pipeline passes and every CodeRabbit comment is resolved. No human review is required (below).

## Considered Options

* Keep the scratch scripts, patched.
* Rely on the ruleset alone.
* **A tested gate in the repository, `tools/mergegate`, as a second gate on top of the ruleset.**

## Decision Outcome

Chosen option: a tested gate in the repository, run as `python3 -m tools.mergegate --repo OWNER/NAME PR`. The ruleset is the first gate, enforced by GitHub. The tool is the second gate, and it also checks that the first is still in force.

The gate merges only when all of the following hold, read in this order. Any `gh` error, timeout, or empty or unreadable answer refuses (exit 2) and nothing merges.
1. The PR is open, not a draft, targets `main`, is mergeable, and has a full head SHA.
2. `main`'s ruleset still strictly requires every check in `tools/mergegate/required_checks.txt`, and requires resolved threads.
3. `main`'s own CI passed on `main`'s head.
4. Every check in `required_checks.txt` (the CI pipeline) passed on the PR's head commit, from the app the ruleset names. Any other check or status that failed or is pending, such as GitGuardian or CodeRabbit's own status, is printed as a `WARNING` and does not block. An unreadable check still refuses. Real secrets are still blocked by the required gitleaks job, `secret scan (full history)`.
5. CodeRabbit's latest review is of the head commit, and CodeRabbit has posted nothing for 30 minutes. Its replies have arrived up to 22 minutes after a reply.
6. Every review thread is settled by its last word, where only CodeRabbit's visible prose counts. Text inside code fences, collapsed sections, HTML comments and quotes is ignored, and an objection or a rate-limit notice keeps the thread open. Alternatively, a thread may be deferred explicitly (`--defer COMMENT=ISSUE`) to an open issue, on CodeRabbit's own last word, and the merge commit records the deferral.

If CodeRabbit's latest decisive review still requests changes once every thread verdict is clean or explicitly deferred, the gate posts `@coderabbitai resolve` itself. It never posts the command while any thread is open, because the command resolves every thread, objections included. It then polls for up to 15 minutes until CodeRabbit's latest decisive review no longer requests changes. Then it reads everything again: the head, the checks, CodeRabbit's review of the head and the threads, because CodeRabbit may have commented meanwhile. The quiet window is skipped on this second read, because CodeRabbit has just answered the command. If CodeRabbit does not approve within 15 minutes, or requests changes again, the gate refuses. A dry run only says that it would post the command.

Then the gate re-reads the head, resolves the settled threads, and merges with `gh pr merge --merge --match-head-commit <sha>`. It waits for `main`'s CI on the merge commit: exit 0 means merged and green; exit 3 means merged, but CI failed or could not be confirmed.

The gate never submits a review or dismisses one. The resolve command is the only comment it posts.

**The CodeRabbit status check is not required by the ruleset,** because it passes when rate-limited. Its review of the head is required by the gate.

**Owner decision O-1, 2026-11-14.** The owner decided: "mpairwe7 needs to be part of reviewers but do not make review on pr before merge. pass of ci pipeline and resolve of coderabbitai comments should make pr mergeable always". So:
* No human review or approval is required before a merge, and no merge decision is recorded on each PR. A PR is mergeable once the CI pipeline's required checks pass and every CodeRabbit comment is resolved.
* `mpairwe7` stays the code owner of every path in `.github/CODEOWNERS`, so the owner is attached to and notified of every PR. GitHub drops a review request to the PR's own author, so none is made.
* Code-owner review is not required (ruleset 27260742 has `require_code_owner_review: false`). `CODE_OWNER_REVIEW_ENFORCED = False` in the tool, and a docs test requires every document that mentions code-owner approval to say that it is not enforced, by this decision.
* Nobody submits a review as `mpairwe7`.

This supersedes ADR-0014 in part: the compensating control for "the amendment gate cannot protect its own code" is now the ruleset plus this gate.

### Consequences

* Good: a change merges only on evidence that was actually read. Every refusal names its reason, and a deferral is visible in the merge commit.
* Good: the gate is versioned and reviewed, and 37 tests run against a stub `gh`. Among them are the five fault cases that broke the old scripts, spoofing cases for the thread verdict, and the resolve command: posted once after clean verdicts, never with a thread open, never in a dry run.
* Good: a PR whose threads are all settled no longer waits for a person to post the resolve command.
* Bad: no human reads a change before it merges (V-03, accepted by the owner in O-1). The evidence is CI and CodeRabbit.
* Bad: a failing check outside the CI pipeline, such as GitGuardian, no longer stops a merge. It is a warning that a person may miss. Real secrets are still blocked by `secret scan (full history)`.
* Bad: the resolve command resolves every thread. The gate posts it only after its own verdicts are clean, and reads the threads again before it merges. A comment that CodeRabbit posts after that second read is not seen, but `--match-head-commit` still pins the code that was judged.
* Bad: merging is slow. Allow the 30-minute quiet window, plus CodeRabbit's hourly quota, plus up to 15 minutes for its approval.
* Bad: an unrecognised CodeRabbit wording means "open", so a person reads it. That conservatism is deliberate.
* Neutral: a new CI job must be added to `required_checks.txt` and to the ruleset in the same pull request. A test keeps the file in step with `.github/workflows/ci.yml`.

### Confirmation

* `python3 -m unittest governance.tests.test_mergegate` runs in the governance CI job.
* Each fault case, the head-pinned merge, the dry run that writes nothing, the spoofing cases, the warning for a check outside the pipeline, and the resolve command are tests. Mutation checks were applied by hand and caught: 8 of 8 in the merge stage, 12 of 12 for the thread verdict, 6 of 6 for the required checks, and 11 of 11 for the resolve command.
* A dry run against a live PR before each merge (`--dry-run`), described in [Merge a pull request](../how-to/merge-a-pull-request.md).

## More Information

* Ruleset 27260742, "main protection": deletion and non-fast-forward blocked, PRs required, review threads resolved, and 9 strict required checks.
* The how-to: [Merge a pull request](../how-to/merge-a-pull-request.md).
