"""tools/mergegate against a stub gh (ADR-0042; evaluation audit V-02, EC-2).

Each test starts from a clean scenario, in which every answer allows the
merge, and overrides one answer. The gate runs as `python3 -m
tools.mergegate --repo o/r 9999`, exactly as an operator runs it.
"""
import json
import os
import re
import shlex
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import yaml

from governance.tests.helpers import ROOT
from tools import mergegate

STUB = ROOT / "governance" / "tests" / "stub_gh.py"
HEAD = "a" * 40
MAIN = "d" * 40
MERGED = "b" * 40
NOW = "2026-11-14T12:00:00Z"
ACTIONS = 15368
EXPECTED = [line.strip() for line in (ROOT / "tools" / "mergegate" / "required_checks.txt")
            .read_text(encoding="utf-8").splitlines() if line.strip() and not line.startswith("#")]


def ok(value, **extra):
    return {"stdout": json.dumps(value), **extra}


def pr(head=HEAD, **over):
    v = {"number": 9999, "state": "OPEN", "isDraft": False, "baseRefName": "main",
         "headRefOid": head, "mergeable": "MERGEABLE", "title": "T", "author": {"login": "dev"}}
    v.update(over)
    return v


def ruleset(contexts=None, thread_resolution=True, strict=True):
    contexts = EXPECTED if contexts is None else contexts
    return [[
        {"type": "deletion", "ruleset_id": 1},
        {"type": "pull_request", "ruleset_id": 1,
         "parameters": {"required_approving_review_count": 0,
                        "required_review_thread_resolution": thread_resolution}},
        {"type": "required_status_checks", "ruleset_id": 1,
         "parameters": {"strict_required_status_checks_policy": strict,
                        "required_status_checks": [{"context": c, "integration_id": ACTIONS}
                                                   for c in contexts]}},
    ]]


def run(name, status="completed", conclusion="success", app=ACTIONS):
    return {"name": name, "status": status, "conclusion": conclusion, "app": {"id": app}}


def check_runs(runs=None):
    runs = [run(n) for n in EXPECTED] if runs is None else runs
    return [{"total_count": len(runs), "check_runs": runs}]


def statuses(items=()):
    return [{"state": "success", "total_count": len(items), "statuses": list(items)}]


CR, CR_REST = "coderabbitai", "coderabbitai[bot]"
MARK = "\n\n✅ Addressed in commits abc1234 to def5678\n"
FINDING = "**Potential issue.** The failed-Dial path is not escalated. Is that intended?"


REVIEW_BODY = "<!-- coderabbit-ai-generated:v1 -->\n\n**Actionable comments posted: 1**"


def review(commit=HEAD, state="COMMENTED", at="2026-11-14T10:00:00Z", user=CR_REST, body=REVIEW_BODY):
    """A review; CodeRabbit's thread replies and command approvals are reviews with an empty body."""
    return {"user": {"login": user}, "commit_id": commit, "state": state, "submitted_at": at, "body": body}


def requested(*later):
    """CodeRabbit requested changes on an earlier commit, then reviewed the head; then `later`."""
    return [review(state="CHANGES_REQUESTED", at="2026-11-14T09:00:00Z"), review(), *later]


# CodeRabbit's answer to the resolve command, inside what was the quiet window.
APPROVED = review(state="APPROVED", at="2026-11-14T12:00:30Z", body="")
RESOLVE_POST = ["pr", "comment", "9999", "--repo", "o/r", "--body", "@coderabbitai resolve"]


def activity(created="2026-11-14T09:30:00Z", updated="2026-11-14T10:30:00Z", user=CR_REST):
    return {"user": {"login": user}, "created_at": created, "updated_at": updated}


def actor(login, kind=None):
    return {"login": login, "__typename": kind or ("Bot" if login == CR else "User")} if login else None


def c(author, body, at, edited=None, editor=None, n=0, kind=None, editor_kind=None):
    """A thread comment as GraphQL returns it; kind overrides the author's
    __typename (a user account could be named like the bot)."""
    return {"author": actor(author, kind), "editor": actor(editor, editor_kind),
            "body": body, "url": f"https://github.com/o/r/pull/9999#discussion_r{n}",
            "createdAt": at, "lastEditedAt": edited}


def thread(tid, *comments, resolved=False, more=False):
    return {"id": tid, "isResolved": resolved,
            "comments": {"pageInfo": {"hasNextPage": more}, "nodes": list(comments)}}


def threads_page(*threads, cursor=None):
    return {"data": {"repository": {"pullRequest": {"reviewThreads": {
        "pageInfo": {"hasNextPage": cursor is not None, "endCursor": cursor}, "nodes": list(threads)}}}}}


# A CodeRabbit finding that CodeRabbit itself marked addressed after the fix.
SETTLED = thread("PRRT_1", c(CR, FINDING + MARK, "2026-11-14T09:30:00Z", "2026-11-14T10:30:00Z", CR, n=101))
# CodeRabbit says nothing more is needed in this PR; the rest belongs in an issue.
DEFERRABLE = thread("PRRT_2", c(CR, FINDING, "2026-11-14T09:30:00Z", n=201),
                    c("dev", "The staging drill cannot run before staging exists.", "2026-11-14T09:40:00Z"),
                    c(CR, "<details>analysis</details>`@dev` I do not see another code or plan change needed "
                          "in this PR. The staging drill remains a go-live gate.", "2026-11-14T09:50:00Z"))


def resolved_answer(name, state=True):
    return ok({"data": {name: {"thread": {"isResolved": state}}}})


