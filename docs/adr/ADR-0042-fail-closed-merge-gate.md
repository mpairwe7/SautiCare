---
status: accepted
date: 2026-11-15
decision-makers: Sauti engineering (repository owner)
consulted: the November 2026 evaluation audit (V-02, V-03, owner decisions O-1 and O-2); ADR-0014; ADR-0041
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
* The owner decided O-2 on 2026-11-15: when CodeRabbit has not reviewed the head, because its quota is spent, the PR merges if GitHub reports it mergeable (below).

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
5. CodeRabbit's latest review is of the head commit, and CodeRabbit has posted nothing for 30 minutes. Its replies have arrived up to 22 minutes after a reply. If CodeRabbit has not reviewed the head, owner decision O-2 applies instead (below).
6. Every review thread is settled by its last word, where only CodeRabbit's visible prose counts. Text inside code fences, collapsed sections, HTML comments and quotes is ignored, and an objection or a rate-limit notice keeps the thread open. A reply settles a thread only when its whole visible text is one concession of the whole finding, such as `The finding is addressed.` A concession beside any other text keeps the thread open, because CodeRabbit often concedes one part and raises another in the same reply. Alternatively, a thread may be deferred explicitly (`--defer COMMENT=ISSUE`) to an open issue, when CodeRabbit's own last word says that no further change is needed in this PR and no sentence of it objects to the merge (one that opens with "However" or "But", or asks for a change before the merge or in this PR), and the merge commit records the deferral.

If CodeRabbit's latest decisive review still requests changes once every thread verdict is clean or explicitly deferred, the gate posts `@coderabbitai resolve` itself. It never posts the command while any thread is open, because the command resolves every thread, objections included. It then polls for up to 15 minutes until CodeRabbit's latest decisive review no longer requests changes. Then it reads everything again: the head, the checks, CodeRabbit's review of the head and the threads, because CodeRabbit may have commented meanwhile. The quiet window is skipped on this second read, because CodeRabbit has just answered the command. If CodeRabbit does not approve within 15 minutes, or requests changes again, the gate refuses. A dry run only says that it would post the command.

Then the gate re-reads the head and resolves the settled threads. GitHub resolves a thread by its ID alone, so the gate then reads every thread and CodeRabbit's latest decisive review again. A thread that is now open is reopened, and the gate refuses; so does a new request for changes, and so does a deferred or accepted thread whose last word is not the one the gate first read (an added reply, or an edit). Last, unless it posted the resolve command, it reads the quiet window again, because CodeRabbit may have written during the writes. Then it merges with `gh pr merge --merge --match-head-commit <sha>`. It waits for `main`'s CI on the merge commit: exit 0 means merged and green; exit 3 means merged, but CI failed or could not be confirmed. A merge command that fails or times out may still have merged, so GitHub's own state of the PR decides. If the PR is open, the gate refuses (exit 2). If it is merged, the gate waits for `main`'s CI as usual. If the state cannot be read, the gate exits 3 with `MAY HAVE MERGED`.

The gate never submits a review or dismisses one. The resolve command is the only comment it posts.

**The CodeRabbit status check is not required by the ruleset,** because it passes when rate-limited. Its review of the head is required by the gate, except under owner decision O-2.

**Owner decision O-1, 2026-11-14.** The owner decided: "mpairwe7 needs to be part of reviewers but do not make review on pr before merge. pass of ci pipeline and resolve of coderabbitai comments should make pr mergeable always". So:
* No human review or approval is required before a merge, and no merge decision is recorded on each PR. A PR is mergeable once the CI pipeline's required checks pass and every CodeRabbit comment is resolved.
* `mpairwe7` stays the code owner of every path in `.github/CODEOWNERS`, so the owner is attached to and notified of every PR. GitHub drops a review request to the PR's own author, so none is made.
* Code-owner review is not required (ruleset 27260742 has `require_code_owner_review: false`). `CODE_OWNER_REVIEW_ENFORCED = False` in the tool. A docs test requires each living document under `docs/` that mentions code-owner approval to say that it is not enforced, by this decision. The test does not scan `docs/adr/`, `docs/superpowers/` or the audits, which are records.
* Nobody submits a review as `mpairwe7`.

