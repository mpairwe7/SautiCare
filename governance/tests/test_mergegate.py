"""tools/mergegate against a stub gh (ADR-0042; evaluation audit V-02, EC-2).

Each test starts from a clean scenario, in which every answer allows the
merge, and overrides one answer. The gate runs as `python3 -m
tools.mergegate --repo o/r 9999`, exactly as an operator runs it.
"""
import json
import os
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


def activity(created="2026-11-14T09:30:00Z", updated="2026-11-14T10:30:00Z", user=CR_REST):
    return {"user": {"login": user}, "created_at": created, "updated_at": updated}


def c(author, body, at, edited=None, editor=None, n=0):
    return {"author": {"login": author} if author else None, "editor": {"login": editor} if editor else None,
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
    }


class Gate:
    """One run of the gate against a scenario, with the calls it made."""

    def __init__(self, rules, *args, env=None):
        with tempfile.TemporaryDirectory() as tmp:
            scen = Path(tmp) / "scenario.json"
            log = Path(tmp) / "calls.jsonl"
            scen.write_text(json.dumps(list(rules.values()) if isinstance(rules, dict) else rules))
            e = dict(os.environ, MERGEGATE_GH=f"{shlex.quote(sys.executable)} {shlex.quote(str(STUB))}",
                     MERGEGATE_NOW=NOW, MERGEGATE_POLL="0", STUB_GH_SCENARIO=str(scen), STUB_GH_LOG=str(log))
            e.update(env or {})
            p = subprocess.run([sys.executable, "-m", "tools.mergegate", "--repo", "o/r", "9999", *args],
                               cwd=ROOT, env=e, capture_output=True, text=True, timeout=30)
            self.code, self.out, self.err = p.returncode, p.stdout, p.stderr
            self.calls = [json.loads(line)["argv"] for line in log.read_text().splitlines()] if log.exists() else []

    def merges(self):
        return [c for c in self.calls if c[:2] == ["pr", "merge"]]

    def mutations(self, name):
        return [c for c in self.calls if c[:2] == ["api", "graphql"] and any(f"{name}(" in a for a in c)
                and not (name == "resolveReviewThread" and any("unresolveReviewThread(" in a for a in c))]


def forbidden(call):
    """A call that asks CodeRabbit for anything, comments, reviews, dismisses
    a review, bypasses the rules, or writes through the REST API."""
    return (call[:2] in (["pr", "comment"], ["pr", "review"], ["issue", "comment"])
            or any(a in ("--admin", "--auto", "-X", "--method") or "dismissals" in a or "@coderabbitai" in a
                   for a in call))


class MergeGateTest(unittest.TestCase):
    def gate(self, overrides=None, *args, env=None):
        rules = clean()
        rules.update(overrides or {})
        g = Gate(rules, *args, env=env)
        self.assertEqual([c for c in g.calls if forbidden(c)], [], "the gate never asks for or dismisses a review")
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

    def test_a_failing_or_pending_check_refuses(self):
        def with_(extra, drop=None):
            return check_runs([run(n) for n in EXPECTED if n != drop] + extra)
        cases = {
            "an expected check failed": with_([run("unit tests (race)", conclusion="failure")], "unit tests (race)"),
            "an expected check is pending": with_([run("fuzz smoke", "in_progress", None)], "fuzz smoke"),
            "an expected check was skipped": with_([run("fuzz smoke", conclusion="skipped")], "fuzz smoke"),
            "GitGuardian failed": with_([run("GitGuardian Security Checks", conclusion="failure", app=46505)]),
            "CodeRabbit is reviewing": with_([run("CodeRabbit", "in_progress", None, app=347564)]),
            "an expected name from another app": with_([run("unit tests (race)", app=999)], "unit tests (race)"),
        }
        for name, page in cases.items():
            with self.subTest(name):
                self.assertRefused(self.gate({"check-runs": {"argv": ["api", "check-runs"], **ok(page)}}))
        with self.subTest("a failing commit status"):
            bad = statuses([{"context": "ci/other", "state": "failure"}])
            self.assertRefused(self.gate({"statuses": {"argv": ["api", "/status?"], **ok(bad)}}), "ci/other")

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

    def test_changes_requested_refuses(self):
        g = self.gate(self.reviews(review(state="CHANGES_REQUESTED", at="2026-11-14T09:00:00Z"), review()))
        self.assertRefused(g, "requests changes")
        self.assertIn("@coderabbitai resolve", g.err, "the refusal says the command is now permitted")
        self.assertTrue(any("reviewThreads" in a for call in g.calls for a in call), "threads judged first")
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

    # Task 1.3: the merge pins the head; no approval by command; a dry run;
    # main's CI is verified on the merge commit.

    def test_a_push_during_the_run_refuses(self):
        g = self.gate({"pr": {"argv": ["pr", "view", "9999"], "times": 1, **ok(pr())},
                       "pr-after-a-push": {"argv": ["pr", "view", "9999"], **ok(pr(head="e" * 40))}})
        self.assertRefused(g, "the head moved during the run")
        self.assertEqual(g.mutations("resolveReviewThread"), [], "nothing is written before the head is re-read")

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

    def test_the_gate_never_asks_for_or_dismisses_a_review(self):
        """Every run in this module is checked by gate(); these are the runs
        where a command or dismissal would be most tempting."""
        objection = thread("PRRT_5", c(CR, FINDING, "2026-11-14T09:30:00Z", n=501), resolved=True)
        runs = [self.gate(),
                self.gate(self.reviews(review(state="CHANGES_REQUESTED"))),
                self.gate(self.threads(objection)),
                self.gate(self.reviews(review(commit="c" * 40))),
                self.gate(self.threads(SETTLED, DEFERRABLE), "--defer", "201=16"),
                self.gate(None, "--dry-run")]
        self.assertTrue(all(g.calls for g in runs))
        self.assertTrue(forbidden(["pr", "comment", "9999", "--body", "@coderabbitai resolve"]))


if __name__ == "__main__":
    unittest.main()
