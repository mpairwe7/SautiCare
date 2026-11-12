---
status: accepted
date: 2026-11-12
decision-makers: Sauti engineering (repository owner)
consulted: Build Prompt §3 rules 5 and 8; ISP; M0 review findings I3/M13
informed: Data Protection Officer
---
# ADR-0021: Screen-answer identifiers are sealed; USSD step keys are keyed hashes

## Context and Problem Statement

Two Tool 1 answers carry identifiers: the callback number (screen 8) and a typed landmark (screen 7, option 6). Events and their audit hashes must never hold them (rules 5 and 8). The M0 step store keyed retries by `sha256(text)`, and USSD text accumulates every answer, including the callback number, so an unsalted hash of about 10^9 possible numbers is reversible. How are these answers stored, and how are retries recognised?

## Decision Drivers

* No identifier in any event payload, audit hash, log or reversible hash
* Retries and duplicate callbacks answered identically, without a second write
* Crypto-shredding by key period still works (ADR-0013)

## Considered Options

* Seal identifiers to their own row (`op.incident_identifiers`), reference them by ID; key steps with an HMAC under the identifiers key class
* Keep plaintext in a restricted table
* Salt the step hash per session

## Decision Outcome

Chosen option: **seal and reference; keyed step hashes**.
- `incident.Service.StoreIdentifier` seals the plaintext (AES-256-GCM, identifiers class) with AAD naming the new `op.incident_identifiers` row. `op.store_identifier` stores it once per (incident, idempotency key). `screen.answered` carries only `identifier_id`.
- A `screen.answered` payload is strictly `{screen, field, value[, identifier_id]}`, and the value must be a code starting with a letter. This is enforced identically in Go (`incident.ParseScreenAnswer`) and SQL (`op.check_screen_answer`), so no number or free text can enter a payload.
- USSD steps are keyed by HMAC-SHA-256 under a key derived with HKDF info `sauti/ussd-step/v1`. The message is the netstring encoding of provider, session and text. Lookups try the current and previous key period. Each step records the prompt it showed and the input length it consumed, so the next answer is the text after that prefix (a `*` typed in free text survives), and the prefix is verified by its keyed hash.

## Consequences

* Good: identifiers are shredded with their key period; logs, events, audit entries and step keys cannot reveal them.
* Bad: a retry that crosses a key rotation recomputes the answer; this is harmless because event appends are idempotent.

## Confirmation

`internal/incident/tool1_test.go::TestApplyScreenAnsweredRejectsInvalidPayloads`; `internal/platform/pg/opstore/tool1_integration_test.go::TestSQLRejectsWhatFoldRejects`; `internal/platform/pg/opstore/identifiers_integration_test.go::TestStoreIdentifierIsIdempotentAndBound`, `::TestStepStoreKeyedByMAC`; `internal/crypto/crypto_test.go::TestStepMACIsKeyedAndSeparated`; `internal/intake/service_test.go::TestCallbackNumberNeverInPayloadOrSteps`; `tests/e2e/m1_intake_test.go::TestNoCallerNumberInLogs`.
