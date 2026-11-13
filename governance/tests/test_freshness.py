import unittest

from governance.tests.helpers import GO_TEST, Tree
from tools.freshness import check

ADR = """---
status: accepted
date: 2026-11-12
---
# ADR-0001: Example

## Context and Problem Statement
x
## Decision Outcome
x
## Consequences
x
## Confirmation
x
"""

RUNBOOK = """# Runbook: Example

Last reviewed: 2026-11-12

## Summary
## Triggers and detection
## Impact
## Diagnosis
## Mitigation
## Escalation
## Verification
"""


class FreshnessTest(unittest.TestCase):
    def tree(self):
        t = Tree()
        t.write("internal/x/x_test.go", GO_TEST)
        t.write("docs/adr/ADR-0001-example.md", ADR)
        t.write("docs/adr/README.md", "| [ADR-0001](ADR-0001-example.md) | Example |\n")
        t.write("docs/runbooks/_template.md", RUNBOOK)
        t.write("docs/runbooks/example.md", RUNBOOK)
        t.write("docs/reference/x.md", "See `internal/x/x_test.go` and `docs/adr/README.md`.\n")
        t.write("docs/traceability.md", "| HC-1 | `internal/x/x_test.go::TestReal` |\n")
        self.addCleanup(t.close)
        return t

    def test_clean_tree_passes(self):
        self.assertEqual(check(self.tree().root), [])

    def test_missing_path_fails(self):
        t = self.tree().write("docs/reference/y.md", "Read `internal/gone/file.go`.\n")
        self.assertTrue(any("internal/gone/file.go" in e for e in check(t.root)))

    def test_adr_missing_heading_fails(self):
        t = self.tree().write("docs/adr/ADR-0001-example.md", ADR.replace("## Confirmation\nx\n", ""))
        self.assertTrue(any("Confirmation" in e for e in check(t.root)))

    def test_unindexed_adr_fails(self):
        t = self.tree().write("docs/adr/ADR-0002-other.md", ADR.replace("0001", "0002"))
        self.assertTrue(any("ADR-0002-other.md" in e for e in check(t.root)))

    def test_runbook_missing_section_fails(self):
        t = self.tree().write("docs/runbooks/example.md", RUNBOOK.replace("## Escalation\n", ""))
        self.assertTrue(any("Escalation" in e for e in check(t.root)))

    def test_traceability_dangling_pointer_fails(self):
        t = self.tree().write("docs/traceability.md", "| HC-1 | `internal/x/x_test.go::TestMissing` |\n")
        self.assertTrue(any("TestMissing" in e for e in check(t.root)))

    # Without the matrix there is nothing to check, which must not pass
    # (CodeRabbit PR #5).
    def test_missing_traceability_matrix_fails(self):
        t = self.tree()
        (t.root / "docs" / "traceability.md").unlink()
        self.assertTrue(any("docs/traceability.md" in e and "missing" in e for e in check(t.root)), check(t.root))

    # A backticked pointer whose name is malformed must be reported, not
    # skipped because the strict pattern does not match it (CodeRabbit PR #5).
    def test_traceability_malformed_pointer_fails(self):
        t = self.tree().write("docs/traceability.md", "| HC-1 | `internal/x/x_test.go::TestReal-more` |\n")
        errs = check(t.root)
        self.assertTrue(any("TestReal-more" in e for e in errs), errs)

    # A pointer must name a test, not any function that happens to exist
    # (CodeRabbit PR #5).
    def test_pointer_to_non_test_function_fails(self):
        t = self.tree().write("tools/x/helper.py", "def resolve(p):\n    return p\n")
        t.write("docs/traceability.md", "| HC-1 | `tools/x/helper.py::resolve` |\n")
        errs = check(t.root)
        self.assertTrue(any("resolve" in e and "test" in e for e in errs), errs)

    # `::TestName` is shorthand for the path of the previous pointer on the
    # same line (the ADR style). It is validated against that path.
    def test_shorthand_pointer_resolves_against_the_previous_path(self):
        go = GO_TEST + "\nfunc TestOther(t *testing.T) {}\n"
        t = self.tree().write("internal/x/x_test.go", go)
        t.write("docs/traceability.md", "| HC-1 | `internal/x/x_test.go::TestReal`, `::TestOther` |\n")
        self.assertEqual(check(t.root), [])
        t.write("docs/traceability.md", "| HC-1 | `internal/x/x_test.go::TestReal`, `::TestGone` |\n")
        errs = check(t.root)
        self.assertTrue(any("internal/x/x_test.go::TestGone" in e for e in errs), errs)

    def test_shorthand_pointer_without_a_previous_path_fails(self):
        t = self.tree().write("docs/traceability.md", "| HC-1 | `::TestReal` |\n| HC-2 | `internal/x/x_test.go::TestReal` |\n")
        errs = check(t.root)
        self.assertTrue(any("'::TestReal'" in e for e in errs), errs)

    def test_python_test_method_pointer_resolves(self):
        t = self.tree().write("tools/x/x_test.py", "class T:\n    def test_ok(self):\n        pass\n")
        t.write("docs/traceability.md", "| HC-1 | `tools/x/x_test.py::test_ok` |\n")
        self.assertEqual(check(t.root), [])

    def test_design_history_is_exempt(self):
        t = self.tree().write("docs/superpowers/plans/p.md", "Future `cmd/pseudonymiser/main.go`.\n")
        self.assertEqual(check(t.root), [])


if __name__ == "__main__":
    unittest.main()
