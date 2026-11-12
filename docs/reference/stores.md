# Data stores and privilege matrix

**Type:** Reference (Diátaxis) · **Migrations:** `db/` · **ADRs:** [ADR-0006](../adr/ADR-0006-five-stores-and-roles.md), [ADR-0003](../adr/ADR-0003-single-write-path.md), [ADR-0005](../adr/ADR-0005-audit-ledger-checkpoints.md)

There are five PostgreSQL 18 databases (`sauti_<store>`). Each has an owner role, used for migrations only, and least-privilege runtime roles. CONNECT is revoked from PUBLIC, so a role of one store cannot connect to another.

## Retention

| Store | Holds | Retention (RMRP §3) | Destroyed by |
|---|---|---|---|
| operational | caller number (ciphertext), location, free text, responder and payment details, events | 12 months after the window closes | key-period destruction (M6) |
| analytic | study ID and the 10 MEDS fields, parish and landmark, logic version, consent status | 5 years after study completion | analytic key |
| linkage | study ID ↔ incident ID only | 12 months after the primary paper | HSM key, two approvals |
| audit | event type, incident ID, staff ID, logic version, hashes | 5 years after study completion | audit key |
| media | consented interview audio only | until the transcript is checked, ≤ 12 months | one key per recording |

## Privilege matrix (M0)

| Role | Store | Tables | Functions |
|---|---|---|---|
| `op_intake` | operational | SELECT `incidents`, `incident_events`, `sessions`; SELECT/INSERT `ussd_steps` | EXECUTE `open_incident`, `append_incident_event` |
| `op_relay` | operational | — | EXECUTE `claim_outbox`, `mark_outbox_delivered`, `default_partition_rows` |
| `op_readonly` | operational | SELECT `incidents`, `incident_events`, `event_keys`, `incident_transitions`, `outbox`, `config_versions`, `key_periods` (not `sessions`) | EXECUTE `default_partition_rows` |
| `op_ivr`, `op_dispatch`, `op_console`, `op_payments`, `op_pseudonymiser`, `op_retention` | operational | none yet: granted in their milestones | — |
| `audit_appender` | audit | — | EXECUTE `append`, `head`, `save_checkpoint` |
| `audit_reader` | audit | SELECT `entries`, `checkpoints` | — |
| `an_writer` | analytic | SELECT/INSERT/DELETE `meds_records`; SELECT/INSERT `suppression_ledger` | — |
| `an_reader` | analytic | SELECT both | — |
| `lk_writer` | linkage | INSERT `pairs` | — |
| `lk_reader` | linkage | — | EXECUTE `resolve_incident` (four reasons, every call logged) |
| `md_writer` / `md_reader` | media | INSERT / SELECT `recordings`, `chunks` | — |

No runtime role holds UPDATE or DELETE on events, audit entries or consent rows. Append-only triggers raise `SA403` even for owners on `audit.entries`, `audit.checkpoints`, `analytic.suppression_ledger` and `linkage.access_log`.

## Database-enforced invariants

- **Operational:**
  - 10-digit reference with a `CHECK` and `UNIQUE`.
  - Lifecycle table.
  - Idempotency and per-incident sequence uniqueness in `op.event_keys`.
  - Monthly partitions with a monitored DEFAULT partition.
  - The audit outbox carries only typed fields and `payload_sha256`.
- **Audit:** typed columns only; the hash is computed in SQL; appends are serialised by an advisory lock.
- **Analytic:** `consent_status` must be in (`consented`, `guardian_permission`, `assent`, `waiver`), and a waiver carries a reason (`deceased`, `untraceable`, `unable_to_understand`).
- **Linkage:** resolution only for `withdrawal`, `data_subject_request`, `payment_dispute`, `lawful_rec_uncst_request`.
- **Media:** `destroy_by` is no later than `created_at` + 366 days.
