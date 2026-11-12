import unittest

from governance.tests.helpers import FLAGS_OFF, GO_TEST, Tree, load

cc = load("governance/compliance_check.py")

VALID = """
risks:
  - id: AIR-001
    title: Wrong triage severity
    nist_function: MANAGE
    iso42001_clause: "6.1.2"
    owner_role: Emergency-medicine co-investigator
    mitigation: Deterministic signed decision table
    verified_by: ["internal/x/x_test.go::TestReal"]
aibom_inventory:
  - bom-ref: triage-table
    type: application
    name: Tool 1 triage decision table
    version: pending
    supplier: Sauti clinical team
    status: active
    data_residency: UG
  - bom-ref: sunbird-asr
    type: machine-learning-model
    name: Sunbird asr-whisper-large-v3-salt
    version: "large-v3-salt"
    supplier: Sunbird AI
    status: flagged-off
    flag: speech_assist
    data_residency: UG
"""


class ComplianceCheckTest(unittest.TestCase):
    def tree(self, manifest=VALID, flags=FLAGS_OFF):
        t = Tree().write("internal/x/x_test.go", GO_TEST)
        t.write("governance/ai_risk_manifest.yaml", manifest)
        t.write("config/gated/flags.yaml", flags)
        self.addCleanup(t.close)
        return t

    def test_valid_manifest_passes(self):
        self.assertEqual(cc.check(self.tree().root), [])

    def test_missing_verified_by_fails(self):
        errs = cc.check(self.tree(VALID.replace('    verified_by: ["internal/x/x_test.go::TestReal"]\n', "")).root)
        self.assertTrue(any("verified_by" in e for e in errs), errs)

    def test_dangling_test_pointer_fails(self):
        errs = cc.check(self.tree(VALID.replace("TestReal", "TestGone")).root)
        self.assertTrue(any("TestGone" in e for e in errs), errs)

    def test_aibom_requires_ug_residency(self):
        errs = cc.check(self.tree(VALID.replace("data_residency: UG\n  - bom-ref: sunbird", "data_residency: US\n  - bom-ref: sunbird")).root)
        self.assertTrue(any("data_residency" in e for e in errs), errs)

    def test_flagged_model_must_match_config_flag(self):
        errs = cc.check(self.tree(flags=FLAGS_OFF.replace("speech_assist: false", "speech_assist: true")).root)
        self.assertTrue(any("speech_assist" in e for e in errs), errs)

    def test_unknown_nist_function_fails(self):
        errs = cc.check(self.tree(VALID.replace("MANAGE", "WISH")).root)
        self.assertTrue(any("nist_function" in e for e in errs), errs)

    # PyYAML keeps the last of two equal keys, so a second `risks:` would
    # silently discard every earlier risk record (CodeRabbit PR #5).
    def test_duplicate_mapping_key_in_manifest_fails(self):
        errs = cc.check(self.tree(VALID + "risks: []\n").root)
        self.assertTrue(any("duplicate key 'risks'" in e for e in errs), errs)

    def test_duplicate_mapping_key_in_flags_fails(self):
        errs = cc.check(self.tree(flags=FLAGS_OFF + "speech_assist: true\n").root)
        self.assertTrue(any("duplicate key 'speech_assist'" in e for e in errs), errs)

    def test_missing_aibom_inventory_fails(self):
        errs = cc.check(self.tree(VALID.split("aibom_inventory:")[0]).root)
        self.assertTrue(any("aibom_inventory" in e for e in errs), errs)

    def test_empty_aibom_inventory_fails(self):
        errs = cc.check(self.tree(VALID.split("aibom_inventory:")[0] + "aibom_inventory: []\n").root)
        self.assertTrue(any("aibom_inventory" in e for e in errs), errs)


if __name__ == "__main__":
    unittest.main()
