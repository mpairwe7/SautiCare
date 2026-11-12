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

    def test_design_history_is_exempt(self):
        t = self.tree().write("docs/superpowers/plans/p.md", "Future `cmd/pseudonymiser/main.go`.\n")
        self.assertEqual(check(t.root), [])


if __name__ == "__main__":
    unittest.main()
