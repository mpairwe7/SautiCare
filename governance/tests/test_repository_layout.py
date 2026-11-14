"""docs/reference/repository-layout.md stays true to the tree (ADR-0040).

Every path in a row marked present or planned is a directory, every
top-level directory in Git appears in the table, and every location of the
reference architecture keeps its row (removing a nested row such as
`cmd/console-api/` is caught even while a sibling under `cmd/` remains).
"""
import re
import subprocess
import unittest

from governance.tests.helpers import ROOT

DOC = "docs/reference/repository-layout.md"
# A layout row: | reference location | repository path(s) | status | notes |
ROW = re.compile(r"^\|[^|]*\|([^|]*)\|\s*(present|planned|deviation)\b", re.M)
PATH = re.compile(r"`([^`]+)`")
# The reference architecture's locations (Scalable MVP Stack, §Repository
# structure; ADR-0040). Each must keep a row, whatever its status.
REQUIRED = (
    "api/proto/sauti/v1/",
    "cmd/intake-gw/", "cmd/dispatch-worker/", "cmd/console-api/", "cmd/ivr-controller/",
    "cmd/payments/", "cmd/pseudonymiser/", "cmd/retention-agent/",
    "internal/incident/", "internal/triage/", "internal/responder/", "internal/dispatch/",
    "internal/payment/", "internal/consent/", "internal/dsr/", "internal/audit/",
    "internal/crypto/", "internal/platform/",
    "triage-rules/", "db/operational/", "db/analytic/", "db/linkage/", "db/audit/",
    "web/console/", "telephony/asterisk/", "analytics/", "deploy/",
    "tests/e2e/", "tests/load/", "tests/drills/", "docs/adr/", "docs/runbooks/",
)


def problems(text, is_dir, top_dirs, required=REQUIRED):
    rows = [(m[2], PATH.findall(m[1])) for m in ROW.finditer(text)]
    all_paths = {p for _, paths in rows for p in paths}
    errs = [f"{DOC}: `{p}` is marked {status} but is not a directory"
            for status, paths in rows if status != "deviation"
            for p in paths if not is_dir(p)]
    listed = {p.split("/")[0] for _, paths in rows for p in paths}
    errs += [f"{DOC}: top-level directory `{d}/` is not listed" for d in sorted(top_dirs - listed)]
    errs += [f"{DOC}: reference location `{p}` has no row" for p in required if p not in all_paths]
    return errs


def tracked_top_dirs():
    # Tracked files only, so local clutter (.venv/, bin/, confidential sa/) is ignored.
    out = subprocess.run(["git", "ls-files", "-z"], cwd=ROOT, check=True,
                         capture_output=True, text=True).stdout
    return {f.split("/")[0] for f in out.split("\0") if "/" in f}


TABLE = """| Reference | Path | Status | Notes |
|---|---|---|---|
| a | `cmd/a/` | present | |
| b | `web/b/` | planned (M5) | |
| c | `tests/c/`, `extra/` | deviation | |
"""


class RepositoryLayoutTest(unittest.TestCase):
    def test_layout_doc_matches_the_tree(self):
        text = (ROOT / DOC).read_text(encoding="utf-8")
        self.assertEqual(problems(text, lambda p: (ROOT / p).is_dir(), tracked_top_dirs()), [])

    def test_fixture_table_passes(self):
        exists = {"cmd/a/", "web/b/"}
        self.assertEqual(problems(TABLE, exists.__contains__, {"cmd", "web", "tests", "extra"}, required=()), [])

    def test_missing_planned_directory_fails(self):
        errs = problems(TABLE, {"cmd/a/"}.__contains__, {"cmd", "web"}, required=())
        self.assertEqual(errs, [f"{DOC}: `web/b/` is marked planned but is not a directory"])

    def test_unlisted_top_level_directory_fails(self):
        errs = problems(TABLE, {"cmd/a/", "web/b/"}.__contains__, {"cmd", "web", "stray"}, required=())
        self.assertEqual(errs, [f"{DOC}: top-level directory `stray/` is not listed"])

    # A table the pattern no longer reads must not pass by checking nothing.
    def test_unparsed_table_lists_nothing(self):
        errs = problems("| a | `cmd/a/` | here | |\n", lambda p: True, {"cmd"}, required=())
        self.assertEqual(errs, [f"{DOC}: top-level directory `cmd/` is not listed"])

    # CodeRabbit on PR #17: a nested row removed while a sibling under the
    # same top-level directory remains must still fail.
    def test_removed_nested_row_fails_while_a_sibling_remains(self):
        errs = problems(TABLE, lambda p: True, {"cmd", "web", "tests", "extra"},
                        required=("cmd/a/", "cmd/gone/"))
        self.assertEqual(errs, [f"{DOC}: reference location `cmd/gone/` has no row"])

    def test_real_doc_lists_every_reference_location(self):
        text = (ROOT / DOC).read_text(encoding="utf-8")
        without = text.replace("| `cmd/console-api/` |", "| `cmd/removed/` |")
        self.assertIn(f"{DOC}: reference location `cmd/console-api/` has no row",
                      problems(without, lambda p: True, tracked_top_dirs()))


if __name__ == "__main__":
    unittest.main()