def clean():
    """Every answer allows the merge. Keys name the rule a test overrides."""
    return {
        "pr-merged": {"argv": ["pr", "view", "mergeCommit"],
                      **ok({"state": "MERGED", "mergeCommit": {"oid": MERGED}})},
        "pr": {"argv": ["pr", "view", "9999"], **ok(pr())},
        "ruleset": {"argv": ["api", "rules/branches/main"], **ok(ruleset())},
        "main": {"argv": ["api", "repos/o/r/branches/main"], **ok({"name": "main", "commit": {"sha": MAIN}})},
        "main-run": {"argv": ["run", "list", "--commit", MAIN],
                     **ok([{"databaseId": 1, "headSha": MAIN, "status": "completed", "conclusion": "success"}])},
        "merge-run": {"argv": ["run", "list", "--commit", MERGED],
                      **ok([{"databaseId": 2, "headSha": MERGED, "status": "completed", "conclusion": "success"}])},
        "check-runs": {"argv": ["api", f"commits/{HEAD}/check-runs"], **ok(check_runs())},
        "statuses": {"argv": ["api", f"commits/{HEAD}/status?"], **ok(statuses())},
        "reviews": {"argv": ["api", "pulls/9999/reviews"], **ok([[review()]])},
        "inline": {"argv": ["api", "pulls/9999/comments"], **ok([[activity()]])},
        "issue-comments": {"argv": ["api", "issues/9999/comments"],
                           **ok([[activity("2026-11-14T09:00:00Z", "2026-11-14T10:05:00Z")]])},
        "unresolve": {"argv": ["api", "graphql", "unresolveReviewThread("],
                      **resolved_answer("unresolveReviewThread", False)},
        "resolve": {"argv": ["api", "graphql", " resolveReviewThread("], **resolved_answer("resolveReviewThread")},
        "threads": {"argv": ["api", "graphql", "reviewThreads"], **ok(threads_page(SETTLED))},
        "issue": {"argv": ["issue", "view", "16"], **ok({"number": 16, "state": "OPEN"})},
        "merge": {"argv": ["pr", "merge", "--match-head-commit"], "stdout": ""},
        "comment": {"argv": ["pr", "comment", "9999"], "stdout": "https://github.com/o/r/pull/9999#issuecomment-1\n"},
    }


class Gate:
    """One run of the gate against a scenario, with the calls it made."""

    def __init__(self, rules, *args, env=None):
        with tempfile.TemporaryDirectory() as tmp:
            scen = Path(tmp) / "scenario.json"
            log = Path(tmp) / "calls.jsonl"
            scen.write_text(json.dumps(list(rules.values()) if isinstance(rules, dict) else rules))
            e = dict(os.environ, MERGEGATE_GH=f"{shlex.quote(sys.executable)} {shlex.quote(str(STUB))}",
                     MERGEGATE_NOW=NOW, MERGEGATE_POLL="0", MERGEGATE_APPROVAL_WAIT="0", STUB_GH_SCENARIO=str(scen), STUB_GH_LOG=str(log))
            e.update(env or {})
            p = subprocess.run([sys.executable, "-m", "tools.mergegate", "--repo", "o/r", "9999", *args],
                               cwd=ROOT, env=e, capture_output=True, text=True, timeout=30)
            self.code, self.out, self.err = p.returncode, p.stdout, p.stderr
            self.calls = [json.loads(line)["argv"] for line in log.read_text().splitlines()] if log.exists() else []

    def merges(self):
        return [c for c in self.calls if c[:2] == ["pr", "merge"]]

    def posts(self):
        return [c for c in self.calls if c[:2] == ["pr", "comment"]]

    def mutations(self, name):
        return [c for c in self.calls if c[:2] == ["api", "graphql"] and any(f"{name}(" in a for a in c)
                and not (name == "resolveReviewThread" and any("unresolveReviewThread(" in a for a in c))]


def forbidden(call):
    """A call that reviews, dismisses a review, bypasses the rules, writes
    through the REST API, or comments anything but the resolve command."""
    return call != RESOLVE_POST and (call[:2] in (["pr", "comment"], ["pr", "review"], ["issue", "comment"])
            or any(a in ("--admin", "--auto", "-X", "--method") or "dismissals" in a or "@coderabbitai" in a
                   for a in call))


