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
    ruleset names, and no other check or status failed or is pending;
  * CodeRabbit's latest review is of the head commit, and CodeRabbit has
    been quiet for QUIET;
  * every review thread is settled by its last word: CodeRabbit's, and a
    concession; or it is deferred explicitly (--defer) to an open issue,
    on CodeRabbit's own last word, and the merge commit records it.
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
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

SHA = re.compile(r"^[0-9a-f]{40}$")
REPO = re.compile(r"^[\w.-]+/[\w.-]+$")
CHECKS_FILE = Path(__file__).with_name("required_checks.txt")
REVIEWER = "coderabbitai"            # CodeRabbit's login in GraphQL
REVIEWER_REST = "coderabbitai[bot]"  # and in REST
# CodeRabbit answers a reply up to 22 minutes later (PR #13: 1324 s), often
# after first marking the finding addressed; its last word must be final.
QUIET = timedelta(minutes=30)
# CodeRabbit's own edit of its finding once it judges it fixed.
MARKED = re.compile(r"✅ (Addressed in (commits? |the latest)|Confirmed as addressed by )")
WITHDRAWN = "<!-- <review_comment_withdrawn> -->"
CONCEDES = re.compile(r"✅ Addressed in commit|Review thread resolved|\bLGTM\b"
                      r"|\b(is|are|has been|have been) (now )?(addressed|resolved)\b")
OBJECTS = re.compile(r"(?i)\b(not|no longer|still|remains?|however|but|unverified|missing|unresolved"
                     r"|rate.limit\w*)\b|please|\?")
# Collapsed analysis (innermost first) and hidden markers are not its word.
COLLAPSED = re.compile(r"<details\b[^>]*>(?:(?!<details\b).)*?</details>|<!--.*?-->", re.S)
DEFER = re.compile(r"^(\d+)=(\d+)$")
THREADS = """query($owner: String!, $name: String!, $n: Int!, $after: String) {
  repository(owner: $owner, name: $name) { pullRequest(number: $n) {
    reviewThreads(first: 100, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes { id isResolved comments(first: 100) {
        pageInfo { hasNextPage }
        nodes { author { login } editor { login } body url createdAt lastEditedAt } } } } } } }"""
RESOLVE = "mutation($id: ID!) { resolveReviewThread(input: {threadId: $id}) { thread { isResolved } } }"
UNRESOLVE = "mutation($id: ID!) { unresolveReviewThread(input: {threadId: $id}) { thread { isResolved } } }"


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


def when(s: Any) -> datetime:
    try:
        t = datetime.fromisoformat(s.replace("Z", "+00:00"))
    except (AttributeError, TypeError, ValueError):
        raise Refused(f"unreadable time {s!r}") from None
    if t.tzinfo is None:
        raise Refused(f"time without a zone {s!r}")
    return t


def now() -> datetime:
    s = os.environ.get("MERGEGATE_NOW")
    return when(s) if s else datetime.now(timezone.utc)


def login(obj: dict, key: str = "user") -> str | None:
    return (obj.get(key) or {}).get("login")


def check_review(repo: str, pr: int, head: str) -> bool:
    """CodeRabbit's latest review is of head, and it has been quiet for QUIET.
    Returns whether its latest decisive review requests changes."""
    mine = [r for r in items(f"repos/{repo}/pulls/{pr}/reviews?per_page=100")
            if login(r) == REVIEWER_REST and r.get("submitted_at")]
    mine.sort(key=lambda r: when(r["submitted_at"]))
    # A thread reply or a command approval is a review with an empty body,
    # stamped with whatever the head was: it is not a review of the code.
    real = [r for r in mine if prose(get(r, "body", str, "a CodeRabbit review"))]
    if not real:
        raise Refused(f"CodeRabbit has not reviewed PR #{pr}")
    if (commit := get(real[-1], "commit_id", str, "a CodeRabbit review")) != head:
        raise Refused(f"CodeRabbit's latest review is of {commit[:7]}, not the head {head[:7]} "
                      "(rate-limited or not reviewed yet); re-run once it has reviewed the head")
    times = [when(r["submitted_at"]) for r in mine]
    for path in (f"repos/{repo}/pulls/{pr}/comments?per_page=100", f"repos/{repo}/issues/{pr}/comments?per_page=100"):
        for cm in items(path):
            if login(cm) == REVIEWER_REST:
                times += [when(cm.get("created_at")), when(cm.get("updated_at"))]
    if (ago := now() - max(times)) < QUIET:
        raise Refused(f"CodeRabbit was active {int(ago.total_seconds())} s ago; "
                      f"re-run after {int(QUIET.total_seconds() // 60)} quiet minutes")
    decisive = [r["state"] for r in mine if r.get("state") in ("APPROVED", "CHANGES_REQUESTED")]
    return bool(decisive) and decisive[-1] == "CHANGES_REQUESTED"


