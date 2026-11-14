"""Runbooks say what the system does now (F-09; ADR-0034).

An escalation rings the coordinator line (M2 Part B), so a runbook that
still says it is only a recorded event sends a coordinator looking in the
wrong place. A call-back to the caller needs the audited reveal of the M5
console, so a step that asks for one, or a line that promises one, must say
so (R-3).
"""
import unittest

import yaml

from governance.tests.helpers import ROOT

STALE = ("only a recorded event", "arrives with M2 Part B", "escalation voice call (M2)")


class StaleEscalationPhrasesTest(unittest.TestCase):
    def test_no_stale_escalation_phrases(self):
        problems = []
        for path in sorted((ROOT / "docs" / "runbooks").glob("*.md")):
            for n, line in enumerate(path.read_text().splitlines(), 1):
                where = f"{path.relative_to(ROOT)}:{n}"
                problems += [f"{where}: stale phrase {p!r}" for p in STALE if p in line]
                low = line.lower()
                if ("call the caller back" in low or "will call back" in low) and "M5" not in line:
                    problems.append(f"{where}: a call-back to the caller without M5")
        self.assertEqual(problems, [])


SENTENCE = (
    "A call that reaches the fallback while ivr-controller is down is not recorded and not escalated; "
    "only the live dial reaches a coordinator, and the caller's open incident stays covered by "
    "intake-gw's sweeper, watchdog and dead-man gauge."
)


class FallbackWhileDownTest(unittest.TestCase):
    """With the Stasis application down, nothing that runs can identify the
    call's incident, so the fallback records nothing (ADR-0034): the runbook
    and the ADR say so, in the Impact section and the hand-over section."""

    def test_ivr_down_runbook_says_fallback_calls_are_not_recorded(self):
        runbook = (ROOT / "docs" / "runbooks" / "ivr-controller-down.md").read_text()
        impact = runbook.split("## Impact", 1)[1].split("\n## ", 1)[0]
        self.assertIn(SENTENCE, impact)
        adr = (ROOT / "docs" / "adr" / "ADR-0034-every-failure-path-ends-with-a-person.md").read_text()
        self.assertIn(SENTENCE, adr)



RULES = ROOT / "build" / "prometheus" / "sauti-rules.yml"
RULE_TESTS = ROOT / "build" / "prometheus" / "sauti-rules.test.yml"


def alerts():
    groups = yaml.safe_load(RULES.read_text(encoding="utf-8"))["groups"]
    return [rule for group in groups for rule in group["rules"] if "alert" in rule]


class AlertRulesTest(unittest.TestCase):
    """Every alert pages a person who must know what to do (ADR-0041 rule 8;
    V-01): it names a runbook that exists and names it back, and promtool's
    cases show it firing and quiet."""

    def test_every_alert_names_a_runbook_that_exists(self):
        problems = []
        names = [rule["alert"] for rule in alerts()]
        self.assertTrue(names, "no alert rules")
        for rule in alerts():
            path = rule.get("annotations", {}).get("runbook", "")
            runbook = ROOT / path
            if not path.startswith("docs/runbooks/") or path.endswith("_template.md") or not runbook.is_file():
                problems.append(f"{rule['alert']}: runbook {path!r} is not a runbook in docs/runbooks/")
            elif rule["alert"] not in runbook.read_text(encoding="utf-8"):
                problems.append(f"{rule['alert']}: {path} does not name the alert")
        self.assertEqual(problems, [])

    def test_every_alert_is_tested_firing_and_quiet(self):
        # promtool passes a quiet case whose alertname matches no rule, so a
        # misspelt name would prove nothing: every tested name must be a rule.
        firing, quiet = set(), set()
        for group in yaml.safe_load(RULE_TESTS.read_text(encoding="utf-8"))["tests"]:
            for case in group.get("alert_rule_test", []):
                (firing if case.get("exp_alerts") else quiet).add(case["alertname"])
        names = {rule["alert"] for rule in alerts()}
        self.assertEqual((firing | quiet) - names, set(), "tested names that are not rules")
        self.assertEqual(names - firing, set(), "alerts never shown firing")
        self.assertEqual(names - {"Watchdog"} - quiet, set(), "alerts never shown quiet")


if __name__ == "__main__":
    unittest.main()
