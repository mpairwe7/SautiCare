"""docs/reference/repository-layout.md stays true to the tree (ADR-0040).

Every path in a row marked present or planned is a directory, and every
top-level directory in Git appears in the table.
"""
import re
import subprocess
import unittest

from governance.tests.helpers import ROOT

DOC = "docs/reference/repository-layout.md"
# A layout row: | reference location | repository path(s) | status | notes |
ROW = re.compile(r"^\|[^|]*\|([^|]*)\|\s*(present|planned|deviation)\b", re.M)
PATH = re.compile(r"`([^`]+)`")


def problems(text, is_dir, top_dirs):
    rows = [(m[2], PATH.findall(m[1])) for m in ROW.finditer(text)]
    errs = [f"{DOC}: `{p}` is marked {status} but is not a directory"
            for status, paths in rows if status != "deviation"
            for p in paths if not is_dir(p)]
    listed = {p.split("/")[0] for _, paths in rows for p in paths}
    errs += [f"{DOC}: top-level directory `{d}/` is not listed" for d in sorted(top_dirs - listed)]
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
        self.assertEqual(problems(TABLE, exists.__contains__, {"cmd", "web", "tests", "extra"}), [])

    def test_missing_planned_directory_fails(self):
        errs = problems(TABLE, {"cmd/a/"}.__contains__, {"cmd", "web"})
        self.assertEqual(errs, [f"{DOC}: `web/b/` is marked planned but is not a directory"])

    def test_unlisted_top_level_directory_fails(self):
        errs = problems(TABLE, {"cmd/a/", "web/b/"}.__contains__, {"cmd", "web", "stray"})
        self.assertEqual(errs, [f"{DOC}: top-level directory `stray/` is not listed"])

    # A table the pattern no longer reads must not pass by checking nothing.
    def test_unparsed_table_lists_nothing(self):
        errs = problems("| a | `cmd/a/` | here | |\n", lambda p: True, {"cmd"})
        self.assertEqual(errs, [f"{DOC}: top-level directory `cmd/` is not listed"])


if __name__ == "__main__":
    unittest.main()
