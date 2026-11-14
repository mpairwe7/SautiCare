# How to merge a pull request

**Type:** How-to (Diátaxis) · **Decision:** [ADR-0042](../adr/ADR-0042-fail-closed-merge-gate.md) · **Tool:** `tools/mergegate`

Pull requests reach `main` only through the merge gate, on top of `main`'s ruleset. The gate refuses whenever it cannot read an answer, so a refusal is normal. Read it, deal with it, and run the gate again.

## Before you start

- `gh` is signed in with write access to the repository.
- The PR's CI has finished.
- You have answered every CodeRabbit comment *in its thread*. Never resolve a thread by hand to get past the gate.

## 1. Dry run

```bash
python3 -m tools.mergegate --repo mpairwe7/SautiCare <PR> --dry-run
```

The dry run reads everything and writes nothing. It prints one line per gate that passed, then either `would merge #<PR> at <sha>` or `mergegate: REFUSED: <reason>`.

## 2. Act on each refusal

| Refusal says | Do this |
|---|---|
| a check failed, is pending or is missing | wait for CI, or fix the cause and push |
| the ruleset no longer requires … | stop: someone weakened `main`'s protection. Tell the owner. |
| `main`'s CI is red | fix `main` first |
| CodeRabbit has not reviewed the head | wait for the review. On the free tier it may be rate-limited; post `@coderabbitai review` once the limit resets. |
| CodeRabbit was active inside the quiet window | wait 30 minutes after its last comment |
| a thread is open | read CodeRabbit's last word in that thread. Fix the code, or answer with your reasoning, in the thread. Then wait for its verdict. |
| CodeRabbit still requests changes, every thread verdict is clean | post `@coderabbitai resolve` yourself, wait for its approval, and re-run. The gate never posts it. |
| the head moved during the run | re-run |

**When CodeRabbit agrees that nothing more is needed in this PR but the remaining work is real** (for example, evidence that needs a staging environment):
1. open an issue for that work;
2. say so in the thread;
3. defer the thread explicitly:

```bash
python3 -m tools.mergegate --repo mpairwe7/SautiCare <PR> --dry-run --defer <COMMENT_ID>=<ISSUE>
```

`COMMENT_ID` is the number in the thread's `discussion_r…` link. The gate accepts a deferral only to an open issue, and only on CodeRabbit's own last word. The merge commit records it.

## 3. Merge

Until a second person can approve (owner decision O-1), the owner records the decision to merge in the PR. Then:

```bash
python3 -m tools.mergegate --repo mpairwe7/SautiCare <PR>
```

| Exit code | Meaning |
|---|---|
| 0 | merged, and `main`'s CI passed on the merge commit |
| 2 | refused; nothing merged |
| 3 | merged, but `main`'s CI on the merge commit failed or could not be confirmed. Treat `main` as broken until it is green. |

## Adding a CI job

A new required job goes into `tools/mergegate/required_checks.txt` and into the ruleset in the same pull request. `test_required_checks_match_the_ci_jobs` fails if the file and `.github/workflows/ci.yml` disagree.
