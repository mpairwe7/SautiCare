"""The merge gate: it merges a pull request into main only when every answer
it reads says yes, and refuses on anything it cannot read (ADR-0042;
evaluation audit V-02).

`python3 -m tools.mergegate --repo OWNER/NAME PR [--dry-run]` checks, in order:
  * the PR is open, not a draft, targets main, is mergeable, and its head is
    a full commit SHA;
  * main's ruleset still requires every expected check (strictly) and
    resolved review threads;
  * main's own CI passed on main's head;
  * every expected check passed on the PR's head commit, from the app the
    ruleset names, and no other check or status failed or is pending.
Any gh error, timeout, empty or unreadable answer refuses: exit 2, with the
reason on stderr. Exit 0 means it merged (or, with --dry-run, would merge).
"""
import argparse
import json
import os
import re
import shlex
import subprocess
import sys
from pathlib import Path
from typing import Any

SHA = re.compile(r"^[0-9a-f]{40}$")
REPO = re.compile(r"^[\w.-]+/[\w.-]+$")
CHECKS_FILE = Path(__file__).with_name("required_checks.txt")


class Refused(Exception):
    """A reason not to merge."""


def _show(args) -> str:
    return " ".join(a if len(a) <= 60 else a[:57] + "..." for a in args)


def gh(*args: str) -> str:
    """Run gh; any failure to get a clean answer refuses."""
    cmd = shlex.split(os.environ.get("MERGEGATE_GH", "gh")) + list(args)
    timeout = float(os.environ.get("MERGEGATE_TIMEOUT", "120"))
    try:
        p = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        raise Refused(f"gh {_show(args)}: timed out after {timeout:g} s") from None
    except OSError as e:
        raise Refused(f"gh {_show(args)}: could not run: {e}") from None
    if p.returncode != 0:
        raise Refused(f"gh {_show(args)}: exit {p.returncode}: {p.stderr.strip() or '(no stderr)'}")
    return p.stdout


def gh_json(*args: str) -> Any:
    out = gh(*args)
    if not out.strip():
        raise Refused(f"gh {_show(args)}: empty answer")
    try:
        return json.loads(out)
    except json.JSONDecodeError as e:
        raise Refused(f"gh {_show(args)}: unreadable answer ({e})") from None


def get(obj: Any, key: str, kind: type, what: str) -> Any:
    """obj[key], which must be a `kind`; anything else refuses."""
    v = obj.get(key) if isinstance(obj, dict) else None
    if not isinstance(v, kind):
        raise Refused(f"{what}: `{key}` is missing or not a {kind.__name__}")
    return v


def pages(path: str) -> list:
    """Every page of a paginated REST endpoint (`--paginate --slurp`)."""
    p = gh_json("api", path, "--paginate", "--slurp")
    if not isinstance(p, list) or not p:
        raise Refused(f"gh api {path}: no pages")
    return p


def items(path: str) -> list[dict]:
    """The elements of a paginated REST array endpoint."""
    out = []
    for page in pages(path):
        if not isinstance(page, list) or not all(isinstance(x, dict) for x in page):
            raise Refused(f"gh api {path}: a page is not a list of objects")
        out += page
    return out


def counted(path: str, key: str) -> list[dict]:
    """The elements under `key` of a paginated REST object endpoint whose
    pages carry `total_count`; a short count refuses (a truncated answer)."""
    out, totals = [], set()
    for page in pages(path):
        out += get(page, key, list, f"gh api {path}")
        totals.add(get(page, "total_count", int, f"gh api {path}"))
    if len(totals) != 1 or len(out) != totals.pop() or not all(isinstance(x, dict) for x in out):
        raise Refused(f"gh api {path}: the answer is truncated or inconsistent")
    return out


def expected_checks() -> list[str]:
    lines = [ln.strip() for ln in CHECKS_FILE.read_text(encoding="utf-8").splitlines()]
    names = [ln for ln in lines if ln and not ln.startswith("#")]
    if not names:
        raise Refused(f"{CHECKS_FILE.name} names no check")
    return names


def check_pr(repo: str, pr: int) -> dict:
    v = gh_json("pr", "view", str(pr), "--repo", repo, "--json",
                "number,state,isDraft,baseRefName,headRefOid,mergeable,title,author")
    what = f"PR #{pr}"
    if get(v, "number", int, what) != pr:
        raise Refused(f"{what}: GitHub answered for another PR")
    if (state := get(v, "state", str, what)) != "OPEN":
        raise Refused(f"{what} is {state}, not open")
    if get(v, "isDraft", bool, what):
        raise Refused(f"{what} is a draft")
    if (base := get(v, "baseRefName", str, what)) != "main":
        raise Refused(f"{what} targets {base}, not main")
    mergeable = get(v, "mergeable", str, what)
    if mergeable == "UNKNOWN":
        raise Refused(f"{what}: GitHub has not computed mergeability yet (UNKNOWN); re-run")
    if mergeable != "MERGEABLE":
        raise Refused(f"{what} is not mergeable ({mergeable})")
    head = get(v, "headRefOid", str, what)
    if not SHA.match(head):
        raise Refused(f"{what}: the head commit is unreadable ({head!r})")
    return {"head": head, "title": get(v, "title", str, what),
            "author": get(get(v, "author", dict, what), "login", str, what)}