def prose(body: str) -> str:
    """What a comment says, without its collapsed sections or hidden markers."""
    while (stripped := COLLAPSED.sub("", body)) != body:
        body = stripped
    return " ".join(body.split())


def last_words(thread: dict) -> list[tuple[int, dict, bool]]:
    """The comments (index, comment, by CodeRabbit) whose time, the later of
    written and last edited, is the thread's latest. An edit by anyone else
    makes a CodeRabbit comment someone else's word."""
    what = "a review thread"
    nodes = get(get(thread, "comments", dict, what), "nodes", list, what)
    events = []
    for i, cm in enumerate(nodes):
        t = when(get(cm, "createdAt", str, what))
        if cm.get("lastEditedAt") is not None:
            t = max(t, when(cm["lastEditedAt"]))
        get(cm, "body", str, what)
        events.append((t, i, cm, login(cm, "author") == REVIEWER and login(cm, "editor") in (None, REVIEWER)))
    latest = max((e[0] for e in events), default=None)
    return [(i, cm, mine) for t, i, cm, mine in events if t == latest]


def judge_thread(thread: dict) -> bool:
    """True when the thread is settled: every latest word is CodeRabbit's and
    concedes, as its mark on its own finding (first comment) or, in a reply,
    its withdrawal marker or a concession with no objection. Resolution is not
    a word: the resolve command resolves every thread, objections included."""
    words = last_words(thread)
    for i, cm, mine in words:
        if not mine:
            return False
        if i == 0:
            if not MARKED.search(cm["body"]):
                return False
        elif WITHDRAWN not in cm["body"]:
            said = prose(cm["body"])
            if not CONCEDES.search(said) or OBJECTS.search(said):
                return False
    return bool(words)


def review_threads(repo: str, pr: int) -> list[dict]:
    owner, name = repo.split("/")
    what = "the review-thread query"
    out, cursor, seen = [], None, set()
    while True:
        args = ["api", "graphql", "-f", f"query={THREADS}", "-f", f"owner={owner}", "-f", f"name={name}",
                "-F", f"n={pr}"] + (["-f", f"after={cursor}"] if cursor else [])
        v = gh_json(*args)
        if not isinstance(v, dict) or v.get("errors"):
            raise Refused(f"{what} failed: {v.get('errors') if isinstance(v, dict) else v!r}")
        rt = get(get(get(get(v, "data", dict, what), "repository", dict, what), "pullRequest", dict, what),
                 "reviewThreads", dict, what)
        for t in get(rt, "nodes", list, what):
            comments = get(t, "comments", dict, what)
            if get(get(comments, "pageInfo", dict, what), "hasNextPage", bool, what):
                raise Refused(f"review thread {get(t, 'id', str, what)} has more than 100 comments; "
                              "the gate does not judge part of a thread")
            get(t, "isResolved", bool, what)
            out.append(t)
        page = get(rt, "pageInfo", dict, what)
        if not get(page, "hasNextPage", bool, what):
            return out
        cursor = get(page, "endCursor", str, what)
        if cursor in seen:
            raise Refused(f"{what} repeated the page cursor {cursor!r}")
        seen.add(cursor)


def thread_url(t: dict) -> str:
    nodes = t["comments"]["nodes"]
    return nodes[0]["url"] if nodes and isinstance(nodes[0].get("url"), str) else t["id"]


def deferral(repo: str, t: dict, issue: int) -> str:
    """A thread deferred to an issue, on CodeRabbit's own last word (it says
    nothing more is needed in this PR). Returns the record of it."""
    url = thread_url(t)
    words = last_words(t)
    said = prose(words[-1][1]["body"]) if words else ""
    if len(words) != 1 or words[0][0] == 0 or not words[0][2] or re.search(r"(?i)rate.limit", said):
        raise Refused(f"--defer: the last word in {url} is not a CodeRabbit reply that evaluated it; "
                      "a deferral rests on what CodeRabbit itself said")
    v = gh_json("issue", "view", str(issue), "--repo", repo, "--json", "number,state")
    if get(v, "number", int, f"issue #{issue}") != issue or get(v, "state", str, f"issue #{issue}") != "OPEN":
        raise Refused(f"--defer: issue #{issue} for {url} is not an open issue")
    quote = said if len(said) <= 240 else said[:237] + "..."
    return f'{url} deferred to #{issue}; CodeRabbit: "{quote}"'


