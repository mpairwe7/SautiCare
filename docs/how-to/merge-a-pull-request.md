# How to merge a pull request

**Type:** How-to (Diátaxis) · **Decision:** [ADR-0042](../adr/ADR-0042-fail-closed-merge-gate.md) · **Tool:** `tools/mergegate`

Merge every pull request into `main` through the merge gate. The gate is the merge procedure; `main`'s ruleset is what GitHub enforces. The ruleset requires the CI pipeline's checks and resolved review threads on every merge path, but a merge from GitHub's web page or a plain `gh pr merge` skips the gate's own checks: CodeRabbit's review of the head, the quiet window, the thread verdicts, the deferrals and `main`'s CI. A PR is mergeable once the CI pipeline passes and every CodeRabbit comment is resolved (owner decision O-1, 2026-11-14). No human review or approval is needed. When CodeRabbit has not reviewed the head, for example because its quota is spent, the PR merges if GitHub reports it mergeable (owner decision O-2, 2026-11-15). The gate refuses whenever it cannot read an answer, so a refusal is normal. Read it, deal with it, and run the gate again.

## Before you start

- `gh` is signed in with write access to the repository.
- The PR's CI has finished.
- You have answered every CodeRabbit comment *in its thread*. Never resolve a thread by hand to get past the gate.
- Do not post `@coderabbitai resolve` and do not submit a review as the owner. The gate posts the resolve command itself, and only once every thread verdict is clean.

## 1. Dry run

```bash
python3 -m tools.mergegate --repo mpairwe7/SautiCare <PR> --dry-run
```

The dry run reads everything and writes nothing. It prints one line per gate that passed, then either `would merge #<PR> at <sha>` or `mergegate: REFUSED: <reason>`.

- A `WARNING` line names a check outside the CI pipeline that failed or is pending, for example GitGuardian. It does not block. Read it anyway: real secrets are blocked by the required job `secret scan (full history)`.
- `WARNING: CodeRabbit has not reviewed the head <sha> (…); merging under owner decision O-2 because GitHub reports the PR mergeable` means that CodeRabbit never reviewed the PR, or reviewed only an older commit. The gate merges anyway because GitHub reports the PR `CLEAN` or `UNSTABLE` (only a check outside the CI pipeline failed or is pending), the head reached GitHub at least 30 minutes ago, CodeRabbit has been quiet for 30 minutes, every thread is settled and every required check passed. The merge commit records the same line. If you would rather have CodeRabbit's review of the head, post `@coderabbitai review` once its limit resets, and run the gate after it has reviewed.
- `GitHub reports the PR BLOCKED: under owner decision O-2 the gate would merge only if GitHub then reports it CLEAN or UNSTABLE …` means the same, except that GitHub reports `BLOCKED`. That is expected while CodeRabbit requests changes or a settled thread is unresolved, and the real run clears both before it reads the state again.
- `CodeRabbit still requests changes: would post …` means that every thread verdict is clean but CodeRabbit's latest decisive review still requests changes. The real run posts the command, waits up to 15 minutes for CodeRabbit's approval, checks the head, the checks and the threads again, and merges.

## 2. Act on each refusal