def check_ruleset(repo: str, expected: list[str]) -> dict[str, Any]:
    """main's rules must require each expected check, strictly, and resolved
    threads. Returns each expected name's integration_id (the app that must
    report it; None if the ruleset names none)."""
    what = "main's rules"
    weak, required = [], {}
    threads = False
    for rule in items(f"repos/{repo}/rules/branches/main"):
        kind = get(rule, "type", str, what)
        params = rule.get("parameters") or {}
        if kind == "pull_request" and params.get("required_review_thread_resolution") is True:
            threads = True
        if kind == "required_status_checks" and params.get("strict_required_status_checks_policy") is True:
            for c in get(params, "required_status_checks", list, what):
                required[get(c, "context", str, what)] = c.get("integration_id")
    if not threads:
        weak.append("resolved review threads are not required")
    if missing := [n for n in expected if n not in required]:
        weak.append("not required as strict checks: " + ", ".join(missing))
    if weak:
        raise Refused("main protection is missing or weaker than expected: " + "; ".join(weak))
    return {n: required[n] for n in expected}


def ci_run(repo: str, sha: str) -> dict | None:
    """The CI push run on main for commit sha, or None if there is none yet."""
    runs = gh_json("run", "list", "--repo", repo, "--branch", "main", "--event", "push", "--workflow", "ci",
                   "--commit", sha, "--limit", "1", "--json", "databaseId,headSha,status,conclusion")
    if not isinstance(runs, list):
        raise Refused("gh run list: unreadable answer")
    if not runs:
        return None
    run = runs[0]
    if get(run, "headSha", str, "main's CI run") != sha:
        raise Refused(f"main's CI run is for {run['headSha'][:7]}, not {sha[:7]}")
    get(run, "status", str, "main's CI run")
    return run


def check_main_green(repo: str) -> None:
    sha = get(get(gh_json("api", f"repos/{repo}/branches/main"), "commit", dict, "main"), "sha", str, "main")
    if not SHA.match(sha):
        raise Refused(f"main's head commit is unreadable ({sha!r})")
    run = ci_run(repo, sha)
    if run is None:
        raise Refused(f"main's CI has no push run on main's head {sha[:7]}")
    if (run["status"], run.get("conclusion")) != ("completed", "success"):
        raise Refused(f"main's CI on {sha[:7]} is {run['status']}/{run.get('conclusion')}, not completed/success")


def check_checks(repo: str, head: str, required: dict[str, Any]) -> None:
    """Every required check passed on head, from the app the ruleset names;
    every other check run and commit status passed, was skipped or neutral."""
    problems, passed = [], set()
    for r in counted(f"repos/{repo}/commits/{head}/check-runs?per_page=100", "check_runs"):
        name = get(r, "name", str, "a check run")
        status, conclusion = get(r, "status", str, name), r.get("conclusion")
        app = get(get(r, "app", dict, name), "id", int, name)
        if name in required and required[name] is not None and app != required[name]:
            problems.append(f"{name}: reported by app {app}, not the app main's ruleset names")
        elif status != "completed":
            problems.append(f"{name}: {status}")
        elif conclusion == "success":
            passed.add(name)
        elif name in required or conclusion not in ("neutral", "skipped"):
            problems.append(f"{name}: {conclusion}")
    for s in counted(f"repos/{repo}/commits/{head}/status?per_page=100", "statuses"):
        ctx, state = get(s, "context", str, "a commit status"), get(s, "state", str, "a commit status")
        if ctx in required:
            problems.append(f"{ctx}: a commit status, not a check run")
        elif state != "success":
            problems.append(f"{ctx}: {state}")
    problems += [f"{n}: missing" for n in required if n not in passed]
    if problems:
        raise Refused(f"checks on {head[:7]}: " + "; ".join(problems))


def say(msg: str) -> None:
    print(f"mergegate: {msg}", flush=True)


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(prog="python3 -m tools.mergegate",
                                 description="Merge a pull request into main only if every gate passes.")
    ap.add_argument("--repo", required=True, help="OWNER/NAME")
    ap.add_argument("pr", type=int)
    ap.add_argument("--dry-run", action="store_true", help="read everything, write nothing, merge nothing")
    args = ap.parse_args(argv)
    repo, pr = args.repo, args.pr
    try:
        if not REPO.match(repo):
            raise Refused(f"--repo {repo!r} is not OWNER/NAME")
        expected = expected_checks()
        info = check_pr(repo, pr)
        head = info["head"]
        say(f"PR #{pr} is open and mergeable at {head[:7]}")
        required = check_ruleset(repo, expected)
        say(f"main's ruleset requires the {len(required)} expected checks and resolved threads")
        check_main_green(repo)
        say("main's CI passed on main's head")
        check_checks(repo, head, required)
        say(f"every check passed on {head[:7]}")
        if args.dry_run:
            say(f"would merge #{pr} at {head[:7]}")
            return 0
        gh("pr", "merge", str(pr), "--repo", repo, "--merge", "--match-head-commit", head,
           "--subject", f"Merge PR #{pr}: {info['title']}")
        say(f"merged #{pr} at {head[:7]}")
        return 0
    except Refused as e:
        print(f"mergegate: REFUSED: {e}", file=sys.stderr)
        return 2
