---
status: accepted
date: 2026-11-12
decision-makers: Sauti engineering (repository owner)
consulted: Engineering Guide §5, §14; Build Prompt rule 3.3, §14
informed: Medical Director, emergency medicine co-investigators, PI
---
# ADR-0019: Triage as a signed CEL decision table

## Context and Problem Statement

Severity (red, yellow, green) is classified from the Tool 1 answers, deterministically and server side (rule 3.3). The rules are clinical content authored by the emergency medicine co-investigators, not code (Guide §14), and must be versioned, frozen during the study, stamped on every incident, and fail safe: a rule error makes the incident red and alerts a coordinator (Build Prompt §14). How are the rules represented, authorised and executed?

## Decision Drivers

* Clinicians can read and own the rules; engineers cannot change them silently
* A table that leaves any combination of answers unclassified must never run
* The exact rule set that classified an incident is identifiable forever

## Considered Options

* Ordered decision table whose conditions are CEL expressions (`cel.dev/cel-go`), signed with ML-DSA-65, totality-checked at load
* Hand-written Go `switch` (code, not clinical configuration)
* Unsigned YAML table

## Decision Outcome

Chosen option: **signed CEL decision table** (`triage-rules/tool1.yaml`, `internal/triage`).
- Rows are evaluated in order; the first match gives the severity.
- A bundle loads only if its detached ML-DSA-65 signature (context `sauti-triage-rules/v1`, distinct from audit checkpoints) verifies against a key trusted in `config/gated/triage.yaml`. That file is gated and fingerprinted: who may sign clinical rules is itself a protocol parameter.
- At load, every combination of the declared input values must reach a row without error (totality), and the Tool 1 catalogue's option values must equal the bundle's input domain (`intake.CheckTriageDomain`). The domain is bounded first, at 9⁴ combinations (each input answers a single-digit menu), so a signed but oversized bundle is refused before the check expands it.
- At run time an evaluation error, no match, an out-of-domain input or a panic gives red with `RuleError`; intake then flags `triage_rule_error` and escalates.
- `LogicVersion` = `<version>+sha256:<12 hex>` is stamped on `incident.classified`.
- Classification happens once, after screen 5, when all four inputs are known. Severity can only rise (ADR-0003).
- Until Tool 1 arrives the bundle encodes the protocol's rule with placeholder input lists, `protocol_status: pending`, and a development signature (`dev-` key prefix); production refuses both. The category "poisoning" carries the label "Poisoning or snake bite" until Tool 1 settles open item O11.

## Consequences

* Good: clinicians review a short table plus a scenario file (`triage-rules/scenarios.yaml`); an unsigned or edited bundle cannot run; the totality check catches a gap before go-live.
* Bad: a new runtime dependency (cel-go and its protobuf/ANTLR dependencies; dependency note in `docs/reference/dependencies.md`); every rule change needs a signature from the clinical key (intended).

## Confirmation

`internal/triage/triage_test.go::TestScenario3PoisoningScreen5NoIsYellow`, `::TestRuleTableFromBuildPrompt`, `::TestLoadRejectsBadSignature`, `::TestLoadRejectsNonTotalTable`, `::TestLoadRejectsOversizedDomain`, `::TestRuntimeErrorIsRed`, `::TestRepositoryBundleVerifies`; `internal/crypto/crypto_test.go::TestSignatureContextsAreSeparated`; `internal/intake/service_test.go::TestTriageRuleErrorIsRedAndEscalated`; `internal/platform/wiring/intake_integration_test.go::TestBuildIntakeRefusesATamperedBundle`.