| Refusal says | Do this |
|---|---|
| a required check failed, is pending or is missing | wait for CI, or fix the cause and push |
| the ruleset no longer requires … | stop: someone weakened `main`'s protection. Tell the owner. |
| `main`'s CI is red | fix `main` first |
| CodeRabbit has not reviewed the head …, pushed … s ago; wait until CodeRabbit has had 30 minutes to review | wait, then re-run. The head reached GitHub less than 30 minutes ago, by the newest GitHub Actions check suite for it on the PR's branch, and CodeRabbit may be reviewing it. |
| CodeRabbit has not reviewed the head …, and GitHub reports the PR `<state>`, not CLEAN or UNSTABLE (owner decision O-2) | `BEHIND`: update the branch from `main` and wait for CI. `DIRTY`: resolve the conflicts. `BLOCKED`: something that the gate cannot clear blocks the merge; read the PR page. `UNKNOWN` or no state: re-run. Or wait for CodeRabbit's review of the head: post `@coderabbitai review` once its limit resets. |
| CodeRabbit was active inside the quiet window | wait 30 minutes after its last comment |
| a thread is open | read CodeRabbit's last word in that thread. Fix the code, or answer with your reasoning, in the thread. Then wait for its verdict. |
| CodeRabbit did not approve within 900 s of `@coderabbitai resolve` | read CodeRabbit's latest comments on the PR, answer them in their threads, and re-run |
| CodeRabbit requests changes again after `@coderabbitai resolve` | read its latest review, deal with it, and re-run after the quiet window |
| the head moved during the run | re-run |

**When CodeRabbit agrees that nothing more is needed in this PR but the remaining work is real** (for example, evidence that needs a staging environment):
1. open an issue for that work;
2. say so in the thread;
3. defer the thread explicitly:

```bash
python3 -m tools.mergegate --repo mpairwe7/SautiCare <PR> --dry-run --defer <COMMENT_ID>=<ISSUE>
```

`COMMENT_ID` is the number in the thread's `discussion_r…` link. The gate accepts a deferral only to an open issue, and only when CodeRabbit's own last word in the thread says that no further change is needed in this PR, for example `I do not see another code or plan change needed in this PR`. The merge commit records it.

**When CodeRabbit has resolved a thread itself but the gate still reads it as open.** The gate settles a reply automatically only when the whole reply is a concession. CodeRabbit often adds neutral detail ("the tests are planned; I did not run them") and ends with its own "Review thread resolved.". It also writes that line under a new defect, though, so the line settles nothing by itself. Read CodeRabbit's last reply:
- If it raises nothing new, or you have already fixed what it asked, accept the thread:

  ```bash
  python3 -m tools.mergegate --repo mpairwe7/SautiCare <PR> --dry-run --accept <COMMENT_ID>
  ```

  The gate accepts it only if CodeRabbit has the last word and that reply ends with its own resolution line. The merge commit records the acceptance with CodeRabbit's words.
- If it states a new defect, fix it and reply in the thread. Do not accept it.

## 3. Merge

Run the gate without `--dry-run`. Nobody approves the PR first (owner decision O-1):

```bash
python3 -m tools.mergegate --repo mpairwe7/SautiCare <PR>
```

If you deferred or accepted a thread in the dry run, give every `--defer` and `--accept` argument again. The dry run recorded nothing, so without them the gate refuses those threads:

```bash
python3 -m tools.mergegate --repo mpairwe7/SautiCare <PR> --defer <COMMENT_ID>=<ISSUE> --accept <COMMENT_ID>
```

| Exit code | Meaning |
|---|---|
| 0 | merged, and `main`'s CI passed on the merge commit |
| 2 | refused; nothing merged. After a failed merge command, this means that GitHub reports the PR still open. |
| 3 | merged, but `main`'s CI on the merge commit failed or could not be confirmed. Treat `main` as broken until it is green. `MAY HAVE MERGED` means that the merge command failed and the PR's state could not be read: check the PR on GitHub before you run the gate again. |

## Adding a CI job

1. Add the job to `.github/workflows/ci.yml` and its name to `tools/mergegate/required_checks.txt` in the same pull request. `test_required_checks_match_the_ci_jobs` fails if the two disagree.
2. The ruleset is a GitHub setting, not a file, so the pull request cannot change it. Once the job has passed on the pull request, ask an administrator to add it to ruleset 27260742 as a required check. Do this before the merge: the gate refuses while the ruleset does not require every name in `required_checks.txt`.
3. Run the gate. Other open pull requests then need the job too, and they get it when they update from `main`, as the ruleset's up-to-date rule already requires.
