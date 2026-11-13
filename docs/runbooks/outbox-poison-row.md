# Runbook: Outbox poison row

| Field | Value |
|---|---|
| Owner | Platform engineering on call, with the Data Protection Officer |
| Severity | SEV-2 |
| Alert / trigger | `sauti_outbox_poison_total` increases; outbox-relay exits with `halted on a poison row` |
| Last reviewed: | 2026-11-12 |
| Last drilled | Not yet drilled (M0, development only) |

## Summary

An audit outbox row does not match the payload contract: an unknown key, a malformed field, trailing data after the JSON object, or an entry ID that does not match the row. The relay stops rather than skipping the row or copying unknown fields into the five-year ledger, because an unknown field could carry personal data (hard constraint 8).

## Triggers and detection

* Metric `sauti_outbox_poison_total`.
* Log line `audit relay stopped on poison row` with the outbox ID only.

## Impact

* **Dispatch is not affected**.
* The audit ledger stops advancing until the row is resolved; the lag alert follows.

## Diagnosis

1. As `op_readonly`, read the row: `SELECT id, created_at, payload FROM op.outbox WHERE id = '<outbox_id>';`
2. Identify which code path wrote it. Only `op.enqueue_audit` should write audit rows, so a poison row means a migration or code change broke the contract.
3. If the payload contains anything resembling personal data, treat it as a breach (ISP §7) before going further.

## Mitigation

1. Fix the producer in a pull request: a forward migration that corrects `op.enqueue_audit`, or relay code that accepts a new, reviewed field.
2. A malformed row that cannot be relayed is corrected by a forward migration that rewrites it to the contract, under a deviation record. It is never deleted silently.
3. Restart the outbox-relay.

## Escalation

DPO immediately if personal data is suspected; platform lead for every occurrence.

## Verification

The relay resumes, `sauti_outbox_lag_seconds` falls, and `sautictl verify-ledger` passes.

## References

ADR-0004, ADR-0005; `internal/audit/outbox.go` (`ParseOutboxPayload`).
