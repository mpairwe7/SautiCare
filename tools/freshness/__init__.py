"""Docs-as-code freshness gate (global engineering rules; spec §10).

`python3 -m tools.freshness --check` fails when living documentation drifts
from the code:
  * a backticked repository path in docs/ or README.md does not exist;
  * an ADR is not indexed in docs/adr/README.md or lacks a mandatory MADR
    section;
  * a runbook lacks a template section or its "Last reviewed:" line;
  * docs/traceability.md is missing, or a backticked `path::name` pointer in
    it is malformed or does not resolve to a test function. `::name` is
    shorthand for the path of the previous pointer on the same line.
docs/superpowers/ (design history: specs and plans) is exempt from the path
check because it describes code that later milestones will create.
"""
import re
from pathlib import Path

from tools.freshness.pointers import resolve

TOP_DIRS = ("internal", "cmd", "db", "docs", "config", "governance", "threat-model",
            "tools", "scripts", "tests", "build", ".github")
PATH_TOKEN = re.compile(r"`((?:" + "|".join(re.escape(d) for d in TOP_DIRS) + r")/[^`\s]+)`")
# Any backticked token containing "::" is a pointer candidate; resolve()
# validates its full syntax, so a malformed one is reported, not skipped.
POINTER_TOKEN = re.compile(r"`([^`\s]*::[^`\s]*)`")
ADR_SECTIONS = ("## Context and Problem Statement", "## Decision Outcome", "## Consequences", "## Confirmation")
RUNBOOK_SECTIONS = ("## Summary", "## Triggers and detection", "## Impact", "## Diagnosis",
                    "## Mitigation", "## Escalation", "## Verification")


def _docs(root: Path):
    files = sorted((root / "docs").rglob("*.md")) if (root / "docs").is_dir() else []
    if (root / "README.md").is_file():
        files.append(root / "README.md")
    return [f for f in files if "superpowers" not in f.relative_to(root).parts]


def check(root: Path) -> list[str]:
    root = Path(root)
    errors: list[str] = []
    for f in _docs(root):
        rel = f.relative_to(root)
        for token in PATH_TOKEN.findall(f.read_text(encoding="utf-8")):
            path = token.split("::")[0].rstrip(".,;:)")
            path = re.sub(r":\d+(-\d+)?$", "", path)
            if any(ch in path for ch in "*?<>{}") or path.endswith("/..."):
                continue
            if not (root / path).exists():
                errors.append(f"{rel}: path `{path}` does not exist")

    adr_dir = root / "docs" / "adr"
    if adr_dir.is_dir():
        index = (adr_dir / "README.md").read_text(encoding="utf-8") if (adr_dir / "README.md").is_file() else ""
        for adr in sorted(adr_dir.glob("ADR-*.md")):
            text = adr.read_text(encoding="utf-8")
            if adr.name not in index:
                errors.append(f"docs/adr/{adr.name}: not listed in docs/adr/README.md")
            for section in ADR_SECTIONS:
                if section not in text:
                    errors.append(f"docs/adr/{adr.name}: missing section '{section}'")

    rb_dir = root / "docs" / "runbooks"
    if rb_dir.is_dir():
        for rb in sorted(rb_dir.glob("*.md")):
            if rb.name in ("_template.md", "README.md"):
                continue
            text = rb.read_text(encoding="utf-8")
            for section in RUNBOOK_SECTIONS:
                if section not in text:
                    errors.append(f"docs/runbooks/{rb.name}: missing section '{section}'")
            if "Last reviewed:" not in text:
                errors.append(f"docs/runbooks/{rb.name}: missing 'Last reviewed:'")

    trace = root / "docs" / "traceability.md"
    if not trace.is_file():
        errors.append("docs/traceability.md: missing (the traceability matrix is required)")
    else:
        for line in trace.read_text(encoding="utf-8").splitlines():
            path = ""
            for ptr in POINTER_TOKEN.findall(line):
                if ptr.startswith("::") and path:
                    ptr = path + ptr
                path = ptr.split("::")[0]
                if problem := resolve(root, ptr):
                    errors.append(f"docs/traceability.md: {problem}")
    return errors