**Owner decision O-2, 2026-11-15.** The owner decided: "when no coderabbitai reviews /limits reached merge pr if mergeable". It sits beside O-1 and the standing rule "always resolve coderabbit comments on open pr before merge". So when CodeRabbit has not reviewed the head, the gate no longer refuses on that alone. CodeRabbit has not reviewed the head when it never reviewed the PR, or when its latest real review, or its corroborated walkthrough range, is of an older commit. The gate goes on only if all of the following hold:
1. GitHub reports the PR mergeable: its `mergeStateStatus` is `CLEAN` or `UNSTABLE` just before the merge. `UNSTABLE` means that only a check outside the CI pipeline failed or is pending; under O-1 such a check only warns, and GitHub itself allows the merge. Any other answer, or none, refuses with the reason. `BLOCKED` is allowed only until the gate's own writes clear it, because GitHub reports `BLOCKED` while CodeRabbit requests changes or a settled thread is unresolved. The gate then posts the resolve command once and waits for the approval, as above, resolves the settled threads, and reads the state again.
2. The head reached GitHub at least 30 minutes ago, so the gate never races a review that has just started. The time is the creation of the newest GitHub Actions check suite for the head commit on the PR's head branch, which GitHub creates on the push; the committer date is not used, because whoever commits sets it. A younger head refuses.
3. CodeRabbit has posted nothing for 30 minutes, as above.
4. Every review thread is settled by the same last-word rules. Existing CodeRabbit comments must still be resolved.
5. Every required check passed, as above.

The gate then prints `WARNING: CodeRabbit has not reviewed the head <sha> (never reviewed | latest review is of <sha>); merging under owner decision O-2 because GitHub reports the PR mergeable`, and the merge commit records the same line. When CodeRabbit has reviewed the head, nothing changes: the gate does not read the merge state or the head's age.

This supersedes ADR-0014 in part. Only the ruleset is enforced: GitHub requires the CI pipeline's checks and resolved review threads on every merge, by any path, and it counts a thread resolved by anyone. The gate's other checks run only when an operator runs it: CodeRabbit's review of the head, the quiet window, the thread verdicts, the deferrals and `main`'s CI. A merge through GitHub's web page or a plain `gh pr merge` skips them. So for "the amendment gate cannot protect its own code", the enforced control is the ruleset. The gate is the merge procedure, not an enforced control, for changes to its own code or to anything else.

### Consequences

* Good: a change merges only on evidence that was actually read. Every refusal names its reason, and a deferral is visible in the merge commit.
* Good: the gate is versioned and reviewed, and 58 tests check it, most of them against a stub `gh`. Among them are the five fault cases that broke the old scripts, spoofing cases for the thread verdict, and the resolve command: posted once after clean verdicts, never with a thread open, never in a dry run.
* Good: a PR whose threads are all settled no longer waits for a person to post the resolve command.
* Bad: no human reads a change before it merges (V-03, accepted by the owner in O-1). The evidence is CI and CodeRabbit.
* Bad: a head that CodeRabbit has not reviewed can merge when CodeRabbit's quota is exhausted (O-2). Then the evidence for that head is CI, GitHub's own mergeability and CodeRabbit's verdicts on earlier commits. The warning and the merge commit say so.
* Bad: a failing check outside the CI pipeline, such as GitGuardian, no longer stops a merge. It is a warning that a person may miss. Real secrets are still blocked by `secret scan (full history)`.
* Bad: the resolve command resolves every thread. The gate posts it only after its own verdicts are clean, and reads the threads again after it resolves them, just before it merges. A comment posted between that last read and the merge is not seen. A new thread is unresolved, so the ruleset refuses the merge, and `--match-head-commit` still pins the code that was judged.
* Bad: merging is slow. Allow the 30-minute quiet window, plus CodeRabbit's hourly quota, plus up to 15 minutes for its approval.
* Bad: an unrecognised CodeRabbit wording means "open", so a person reads it. That conservatism is deliberate.
* Neutral: a new CI job is added to `required_checks.txt` in its pull request, and a test keeps the file in step with `.github/workflows/ci.yml`. The ruleset is a GitHub setting outside the repository, so no pull request can change it. An administrator adds the job to the ruleset before that pull request merges, because the gate refuses while the ruleset does not require every name in the file it reads.

### Confirmation

* `python3 -m unittest governance.tests.test_mergegate` runs in the governance CI job.
* Each fault case, the head-pinned merge, the dry run that writes nothing, the spoofing cases, the warning for a check outside the pipeline, and the resolve command are tests. Mutation checks were applied by hand and caught: 8 of 8 in the merge stage, 12 of 12 for the thread verdict, 6 of 6 for the required checks, 11 of 11 for the resolve command, 11 of 11 for the fixes from the review of PR #20, 15 of 15 for owner decision O-2, and 12 of 12 for the fixes from its second review.
* A dry run against a live PR before each merge (`--dry-run`), described in [Merge a pull request](../how-to/merge-a-pull-request.md).

## More Information

* Ruleset 27260742, "main protection": deletion and non-fast-forward blocked, PRs required, review threads resolved, and 9 strict required checks.
* The how-to: [Merge a pull request](../how-to/merge-a-pull-request.md).
