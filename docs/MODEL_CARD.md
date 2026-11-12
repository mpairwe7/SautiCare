# Model Card: Sauti Dispatch decision logic

**Type:** Reference · **Last updated:** 2026-11-12 (M0) · **Owner:** Medical Director (clinical lead of record) with the PI
**Format:** Model Cards for Model Reporting (Mitchell et al., 2019), adapted for deterministic clinical decision logic. Supports ISO/IEC 42001 §7.5, the NIST AI RMF MAP function, and EU AI Act Art. 11 documentation practice. Risk register: `governance/ai_risk_manifest.yaml`.

## 1. Summary

In the pilot, **no machine-learning model makes or influences a dispatch decision** (Build Prompt rule 3.3). The decision logic is:

| Component | Kind | Status (M0) |
|---|---|---|
| Tool 1 triage decision table | deterministic rules in CEL, signed and versioned (M1) | planned: protocol wording pending |
| Responder ranking | deterministic: zone distance, then response history, then least recently dispatched (decision 4 default) | planned (M2) |
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

- **Severity can never be lowered.** This is enforced by the Go reducer and the SQL write path (`internal/incident/fold_test.go::TestApplyRejectsSeverityLowering`).
- **Every incident records the logic version** that classified it, together with the configuration fingerprint ([ADR-0014](adr/ADR-0014-configuration-and-amendment-gate.md)).
- **A rule error is treated as red and escalates to a coordinator by voice** (Build Prompt §14). Implemented in M1.
- **Rule changes during the study** need an ethics amendment record (amendment gate) and approval by the emergency-medicine co-investigator (CODEOWNERS).

## 4. Evaluation

- **Clinical scenario suite.** It runs before every rule release (M1), and covers Build Prompt §16 scenarios 1–3 and 11, including "poisoning with screen 5 = no is yellow".
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
