import unittest

from governance.tests.helpers import GO_TEST, Tree, load

vt = load("threat-model/validate_threats.py")


def threats(categories="stride: Tampering", constraints=range(1, 15)):
    return f"""
threats:
  - id: T-001
    title: Ledger rewritten
    {categories}
    asset: audit ledger
    hard_constraints: [{", ".join(str(c) for c in constraints)}]
    mitigations: [hash chain]
    verified_by: ["internal/x/x_test.go::TestReal", "planned:M2 failover drill"]
"""


class ValidateThreatsTest(unittest.TestCase):
    def tree(self, text):
        t = Tree().write("internal/x/x_test.go", GO_TEST).write("threat-model/threats.yaml", text)
        self.addCleanup(t.close)
        return t

    def test_valid_threats_pass(self):
        self.assertEqual(vt.check(self.tree(threats()).root), [])

    def test_unknown_category_fails(self):
        errs = vt.check(self.tree(threats(categories="stride: Mischief")).root)
        self.assertTrue(any("category" in e for e in errs), errs)

    def test_hard_constraint_uncovered_fails(self):
        errs = vt.check(self.tree(threats(constraints=range(1, 14))).root)
        self.assertTrue(any("hard constraint 14" in e for e in errs), errs)

    def test_dangling_pointer_fails(self):
        errs = vt.check(self.tree(threats().replace("TestReal", "TestNope")).root)
        self.assertTrue(any("TestNope" in e for e in errs), errs)


if __name__ == "__main__":
    unittest.main()
