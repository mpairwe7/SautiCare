"""Runbooks say what the system does now (F-09; ADR-0034).

An escalation rings the coordinator line (M2 Part B), so a runbook that
still says it is only a recorded event sends a coordinator looking in the
wrong place. A call-back to the caller needs the audited reveal of the M5
console, so a step that asks for one must say so.
"""
import unittest

from governance.tests.helpers import ROOT

STALE = ("only a recorded event", "arrives with M2 Part B", "escalation voice call (M2)")


class StaleEscalationPhrasesTest(unittest.TestCase):
    def test_no_stale_escalation_phrases(self):
        problems = []
        for path in sorted((ROOT / "docs" / "runbooks").glob("*.md")):
            for n, line in enumerate(path.read_text().splitlines(), 1):
                where = f"{path.relative_to(ROOT)}:{n}"
                problems += [f"{where}: stale phrase {p!r}" for p in STALE if p in line]
                if "call the caller back" in line.lower() and "M5" not in line:
                    problems.append(f"{where}: a call-back to the caller without M5")
        self.assertEqual(problems, [])


if __name__ == "__main__":
    unittest.main()
