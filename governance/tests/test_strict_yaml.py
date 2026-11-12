import unittest

from governance.tests.helpers import Tree
from tools import strict_yaml


class StrictYAMLTest(unittest.TestCase):
    def load(self, text):
        t = Tree().write("x.yaml", text)
        self.addCleanup(t.close)
        return strict_yaml.load(t.root / "x.yaml")

    def test_nested_duplicate_key_fails(self):
        with self.assertRaises(strict_yaml.YAMLError) as cm:
            self.load("a: 1\nb:\n  c: 2\n  c: 3\n")
        self.assertIn("duplicate key 'c'", strict_yaml.describe(cm.exception))

    def test_merge_key_may_be_overridden(self):
        self.assertEqual(self.load("base: &b {x: 1, y: 1}\nm:\n  <<: *b\n  x: 2\n")["m"], {"x": 2, "y": 1})


if __name__ == "__main__":
    unittest.main()