def set_resolved(tid: str, resolved: bool) -> None:
    name = "resolveReviewThread" if resolved else "unresolveReviewThread"
    v = gh_json("api", "graphql", "-f", f"query={RESOLVE if resolved else UNRESOLVE}", "-f", f"id={tid}")
    what = f"{name} {tid}"
    if get(get(get(get(v, "data", dict, what), name, dict, what), "thread", dict, what),
           "isResolved", bool, what) is not resolved:
        raise Refused(f"{what} did not take effect")


def check_threads(repo: str, pr: int, dry_run: bool, deferrals: dict[int, int]) -> tuple[list[str], list[str]]:
    """Judge every thread. Open threads that are resolved are reopened, so
    GitHub's own rule blocks a merge from the web page too, and refuse.
    Returns the settled threads still to resolve, and the deferral records."""
    to_resolve, records, open_, named = [], [], [], set()
    for t in review_threads(repo, pr):
        url = thread_url(t)
        m = re.search(r"#discussion_r(\d+)$", url)
        key = int(m.group(1)) if m else None
        if key in deferrals:
            named.add(key)
            if judge_thread(t):
                raise Refused(f"--defer {key}: {url} is already settled; drop the deferral")
            records.append(deferral(repo, t, deferrals[key]))
        elif not judge_thread(t):
            open_.append(t)
            continue
        if not t["isResolved"]:
            to_resolve.append(t["id"])
    if unknown := sorted(set(deferrals) - named):
        raise Refused(f"--defer {unknown[0]}: no review thread of PR #{pr} starts at discussion_r{unknown[0]}")
    if open_:
        if not dry_run:
            for t in open_:
                if t["isResolved"]:
                    set_resolved(t["id"], False)
        raise Refused(f"{len(open_)} review thread(s) open, without CodeRabbit's concession as the last word: "
                      + " ".join(thread_url(t) for t in open_))
    return to_resolve, records


def say(msg: str) -> None:
    print(f"mergegate: {msg}", flush=True)


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(prog="python3 -m tools.mergegate",
                                 description="Merge a pull request into main only if every gate passes.")
    ap.add_argument("--repo", required=True, help="OWNER/NAME")
    ap.add_argument("pr", type=int)
    ap.add_argument("--dry-run", action="store_true", help="read everything, write nothing, merge nothing")
    ap.add_argument("--defer", action="append", default=[], metavar="COMMENT=ISSUE",
                    help="settle the thread that starts at discussion_r<COMMENT> by deferring it to open issue "
                         "#<ISSUE>, on CodeRabbit's own last word; recorded in the merge commit")
    args = ap.parse_args(argv)
    repo, pr = args.repo, args.pr
    try:
        if not REPO.match(repo):
            raise Refused(f"--repo {repo!r} is not OWNER/NAME")
        deferrals = {}
        for d in args.defer:
            if not (m := DEFER.match(d)):
                raise Refused(f"--defer {d!r} is not COMMENT=ISSUE (two numbers)")
            deferrals[int(m.group(1))] = int(m.group(2))
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
        changes_requested = check_review(repo, pr, head)
        say(f"CodeRabbit reviewed {head[:7]} and has been quiet for {int(QUIET.total_seconds() // 60)} minutes")
        to_resolve, deferred = check_threads(repo, pr, args.dry_run, deferrals)
        say(f"every review thread is settled ({len(deferred)} deferred)")
        for d in deferred:
            say(f"deferred: {d}")
        if changes_requested:
            raise Refused("CodeRabbit's latest decisive review requests changes. Every thread verdict is clean, "
                          "so `@coderabbitai resolve` is now permitted (the gate never posts it); "
                          "re-run after the quiet window")
        if args.dry_run:
            say(f"would merge #{pr} at {head[:7]}")
            return 0
        for tid in to_resolve:
            set_resolved(tid, True)
        body = f"Merged by tools/mergegate (ADR-0042) at {head[:7]}."
        if deferred:
            body += "\n\nDeferred review threads:\n" + "\n".join(f"- {d}" for d in deferred)
        gh("pr", "merge", str(pr), "--repo", repo, "--merge", "--match-head-commit", head,
           "--subject", f"Merge PR #{pr}: {info['title']}", "--body", body)
        say(f"merged #{pr} at {head[:7]}")
        return 0
    except Refused as e:
        print(f"mergegate: REFUSED: {e}", file=sys.stderr)
        return 2