class MergeGateTest(unittest.TestCase):
    def gate(self, overrides=None, *args, env=None):
        rules = clean()
        rules.update(overrides or {})
        g = Gate(rules, *args, env=env)
        self.assertEqual([c for c in g.calls if forbidden(c)], [], "the gate never reviews or dismisses a review")
        self.assertLessEqual(len(g.posts()), 1, "the resolve command is posted at most once")
        if "--dry-run" in args:
            self.assertEqual(g.posts(), [], "a dry run posts nothing")
        return g

    def assertRefused(self, g, reason=""):
        self.assertEqual(g.code, 2, g.err + g.out)
        self.assertEqual(g.merges(), [], "a refused run must not merge")
        self.assertIn("mergegate: REFUSED:", g.err)
        self.assertIn(reason, g.err)

    # Task 1.1: the PR, the ruleset, main and the checks.

    def test_a_clean_pr_merges_with_the_head_pinned(self):
        g = self.gate()
        self.assertEqual(g.code, 0, g.err + g.out)
        merges = g.merges()
        self.assertEqual(len(merges), 1)
        self.assertEqual(merges[0][:10], ["pr", "merge", "9999", "--repo", "o/r", "--merge",
                                          "--match-head-commit", HEAD, "--subject", "Merge PR #9999: T"])

    def test_an_unreadable_pr_refuses(self):
        g = self.gate({"pr": {"argv": ["pr", "view"], "exit": 1, "stderr": "HTTP 502: Bad Gateway"}})
        self.assertRefused(g, "HTTP 502")

    def test_an_unreadable_answer_refuses(self):
        cases = {
            "empty": ({"stdout": ""}, "empty answer"),
            "truncated": ({"stdout": json.dumps(pr())[:40]}, "unreadable answer"),
            "empty head": (ok(pr(head="")), "the head commit is unreadable"),
            "unknown mergeability": (ok(pr(mergeable="UNKNOWN")), "re-run"),
            "conflicting": (ok(pr(mergeable="CONFLICTING")), "not mergeable"),
            "a draft": (ok(pr(isDraft=True)), "draft"),
            "another base": (ok(pr(baseRefName="ev-0-audit")), "targets ev-0-audit"),
            "closed": (ok(pr(state="CLOSED")), "not open"),
            "a timeout": ({"sleep": 5, **ok(pr())}, "timed out"),
        }
        for name, (answer, reason) in cases.items():
            with self.subTest(name):
                g = self.gate({"pr": {"argv": ["pr", "view"], **answer}}, env={"MERGEGATE_TIMEOUT": "1"})
                self.assertRefused(g, reason)

    def test_an_unreadable_check_list_refuses(self):
        g = self.gate({"check-runs": {"argv": ["api", "check-runs"], "exit": 1,
                                      "stderr": "no checks reported on the 'probe' branch"}})
        self.assertRefused(g, "no checks reported")

    def test_a_truncated_check_list_refuses(self):
        page = check_runs()
        page[0]["total_count"] += 1
        self.assertRefused(self.gate({"check-runs": {"argv": ["api", "check-runs"], **ok(page)}}))

    def test_a_missing_required_check_refuses(self):
        runs = [run(n) for n in EXPECTED if n != "governance and living docs"]
        g = self.gate({"check-runs": {"argv": ["api", "check-runs"], **ok(check_runs(runs))}})
        self.assertRefused(g, "governance and living docs")

    def test_a_failing_or_pending_required_check_refuses(self):
        def with_(extra, drop=None):
            return check_runs([run(n) for n in EXPECTED if n != drop] + extra)
        cases = {
            "an expected check failed": (with_([run("unit tests (race)", conclusion="failure")], "unit tests (race)"),
                                         "unit tests (race): failure"),
            "an expected check is pending": (with_([run("fuzz smoke", "in_progress", None)], "fuzz smoke"),
                                             "fuzz smoke: in_progress"),
            "an expected check was skipped": (with_([run("fuzz smoke", conclusion="skipped")], "fuzz smoke"),
                                              "fuzz smoke: skipped"),
            "an expected name from another app": (with_([run("unit tests (race)", app=999)], "unit tests (race)"),
                                                  "app 999"),
            "an unreadable check outside the list": (with_([{"name": "GitGuardian Security Checks",
                                                             "status": "completed", "conclusion": "failure"}]),
                                                     "`app` is missing"),
        }
        for name, (page, reason) in cases.items():
            with self.subTest(name):
                self.assertRefused(self.gate({"check-runs": {"argv": ["api", "check-runs"], **ok(page)}}), reason)
        with self.subTest("an expected name as a commit status"):
            bad = statuses([{"context": "fuzz smoke", "state": "success"}])
            self.assertRefused(self.gate({"statuses": {"argv": ["api", "/status?"], **ok(bad)}}), "fuzz smoke")

    def test_a_failing_check_outside_the_required_list_only_warns(self):
        """O-1: only the CI pipeline's required checks block. Any other check
        or status that failed or is pending is a printed warning."""
        cases = {
            "GitGuardian failed": run("GitGuardian Security Checks", conclusion="failure", app=46505),
            "CodeRabbit is reviewing": run("CodeRabbit", "in_progress", None, app=347564),
        }
        for name, extra in cases.items():
            with self.subTest(name):
                page = check_runs([run(n) for n in EXPECTED] + [extra])
                g = self.gate({"check-runs": {"argv": ["api", "check-runs"], **ok(page)}})
                self.assertEqual(g.code, 0, g.err + g.out)
                self.assertEqual(len(g.merges()), 1)
                self.assertIn(f"WARNING: {extra['name']}: {extra['conclusion'] or extra['status']}", g.out)
        with self.subTest("a failing commit status"):
            bad = statuses([{"context": "ci/other", "state": "failure"}])
            g = self.gate({"statuses": {"argv": ["api", "/status?"], **ok(bad)}})
            self.assertEqual(g.code, 0, g.err + g.out)
            self.assertEqual(len(g.merges()), 1)
            self.assertIn("WARNING: ci/other: failure", g.out)

    def test_a_skipped_check_outside_the_expected_list_is_allowed(self):
        page = check_runs([run(n) for n in EXPECTED] + [run("optional", conclusion="skipped", app=1)])
        self.assertEqual(self.gate({"check-runs": {"argv": ["api", "check-runs"], **ok(page)}}).code, 0)

    def test_a_weakened_ruleset_refuses(self):
        cases = {
            "no rules": [[]],
            "unit tests (race) not required": ruleset([c for c in EXPECTED if c != "unit tests (race)"]),
            "threads need not be resolved": ruleset(thread_resolution=False),
            "checks need not be up to date": ruleset(strict=False),
        }
        for name, body in cases.items():
            with self.subTest(name):
                g = self.gate({"ruleset": {"argv": ["api", "rules/branches/main"], **ok(body)}})
                self.assertRefused(g, "main protection is missing or weaker than expected")

    def test_red_main_refuses(self):
        cases = {
            "failure": [{"databaseId": 1, "headSha": MAIN, "status": "completed", "conclusion": "failure"}],
            "still running": [{"databaseId": 1, "headSha": MAIN, "status": "in_progress", "conclusion": ""}],
            "an older commit": [{"databaseId": 1, "headSha": "c" * 40, "status": "completed",
                                 "conclusion": "success"}],
            "no run": [],
        }
        for name, runs in cases.items():
            with self.subTest(name):
                self.assertRefused(self.gate({"main-run": {"argv": ["run", "list", "--commit", MAIN], **ok(runs)}}),
                                   "main")

    def test_required_checks_match_the_ci_jobs(self):
        ci = yaml.safe_load((ROOT / ".github" / "workflows" / "ci.yml").read_text(encoding="utf-8"))
        jobs = [job["name"] for job in ci["jobs"].values()]
        self.assertEqual(len(EXPECTED), len(set(EXPECTED)), "duplicate names in required_checks.txt")
        self.assertEqual(set(EXPECTED), set(jobs))

    # Task 1.2: CodeRabbit's review of the head, the quiet window, the threads.

    def reviews(self, *rs):
        return {"reviews": {"argv": ["api", "pulls/9999/reviews"], **ok([list(rs)])}}

    def threads(self, *ts):
        return {"threads": {"argv": ["api", "graphql", "reviewThreads"], **ok(threads_page(*ts))}}

    def test_a_head_without_a_coderabbit_review_refuses(self):
        cases = {
            "no CodeRabbit review": [review(user="dev")],
            "only an older commit reviewed (rate-limited push)": [review(commit="c" * 40)],
            "the latest review is of an older commit": [review(), review(commit="c" * 40, at="2026-11-14T10:10:00Z")],
            "a thread reply after the push is not a review of the head": [
                review(commit="c" * 40), review(at="2026-11-14T10:10:00Z", body="")],
            "an approval by command is not a review of the head": [
                review(commit="c" * 40), review(state="APPROVED", at="2026-11-14T10:10:00Z", body="")],
        }
        for name, rs in cases.items():
            with self.subTest(name):
                self.assertRefused(self.gate(self.reviews(*rs)), "CodeRabbit")

    def test_an_unreadable_review_list_refuses(self):
        self.assertRefused(self.gate({"reviews": {"argv": ["api", "pulls/9999/reviews"], "exit": 1,
                                                  "stderr": "HTTP 502: Bad Gateway"}}), "HTTP 502")

    def reviews_in_turn(self, *answers):
        """Each answer to one read of CodeRabbit's reviews, in turn; the last one from then on."""
        argv = ["api", "pulls/9999/reviews"]
        rules = {"reviews": {"argv": argv, "times": 1, **ok([answers[0]])}}
        for i, answer in enumerate(answers[1:-1]):
            rules[f"reviews-{i}"] = {"argv": argv, "times": 1, **ok([answer])}
        return {**rules, "reviews-last": {"argv": argv, **ok([answers[-1]])}}

    def test_clean_verdicts_and_changes_requested_post_the_resolve_command_then_merge(self):
        """O-1: once every thread verdict is clean, the gate posts the resolve
        command itself, once, waits for CodeRabbit's approval, verifies the
        head, the checks and the threads again, and merges."""
        flow = self.reviews_in_turn(requested(), requested(), requested(), requested(APPROVED))
        wait = {"MERGEGATE_APPROVAL_WAIT": "20"}
        g = self.gate(flow, env=wait)
        self.assertEqual(g.code, 0, g.err + g.out)
        self.assertEqual(g.posts(), [RESOLVE_POST])
        self.assertLess(g.calls.index(RESOLVE_POST), g.calls.index(g.merges()[0]))
        with self.subTest("an explicitly deferred thread counts as clean"):
            g = self.gate({**flow, **self.threads(SETTLED, DEFERRABLE)}, "--defer", "201=16", env=wait)
            self.assertEqual(g.code, 0, g.err + g.out)
            self.assertEqual(g.posts(), [RESOLVE_POST])
        opened = thread("PRRT_8", c(CR, FINDING, "2026-11-14T12:00:20Z", n=801))
        fuzz_failed = [run(n, conclusion="failure" if n == "fuzz smoke" else "success") for n in EXPECTED]
        later = {
            "CodeRabbit opens a thread meanwhile": ({
                **flow, "threads": {"argv": ["api", "graphql", "reviewThreads"], "times": 1, **ok(threads_page(SETTLED))},
                "threads-later": {"argv": ["api", "graphql", "reviewThreads"], **ok(threads_page(SETTLED, opened))}},
                "discussion_r801"),
            "a required check fails on a re-run": ({
                **flow, "check-runs": {"argv": ["api", "check-runs"], "times": 1, **ok(check_runs())},
                "check-runs-later": {"argv": ["api", "check-runs"], **ok(check_runs(fuzz_failed))}},
                "fuzz smoke: failure"),
            "the head moves": ({
                **flow, "pr": {"argv": ["pr", "view", "9999"], "times": 2, **ok(pr())},
                "pr-later": {"argv": ["pr", "view", "9999"], **ok(pr(head="e" * 40))}},
                "the head moved during the run"),
            "CodeRabbit requests changes again": (self.reviews_in_turn(
                requested(), requested(APPROVED),
                requested(APPROVED, review(state="CHANGES_REQUESTED", at="2026-11-14T12:01:00Z"))),
                "requests changes again"),
        }
        for name, (rules, reason) in later.items():
            with self.subTest(name):
                g = self.gate(rules, env=wait)
                self.assertRefused(g, reason)
                self.assertEqual(g.posts(), [RESOLVE_POST])

    def test_an_open_thread_never_gets_the_resolve_command(self):
        """The resolve command resolves every thread, objections included, so
        the gate never posts it while any thread is open."""
        objection = thread("PRRT_5", c(CR, FINDING, "2026-11-14T09:30:00Z", n=501),
                           c(CR, "I am not marking this as addressed.", "2026-11-14T10:00:00Z"))
        second_page = {"threads": {"argv": ["api", "graphql", "reviewThreads"], "times": 1,
                                   **ok(threads_page(SETTLED, cursor="CUR1"))},
                       "threads-2": {"argv": ["api", "graphql", "reviewThreads", "after=CUR1"],
                                     **ok(threads_page(objection))}}
        cases = {
            "an objection": (self.threads(SETTLED, objection), "discussion_r501"),
            "an objection on the second page": (second_page, "discussion_r501"),
            "a deferrable thread, not deferred": (self.threads(SETTLED, DEFERRABLE), "discussion_r201"),
        }
        for name, (over, reason) in cases.items():
            with self.subTest(name):
                g = self.gate({**self.reviews(*requested()), **over}, env={"MERGEGATE_APPROVAL_WAIT": "20"})
                self.assertRefused(g, reason)
                self.assertEqual(g.posts(), [])

    def test_no_approval_within_the_bound_refuses(self):
        g = self.gate(self.reviews(*requested()))
        self.assertRefused(g, "did not approve")
        self.assertEqual(g.posts(), [RESOLVE_POST])
        self.assertEqual(g.mutations("resolveReviewThread"), [], "nothing is resolved by the gate after a refusal")

    def test_changes_requested_without_the_resolve_command(self):
        with self.subTest("an approval after the request merges"):
            g = self.gate(self.reviews(review(state="CHANGES_REQUESTED", at="2026-11-14T09:00:00Z"),
                                       review(state="APPROVED", body="")))
            self.assertEqual(g.code, 0, g.err)
        with self.subTest("a dismissed request does not count"):
            g = self.gate(self.reviews(review(state="DISMISSED", at="2026-11-14T09:00:00Z"), review()))
            self.assertEqual(g.code, 0, g.err)

    def test_activity_inside_the_quiet_window_refuses(self):
        cases = {
            "an inline comment created 4 minutes ago": {"inline": {"argv": ["api", "pulls/9999/comments"],
                                                                   **ok([[activity("2026-11-14T11:56:00Z",
                                                                                   "2026-11-14T11:56:00Z")]])}},
            "an issue comment updated 1 minute ago": {"issue-comments": {"argv": ["api", "issues/9999/comments"],
                                                                         **ok([[activity(updated="2026-11-14T11:59:00Z")]])}},
            "a review submitted 29 minutes ago": self.reviews(review(at="2026-11-14T11:31:00Z")),
        }
        for name, over in cases.items():
            with self.subTest(name):
                self.assertRefused(self.gate(over), "quiet")
        with self.subTest("someone else's activity does not count"):
            g = self.gate({"inline": {"argv": ["api", "pulls/9999/comments"],
                                      **ok([[activity(), activity(updated="2026-11-14T11:59:00Z", user="dev")]])}})
            self.assertEqual(g.code, 0, g.err)

    def test_a_thread_query_error_refuses(self):
        cases = {
            "rate limited": {"exit": 1, "stderr": "HTTP 403: API rate limit exceeded"},
            "errors in the body": ok({"data": None, "errors": [{"message": "Something went wrong"}]}),
            "no pull request": ok({"data": {"repository": {"pullRequest": None}}}),
        }
        for name, answer in cases.items():
            with self.subTest(name):
                self.assertRefused(self.gate({"threads": {"argv": ["api", "graphql", "reviewThreads"], **answer}}))

    def test_a_thread_with_a_second_page_of_comments_refuses(self):
        g = self.gate(self.threads(thread("PRRT_9", c(CR, FINDING + MARK, "2026-11-14T09:30:00Z"), more=True)))
        self.assertRefused(g, "more than 100 comments")

    def test_every_page_of_threads_is_judged(self):
        open_ = thread("PRRT_7", c(CR, FINDING, "2026-11-14T09:30:00Z", n=701))
        g = self.gate({"threads-2": {"argv": ["api", "graphql", "reviewThreads", "after=CUR1"],
                                     **ok(threads_page(open_))},
                       "threads": {"argv": ["api", "graphql", "reviewThreads"], "times": 1,
                                   **ok(threads_page(SETTLED, cursor="CUR1"))}})
        self.assertRefused(g, "discussion_r701")

    def test_a_repeated_page_cursor_refuses(self):
        g = self.gate({"threads": {
            "argv": ["api", "graphql", "reviewThreads"], **ok(threads_page(SETTLED, cursor="CUR1"))}})
        self.assertRefused(g, "cursor")

    def test_judge_thread(self):
        t0, t1, t2, t3 = ("2026-11-14T09:00:00Z", "2026-11-14T09:10:00Z", "2026-11-14T09:20:00Z",
                          "2026-11-14T09:30:00Z")
        finding = c(CR, FINDING, t0)
        table = [
            ("one CodeRabbit comment, unresolved", thread("T", finding), False),
            ("CodeRabbit, then the author: CodeRabbit has not answered",
             thread("T", finding, c("dev", "Fixed in abc1234.", t1)), False),
            ("a polite objection: looks good, but ... remain unverified",
             thread("T", finding, c("dev", "Fixed.", t1),
                    c(CR, "This looks good, but the failed-Dial path and the busy branch remain unverified.", t2)),
             False),
            ("addressed; however ...",
             thread("T", finding, c(CR, "The coordinator fallback is addressed; however the coordinator dial is "
                                        "not escalated when every attempt fails.", t1)), False),
            ("a stranger's ok", thread("T", finding, c("someone-else", "ok", t1)), False),
            ("resolved by CodeRabbit is not a verdict (the resolve command resolves every thread)",
             thread("T", finding, resolved=True), False),
            ("resolved by the author over an objection",
             thread("T", finding, c(CR, "I am not marking this addressed.", t1), resolved=True), False),
            ("a rate-limit notice", thread("T", finding, c("dev", "Fixed.", t1),
                                           c(CR, "### Rate Limit Exceeded\n`@dev` please wait 45 minutes.", t2)),
             False),
            ("CodeRabbit last: Addressed in commit", thread("T", finding, c(CR, "✅ Addressed in commit abc1234", t1)),
             True),
            ("its finding marked addressed after the author's reply",
             thread("T", c(CR, FINDING + MARK, t0, t2, CR), c("dev", "Fixed in abc1234.", t1)), True),
            ("its finding confirmed as addressed",
             thread("T", c(CR, FINDING + "\n\n✅ Confirmed as addressed by @dev\n", t0, t2, CR),
                    c("dev", "Fixed.", t1)), True),
            ("marked addressed, then a reply objects",
             thread("T", c(CR, FINDING + MARK, t0, t1, CR), c("dev", "Done.", t1),
                    c(CR, "One race remains in the timer path.", t3)), False),
            ("the mark was written by someone else",
             thread("T", c(CR, FINDING + MARK, t0, t2, "dev"), c("dev", "Fixed.", t1)), False),
            ("a reply edited by someone else",
             thread("T", finding, c(CR, "✅ Addressed in commit abc1234", t1, t2, "dev")), False),
            ("withdrawn", thread("T", finding, c("dev", "This path is not gated.", t1),
                                 c(CR, "<!-- <review_comment_withdrawn> -->\nThanks, I withdraw the finding.", t2)),
             True),
            ("objection words only inside its collapsed analysis",
             thread("T", finding, c(CR, "<details><summary>Analysis</summary><details>not? still</details>"
                                        "missing</details>\nThe finding is addressed.", t1)), True),
            ("the mark and an author reply in the same second",
             thread("T", c(CR, FINDING + MARK, t0, t2, CR), c("dev", "One more question.", t2)), False),
            ("a deleted account's comment", thread("T", finding, c(None, "ok", t1)), False),
        ]
        for name, t, settled in table:
            with self.subTest(name):
                self.assertEqual(mergegate.judge_thread(t), settled)

    def test_a_concession_counts_only_as_codeRabbits_visible_word(self):
        """A mark or marker inside a fence, collapsed section, HTML comment
        or quote, mid-prose, unedited, or from another account, is not a
        concession: the thread is open, and a person reads it."""
        t0, t1, t2 = "2026-11-14T09:00:00Z", "2026-11-14T09:10:00Z", "2026-11-14T09:20:00Z"
        tag = "✅ Addressed in commit abc1234"
        withdrawn = "<!-- <review_comment_withdrawn> -->"

        def marked(body, **kw):
            return thread("T", c(CR, body, t0, kw.pop("edited", t2), kw.pop("editor", CR), **kw),
                          c("dev", "Fixed.", t1))

        def replied(body, **kw):
            return thread("T", c(CR, FINDING, t0), c("dev", "Fixed.", t1), c(CR, body, t2, **kw))

        real_finding = (FINDING + "\n\n```suggestion\nescalate()\n```\n\n<details>\n<summary>Prompt for AI Agents"
                        "</summary>\n\n```\nVerify each finding.\n```\n\n</details>\n\n<!-- This is an "
                        "auto-generated reply by CodeRabbit -->\n\n")
        open_ = {
            "mark inside a collapsed section": marked(FINDING + f"\n<details><summary>Prompt</summary>\n{tag}\n</details>\n"),
            "mark inside a code fence": marked(FINDING + f"\n```\n{tag}\n```\n"),
            "mark inside an unclosed code fence": marked(FINDING + f"\n```\n{tag}\n"),
            "mark inside an HTML comment": marked(FINDING + f"\n<!-- {tag} -->\n"),
            "mark inside a quote": marked(FINDING + f"\n> {tag}\n"),
            "mark mid-prose, then an objection": marked(FINDING + f"\n{tag}\nOne race remains in the timer path.\n"),
            "mark in the original text, not an edit": thread("T", c(CR, FINDING + MARK, t0)),
            "mark edited in by a user account named like the bot": marked(FINDING + MARK, editor_kind="User"),
            "mark on a comment by a user account named like the bot": marked(FINDING + MARK, kind="User"),
            "mark on another author's comment": thread("T", c("dev", FINDING + MARK, t0, t2, "dev"), c("dev", "x", t1)),
            "withdrawal marker inside a code fence": replied(f"```\n{withdrawn}\n```\nThanks."),
            "withdrawal marker inside a collapsed section": replied(f"<details>{withdrawn}</details>\nThanks."),
            "withdrawal marker inside a quote": replied(f"> {withdrawn}\nThanks."),
            "a withdrawal, then a point that remains": replied(f"{withdrawn}\nI withdraw that part. One point remains."),
            "a concession only inside a quote": replied("> This is addressed.\n\nSee the earlier note."),
            "a concession by a user account named like the bot": replied("This is addressed.", kind="User"),
        }
        for name, t in open_.items():
            with self.subTest(name):
                self.assertFalse(mergegate.judge_thread(t))
        settled = {
            "CodeRabbit's real shape: the mark last, after a suggestion and its prompt":
                marked(real_finding + "✅ Addressed in commits abc1234 to def5678\n\n"
                       "<!-- This is an auto-generated comment by CodeRabbit -->"),
            "confirmed as addressed": marked(real_finding + "✅ Confirmed as addressed by @dev\n"),
            "addressed in the latest update": marked(real_finding + "✅ Addressed in the latest update\n"),
            "a top-level withdrawal with no objection":
                replied(f"`@dev` Thanks.\n\n{withdrawn}\nI withdraw the finding: the path is outside the gate."),
        }
        for name, t in settled.items():
            with self.subTest(name):
                self.assertTrue(mergegate.judge_thread(t))

    def test_open_threads_are_reopened_and_refuse(self):
        objection = thread("PRRT_5", c(CR, FINDING, "2026-11-14T09:30:00Z", n=501),
                           c(CR, "I am not marking this as addressed.", "2026-11-14T10:00:00Z"), resolved=True)
        g = self.gate(self.threads(SETTLED, objection))
        self.assertRefused(g, "https://github.com/o/r/pull/9999#discussion_r501")
        reopened = g.mutations("unresolveReviewThread")
        self.assertEqual(len(reopened), 1)
        self.assertIn("id=PRRT_5", reopened[0])
        self.assertEqual(g.mutations("resolveReviewThread"), [], "nothing is resolved while a thread is open")

    def test_a_failed_resolve_refuses(self):
        cases = {"exit 1": {"exit": 1, "stderr": "HTTP 502"},
                 "not resolved": resolved_answer("resolveReviewThread", False)}
        for name, answer in cases.items():
            with self.subTest(name):
                self.assertRefused(self.gate({"resolve": {"argv": ["api", "graphql", " resolveReviewThread("],
                                                          **answer}}))

    def test_the_resolve_mutation_passes_the_thread_id(self):
        g = self.gate()
        self.assertEqual(g.code, 0, g.err)
        [call] = g.mutations("resolveReviewThread")
        query = next(a for a in call if a.startswith("query="))
        self.assertIn("$id", query)
        self.assertIn("id=PRRT_1", call)

    def test_a_deferral_settles_a_thread_and_is_recorded(self):
        g = self.gate(self.threads(SETTLED, DEFERRABLE), "--defer", "201=16")
        self.assertEqual(g.code, 0, g.err)
        [merge] = g.merges()
        body = merge[merge.index("--body") + 1]
        for text in ("discussion_r201", "#16", "I do not see another code or plan change needed in this PR"):
            self.assertIn(text, body)
            self.assertIn(text, g.out)
        self.assertNotIn("analysis", body, "the quote is CodeRabbit's prose, not its collapsed analysis")
        self.assertEqual(len(g.mutations("resolveReviewThread")), 2)

    def test_a_deferral_is_explicit_and_checked(self):
        author_last = thread("PRRT_3", c(CR, FINDING, "2026-11-14T09:30:00Z", n=301),
                             c("dev", "Tracked in #16.", "2026-11-14T09:40:00Z"))
        limited = thread("PRRT_4", c(CR, FINDING, "2026-11-14T09:30:00Z", n=401),
                         c(CR, "### Rate Limit Exceeded\nplease wait.", "2026-11-14T09:40:00Z"))
        cases = {
            "no deferral: the thread is open": ((), {}, "discussion_r201"),
            "a deferral that names no thread": (("--defer", "201=16", "--defer", "999=16"), {}, "999"),
            "a closed issue": (("--defer", "201=16"),
                               {"issue": {"argv": ["issue", "view", "16"], **ok({"number": 16, "state": "CLOSED"})}},
                               "#16"),
            "an unreadable issue": (("--defer", "201=16"),
                                    {"issue": {"argv": ["issue", "view", "16"], "exit": 1, "stderr": "not found"}},
                                    "not found"),
            "a malformed deferral": (("--defer", "r201"), {}, "--defer"),
        }
        for name, (args, over, reason) in cases.items():
            with self.subTest(name):
                self.assertRefused(self.gate({**self.threads(SETTLED, DEFERRABLE), **over}, *args), reason)
        conceded = thread("PRRT_6", c(CR, FINDING, "2026-11-14T09:30:00Z", n=601),
                          c(CR, "✅ Addressed in commit abc1234", "2026-11-14T09:40:00Z"))
        for name, t, n, reason in (("the author had the last word", author_last, 301, "not a CodeRabbit reply"),
                                   ("the last word is a rate-limit notice", limited, 401, "not a CodeRabbit reply"),
                                   ("the thread is already settled", conceded, 601, "already settled")):
            with self.subTest(name):
                g = self.gate(self.threads(t), "--defer", f"{n}=16")
                self.assertRefused(g, f"discussion_r{n}")
                self.assertIn(reason, g.err)

    def test_the_ec2_fault_cases_refuse(self):
        """The five cases the audit's stub run (EC-2) merged, against this gate."""
        cases = {
            "A: gh pr checks fails": {"check-runs": {"argv": ["api", "check-runs"], "exit": 1,
                                                     "stderr": "no checks reported"}},
            "B: two required checks missing": {"check-runs": {"argv": ["api", "check-runs"], **ok(check_runs(
                [run(n) for n in EXPECTED if n not in ("governance and living docs", "unit tests (race)")]))}},
            "C: the thread query rate-limited": {"threads": {"argv": ["api", "graphql", "reviewThreads"], "exit": 1,
                                                             "stderr": "HTTP 403: API rate limit exceeded"}},
            "D: the review list unreadable": {"reviews": {"argv": ["api", "pulls/9999/reviews"], "exit": 1,
                                                          "stderr": "HTTP 502"}},
            "F: CodeRabbit never quiet": {"issue-comments": {"argv": ["api", "issues/9999/comments"],
                                                             **ok([[activity(updated=NOW)]])}},
        }
        for name, over in cases.items():
            with self.subTest(name):
                self.assertRefused(self.gate(over))

    # Task 1.3: the merge pins the head; the resolve command only after clean
    # verdicts; a dry run; main's CI is verified on the merge commit.

    def test_a_push_during_the_run_refuses(self):
        pushed = {"pr": {"argv": ["pr", "view", "9999"], "times": 1, **ok(pr())},
                  "pr-after-a-push": {"argv": ["pr", "view", "9999"], **ok(pr(head="e" * 40))}}
        g = self.gate(pushed)
        self.assertRefused(g, "the head moved during the run")
        self.assertEqual(g.mutations("resolveReviewThread"), [], "nothing is written before the head is re-read")
        with self.subTest("changes requested: no resolve command on a moved head"):
            g = self.gate({**pushed, **self.reviews(*requested())}, env={"MERGEGATE_APPROVAL_WAIT": "20"})
            self.assertRefused(g, "the head moved during the run")
            self.assertEqual(g.posts(), [])

    def test_a_refused_merge_refuses(self):
        g = self.gate({"merge": {"argv": ["pr", "merge", "--match-head-commit"], "exit": 1, "stderr": "Head branch was modified"}})
        self.assertEqual(g.code, 2, g.err)
        self.assertIn("Head branch was modified", g.err)

    def test_dry_run_writes_nothing(self):
        g = self.gate(None, "--dry-run")
        self.assertEqual(g.code, 0, g.err)
        self.assertIn("would merge #9999 at aaaaaaa", g.out)
        self.assertEqual(g.merges(), [])
        self.assertEqual([c for c in g.calls if any(a.startswith("query=mutation") for a in c)], [])
        with self.subTest("an open, resolved thread is not reopened"):
            objection = thread("PRRT_5", c(CR, FINDING, "2026-11-14T09:30:00Z", n=501), resolved=True)
            g = self.gate(self.threads(objection), "--dry-run")
            self.assertRefused(g, "discussion_r501")
            self.assertEqual([c for c in g.calls if any(a.startswith("query=mutation") for a in c)], [])
        with self.subTest("changes requested: it says it would post the resolve command, and posts nothing"):
            g = self.gate(self.reviews(*requested()), "--dry-run")
            self.assertEqual(g.code, 0, g.err + g.out)
            self.assertIn("would post `@coderabbitai resolve`", g.out)
            self.assertIn("would merge #9999 at aaaaaaa", g.out)
            self.assertEqual(g.posts(), [])
            self.assertEqual(g.merges(), [])

    def run_list(self, *answers):
        rules = {}
        for i, answer in enumerate(answers[:-1]):
            rules[f"merge-run-{i}"] = {"argv": ["run", "list", "--commit", MERGED], "times": 1, **answer}
        return {**rules, "merge-run": {"argv": ["run", "list", "--commit", MERGED], **answers[-1]}}

    def test_main_is_verified_on_the_merge_commit(self):
        def r(status, conclusion=""):
            return ok([{"databaseId": 2, "headSha": MERGED, "status": status, "conclusion": conclusion}])
        green = self.gate()
        self.assertEqual(green.code, 0, green.err)
        self.assertIn("main's CI passed on bbbbbbb", green.out)
        self.assertEqual(self.gate(self.run_list(ok([]), r("in_progress"), r("completed", "success"))).code, 0)
        cases = {
            "failure": (self.run_list(r("completed", "failure")), {}, "failure"),
            "cancelled": (self.run_list(r("completed", "cancelled")), {}, "cancelled"),
            "never finishes": (self.run_list(r("in_progress")), {"MERGEGATE_WAIT": "0"}, "did not finish"),
            "no run appears": (self.run_list(ok([])), {"MERGEGATE_WAIT": "0"}, "did not finish"),
            "unreadable": (self.run_list({"exit": 1, "stderr": "HTTP 502"}), {}, "HTTP 502"),
            "the PR is not merged": ({"pr-merged": {"argv": ["pr", "view", "mergeCommit"],
                                                    **ok({"state": "OPEN", "mergeCommit": None})}}, {}, "OPEN"),
        }
        for name, (over, env, reason) in cases.items():
            with self.subTest(name):
                g = self.gate(over, env=env)
                self.assertEqual(g.code, 3, g.err)
                self.assertEqual(len(g.merges()), 1)
                self.assertIn("mergegate: MERGED, BUT", g.err)
                self.assertIn(reason, g.err)

    def test_the_gate_never_reviews_or_dismisses_and_posts_resolve_only_after_clean_verdicts(self):
        """Every run in this module is checked by gate(): no review, no
        dismissal, no comment but the resolve command, at most once and never
        in a dry run. These are the runs where a post would be most tempting."""
        objection = thread("PRRT_5", c(CR, FINDING, "2026-11-14T09:30:00Z", n=501), resolved=True)
        changes = self.reviews(*requested())
        runs = {
            "clean": (self.gate(), 0),
            "changes requested, every verdict clean": (self.gate(changes), 1),
            "changes requested, a thread open": (self.gate({**changes, **self.threads(objection)}), 0),
            "changes requested, a dry run": (self.gate(changes, "--dry-run"), 0),
            "a thread open": (self.gate(self.threads(objection)), 0),
            "an older commit reviewed": (self.gate(self.reviews(review(commit="c" * 40))), 0),
            "a deferral": (self.gate(self.threads(SETTLED, DEFERRABLE), "--defer", "201=16"), 0),
            "a dry run": (self.gate(None, "--dry-run"), 0),
        }
        for name, (g, posts) in runs.items():
            with self.subTest(name):
                self.assertTrue(g.calls)
                self.assertEqual(len(g.posts()), posts)
        self.assertFalse(forbidden(RESOLVE_POST))
        for call in (["pr", "comment", "9999", "--repo", "o/r", "--body", "@coderabbitai approve"],
                     ["pr", "comment", "9999", "--body", "LGTM"], ["pr", "review", "9999", "--approve"],
                     ["api", "repos/o/r/pulls/9999/reviews/1/dismissals", "-X", "PUT"]):
            self.assertTrue(forbidden(call), call)


class DocsTest(unittest.TestCase):
    QUALIFIER = "not enforced until the owner's decision O-1"

    def test_the_docs_name_no_enforced_codeowners_approval(self):
        """V-03: while main's ruleset does not require code-owner review, no
        living doc may claim a CODEOWNERS approval without the qualifier, and
        once the owner enforces it (O-1) no doc may keep the qualifier.
        Decision records, audits and plans are history and are not scanned."""
        claims = []
        for f in sorted((ROOT / "docs").rglob("*.md")):
            rel = f.relative_to(ROOT).as_posix()
            if rel.startswith(("docs/adr/", "docs/superpowers/")) or "-audit-" in rel:
                continue
            for n, line in enumerate(f.read_text(encoding="utf-8").splitlines(), 1):
                if re.search(r"CODEOWNERS|code[- ]owner", line, re.I):
                    claims.append((f"{rel}:{n}", self.QUALIFIER in line))
        self.assertTrue(claims, "the scan found no claim at all")
        wrong = [where for where, qualified in claims if qualified == mergegate.CODE_OWNER_REVIEW_ENFORCED]
        self.assertEqual(wrong, [])


if __name__ == "__main__":
    unittest.main()
