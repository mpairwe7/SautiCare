# Model Card: Sauti Dispatch decision logic

**Type:** Reference · **Last updated:** 2026-11-12 (M1 Part A) · **Owner:** Medical Director (clinical lead of record) with the PI
**Format:** Model Cards for Model Reporting (Mitchell et al., 2019), adapted for deterministic clinical decision logic. Supports ISO/IEC 42001 §7.5, the NIST AI RMF MAP function, and EU AI Act Art. 11 documentation practice. Risk register: `governance/ai_risk_manifest.yaml`.

## 1. Summary

In the pilot, **no machine-learning model makes or influences a dispatch decision** (Build Prompt rule 3.3). The decision logic is:

| Component | Kind | Status (M0) |
|---|---|---|
| Tool 1 triage decision table | deterministic ordered table, CEL conditions, ML-DSA-65 signed, totality-checked (`triage-rules/tool1.yaml`, ADR-0019) | **built (M1)**; input lists are placeholders until Tool 1; dev-signed, `protocol_status: pending` (production refuses) |
| Responder ranking | deterministic: zone distance, then response history (smoothed accept rate over 90 days), then least recently dispatched (decision 4 default, gated); the responder ID breaks ties; version stamped on every dispatch fact | active (M2, `internal/dispatch`, [ADR-0024](adr/ADR-0024-dispatch-planners-and-postgresql-scheduler.md)) |
| Sunbird `asr-whisper-large-v3-salt` speech assist | machine-learning model, self-hosted in Uganda | **flagged off** (`speech_assist: false`) |
| Reinforcement-learning dispatcher | machine-learning model | not built: post-study only |

## 2. Intended use

- **Triage.** Classify a reported emergency as red, yellow or green from the caller's Tool 1 answers, anchored to the WHO Community First Aid Response guidance and authored by the emergency-medicine co-investigators.
- **Rules from the protocol, as described in Engineering Guide §5:**
  - **Red** if the immediate-danger check (screen 2) or the category danger question (screen 5) is yes.
  - **Yellow** if screen 2 is "I do not know" and screen 5 is no, or if the category is poisoning or snake bite and screen 5 is no.
  - **Green** otherwise.

  The exact wording and categories come from Tool 1, which is not yet received; the configuration stays `protocol_status: pending` until then.
- **Out of scope.** Diagnosis, treatment advice beyond the Tool 1 first-aid line, and any decision by a model.

## 3. Safety properties (enforced, tested)

- **Severity can never be lowered.** This is enforced by the Go reducer and the SQL write path (`internal/incident/fold_test.go::TestApplyRejectsSeverityLowering`). A merge of duplicate calls never lowers it either: the surviving incident takes the higher severity of any call it absorbs, and its escalation (`internal/platform/pg/opstore/dedup_integration_test.go::TestMergeCarriesSeverityAndEscalation`).
- **Every incident records the logic version** that classified it, together with the configuration fingerprint ([ADR-0014](adr/ADR-0014-configuration-and-amendment-gate.md)).
- **A rule error is treated as red, flagged `triage_rule_error` and escalated** (Build Prompt §14): `internal/triage/triage_test.go::TestRuntimeErrorIsRed`, `internal/intake/service_test.go::TestTriageRuleErrorIsRedAndEscalated`. The three events are one transaction with the screen 5 answer, so a failure cannot leave the red classification without its flag and escalation (`internal/intake/atomicity_test.go::TestRuleErrorFlagAndEscalationSurviveAFailure`). The coordinator voice call is the Escalate port (M2).
- **Only a signed, total table runs.** A bundle loads only with a valid signature from a trusted key, only if every combination of its inputs reaches a row, and only if its inputs equal the Tool 1 catalogue's options (`internal/triage/triage_test.go::TestLoadRejectsNonTotalTable`, `internal/intake/machine_test.go::TestCatalogueMatchesTriageDomain`).
- **Classification happens once, after screen 5**, with all four inputs, in the same transaction as the screen 5 answer; an incident that lacks it (an earlier failure) is classified when its caller redials, before any further question (`internal/intake/atomicity_test.go::TestResumeClassifiesAnIncidentThatLacksIt`). The logic version `<version>+sha256:<12 hex>` is stamped on the event.
- **Rule changes during the study** need an ethics amendment record (amendment gate) and approval by the emergency-medicine co-investigator (CODEOWNERS).

## 4. Evaluation

- **Clinical scenario suite.** `triage-rules/scenarios.yaml` runs in CI against the signed bundle (`internal/triage/triage_test.go::TestRepositoryBundleVerifies`), with an independent oracle over every input combination (`::TestRuleTableFromBuildPrompt`). It includes scenario 3, "poisoning with screen 5 = no is yellow", proven end to end by `tests/e2e/m1_intake_test.go::TestScenario3PoisoningIsYellow`.
- **Agreement measures.** Agreement between telephone triage and the responder's on-scene severity (Tool 2) is reported as a 3×3 cross-classification (Guide §13). It is a study output, never a training signal during the pilot.

## 5. Speech assist (flagged off)

| Field | Value |
|---|---|
| Model | Sunbird AI `asr-whisper-large-v3-salt` (fine-tuned Whisper large-v3) |
| Reported performance | Luganda word error rate 14.2% (Sunbird; test split not stated) |
| Allowed use, if an ethics amendment and DPIA update enable it | capture the spoken landmark only; read it back ("Kalerwe market? Press 1 for yes"); two failures fall back to the keypad menu |
| Never | decide severity or dispatch; store caller audio (processed in memory and discarded); train on caller audio without ethics approval and a DTUA |
| Residency | GPU server in Uganda; no foreign speech service (rule 3.1) |

## 6. Ethical considerations and limitations

- **No modelled health impact.** The pilot is observational and descriptive. No dashboard or export shows a modelled health impact figure (Guide §11).
- **The future learned dispatcher.** It may only be trained on approved pilot data, and must include an equity term so poorer, less accessible settlements are not deprioritised (Guide §6).
- **Arrival is the weakest measurement.** It is attested by the responder; arrival grades and evidence flags travel with every record (M3).
