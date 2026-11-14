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
        "merge": {"argv": ["pr", "merge"], "stdout": ""},
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
                               cwd=ROOT, env=e, capture_output=True, text=True, timeout=120)
            self.code, self.out, self.err = p.returncode, p.stdout, p.stderr
            self.calls = [json.loads(line)["argv"] for line in log.read_text().splitlines()] if log.exists() else []

    def merges(self):
        return [c for c in self.calls if c[:2] == ["pr", "merge"]]


class MergeGateTest(unittest.TestCase):
    def gate(self, overrides=None, *args, env=None):
        rules = clean()
        rules.update(overrides or {})
        return Gate(rules, *args, env=env)

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


if __name__ == "__main__":
    unittest.main()
