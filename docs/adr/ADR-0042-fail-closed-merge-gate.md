---
status: accepted
date: 2026-11-14
decision-makers: Sauti engineering (repository owner)
consulted: the November 2026 evaluation audit (V-02, V-03); ADR-0014; ADR-0041
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
4. Every expected check passed on the PR's head commit, from the app the ruleset names, and no other check or status has failed or is pending.
5. CodeRabbit's latest review is of the head commit, and CodeRabbit has posted nothing for 30 minutes. Its replies have arrived up to 22 minutes after a reply.
6. Every review thread is settled by its last word, where only CodeRabbit's visible prose counts. Text inside code fences, collapsed sections, HTML comments and quotes is ignored, and an objection or a rate-limit notice keeps the thread open. Alternatively, a thread may be deferred explicitly (`--defer COMMENT=ISSUE`) to an open issue, on CodeRabbit's own last word, and the merge commit records the deferral.

Then the gate re-reads the head, resolves the settled threads, and merges with `gh pr merge --merge --match-head-commit <sha>`. It waits for `main`'s CI on the merge commit: exit 0 means merged and green; exit 3 means merged, but CI failed or could not be confirmed.

The gate never comments, reviews, dismisses a review or asks CodeRabbit for anything. If CodeRabbit still requests changes once every thread verdict is clean, the gate says that `@coderabbitai resolve` is now permitted, and a person posts it.

**The CodeRabbit status check is not required by the ruleset,** because it passes when rate-limited. Its review of the head is required by the gate.

**Until a second human can approve** (owner decision O-1 in the audit), the owner records the decision to merge each PR. Code-owner review stays the owner's decision. `CODE_OWNER_REVIEW_ENFORCED = False` in the tool, and a docs test forbids any document from claiming code-owner approval is enforced while it is not.

This supersedes ADR-0014 in part: the compensating control for "the amendment gate cannot protect its own code" is now the ruleset plus this gate.

### Consequences

* Good: a change merges only on evidence that was actually read. Every refusal names its reason, and a deferral is visible in the merge commit.
* Good: the gate is versioned and reviewed, and 33 tests run against a stub `gh`. Among them are the five fault cases that broke the old scripts, and spoofing cases for the thread verdict.
* Bad: merging is slow. Allow the 30-minute quiet window, plus CodeRabbit's hourly quota.
* Bad: an unrecognised CodeRabbit wording means "open", so a person reads it. That conservatism is deliberate.
* Neutral: a new CI job must be added to `required_checks.txt` and to the ruleset in the same pull request. A test keeps the file in step with `.github/workflows/ci.yml`.

### Confirmation

* `python3 -m unittest governance.tests.test_mergegate` runs in the governance CI job.
* Each fault case, the head-pinned merge, the dry run that writes nothing, and the spoofing cases are tests. Mutation checks were applied by hand and caught: 8 of 8 in the merge stage, and 12 of 12 for the thread verdict.
* A dry run against a live PR before each merge (`--dry-run`), described in [Merge a pull request](../how-to/merge-a-pull-request.md).

## More Information

* Ruleset 27260742, "main protection": deletion and non-fast-forward blocked, PRs required, review threads resolved, and 9 strict required checks.
* The how-to: [Merge a pull request](../how-to/merge-a-pull-request.md).
