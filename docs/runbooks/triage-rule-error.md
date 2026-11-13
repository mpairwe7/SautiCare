# Runbook: Triage rule error

| Field | Value |
|---|---|
| Owner | Medical Director (clinical) with the on-call engineer |
| Severity | SEV-2 |
| Alert / trigger | an `incident.flagged` event with flag `triage_rule_error` (and its `incident.escalated`); intake-gw refusing to start with a triage bundle error |
| Last reviewed: | 2026-11-13 |
| Last drilled | not yet drilled |

## Summary
The signed triage table could not classify an incident: an evaluation error, no matching row, or an answer outside the bundle's declared values. The incident was made **red**, flagged and escalated (Build Prompt §14), so dispatch is not delayed. This runbook finds the cause and restores a correct bundle.

## Triggers and detection
- A `triage_rule_error` flag on any incident (the coordinator board, from M5; until then a query by the on-call engineer).
- intake-gw exits at start-up with `triage: bundle signature not valid`, `triage: invalid rule bundle … not total`, or `intake: catalogue and triage bundle disagree`.

## Impact
The affected incident is dispatched as red, which may over-triage it. The dispatch path is **not** blocked. A start-up refusal stops new intake on that instance, so the other instances and the IVR keep answering.

## Diagnosis
1. Read the logic version in force: the `triage=` field of intake-gw's start-up log line.
2. List the affected incidents (read-only role):
   `SELECT id, logic_version, category, age_band FROM op.incidents WHERE 'triage_rule_error' = ANY (flags) ORDER BY created_at DESC;`
3. Re-run the clinical suite against the committed bundle: `go test ./internal/triage/ -run TestRepositoryBundleVerifies -v`.
4. Compare the Tool 1 catalogue's option values with the bundle's `inputs:` (`go test ./internal/intake/ -run TestCatalogueMatchesTriageDomain`). A bundle whose inputs give more than 9⁴ combinations is refused at load, before the totality check.

## Mitigation
1. Never edit an incident's severity: it can only rise, and the classification stands in the record.
2. The emergency-medicine co-investigator corrects `triage-rules/tool1.yaml`, adds a scenario to `triage-rules/scenarios.yaml`, and signs the bundle with the clinical key (`sautictl triage sign`; in production through OpenBao).
3. During the study window, the change needs an amendment record (`governance/amendments.yaml`); record a deviation for the affected incidents.
4. Deploy; intake-gw verifies the signature, the totality and the domain match before it serves.

## Escalation
Medical Director by voice at once for any red-by-error incident (ISP §5.6). PI within 24 h.

## Verification
intake-gw starts and logs the new `triage=` version; `TestRepositoryBundleVerifies` passes; no new `triage_rule_error` flags appear.

## References
ADR-0019; `docs/reference/tool1-flow.md`; Build Prompt §14 (fallbacks); Engineering Guide §5, §14.
