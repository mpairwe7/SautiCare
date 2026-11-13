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
| `op_intake` | operational | SELECT `incidents`, `incident_events`, `sessions`; SELECT/INSERT `ussd_steps` | EXECUTE `open_incident`, `append_incident_event` (never a staff actor, `incident.split` or `incident.unmerged`: the function refuses them to every role but `op_console`), `dedup_candidates` (reads the merge candidates and locks them `FOR SHARE` until the dispatch decision commits; also granted to `op_ivr` ahead of M1 Part C); M2: `set_availability`, `accept_alert`, `decline_alert`; SELECT `responders` (`id`, `phone_mac`, `status` only), `stages`, `facilities`; EXECUTE `dispatch_suspended`; EXECUTE `record_delivery_report` (the SMS delivery-report callback) |
| `op_relay` | operational | — | EXECUTE `claim_outbox`, `mark_outbox_delivered`, `default_partition_rows`, `ensure_event_partitions` (the leader keeps partitions three months ahead) |
| `op_readonly` | operational | SELECT `incidents`, `incident_events`, `event_keys`, `incident_transitions`, `outbox`, `config_versions`, `key_periods`, the registry without identifiers (`responders` minus name, phone and MAC columns; `availability`), `stages`, `facilities`, `alerts`, `assignments`, `alert_attempts`, `forwards`, `caller_notices`, `dispatch_timers`, `dispatch_control`, `dispatch_control_log`, `notice_attempts`, `forward_attempts`, `coordinator_calls`, `bridge_calls`, `responder_menu_pins` (not `sessions`) | EXECUTE `default_partition_rows`, `dispatch_surge` |
| `op_console` | operational | SELECT `incidents`, `incident_events` | EXECUTE `split_incident` (coordinator split, M1); `register_responder`, `set_responder_gates`, `set_availability` (registry, M2); `suspend_dispatch`, `resume_dispatch` (staff identity required), `dispatch_suspended`; SELECT `dispatch_control`, `dispatch_control_log`; SELECT `alerts` (`id`, `incident_id`, `responder_id`, `role`, `sent_at`, `outcome`, `decline_reason`, `responded_at`: a responder's alert history with decline reasons, scenario 10); `dispatch_surge` (M2 Part C) |
| `op_ivr` | operational | SELECT `incidents`, `incident_events`, `sessions`, gazetteer tables, `config_versions` (never `ussd_steps`) | EXECUTE `open_incident`, `append_incident_event`, `resume_session`, `store_identifier`, `match_landmark` (M1 voice line); M2: `set_availability`, `accept_alert`, `decline_alert`; `acknowledge_forward`; SELECT `responders` (`id`, `phone_mac`, `status` only), `stages`, `facilities`; EXECUTE `dispatch_suspended`; SELECT `alerts` (`id`, `incident_id`, `responder_id`, `role`, `outcome`, `sent_at`), `availability` (the responder menu and voice alerts); voice alerts: EXECUTE `begin_attempt`, `finish_attempt`, SELECT `alert_attempts` (`id`, `alert_id`, `purpose`, `channel`, `status`), `responders` (`phone_ct`, `phone_key_id`: the sealed number to dial); forwarding and coordinator calls: EXECUTE `begin_forward_attempt`, `finish_forward_attempt`, `claim_coordinator_calls`, `acknowledge_coordinator_call`, SELECT `forwards`, `forward_attempts`; EXECUTE `responder_eligible_now` (a voice alert only while the responder is still eligible); the masked bridge (M2 Part C): EXECUTE `bridge_targets_for_responder`, `bridge_targets_for_caller`, `begin_bridge_call`, `bridge_call_answered`, `finish_bridge_call`, `caller_contact` (the sealed number of the caller's leg, opened only to dial it) |
| `op_dispatch` | operational | SELECT `incidents`, `incident_events`, `responders`, `availability`, `alerts`, `assignments`, `alert_attempts`, `forwards`, `caller_notices`, `dispatch_timers`, `dispatch_control_log` (the stopping rule's resumption anchor), `stages`, `facilities`, `config_versions` | EXECUTE `issue_alert`, `close_alert`, `record_forward`, `time_out_forward`, `notify_caller`, `dispatch_escalate`, `begin_attempt`, `finish_attempt`, `claim_due_dispatch`, `schedule_dispatch`, `auto_suspend_dispatch` (stopping rule only), `dispatch_suspended`, `begin_notice_attempt`, `finish_notice_attempt`, `caller_contact` (the caller's sealed number for SMS, never the tables), `claim_availability_reminders`, `begin_forward_attempt`, `finish_forward_attempt`, `responder_eligible_now` (M2; never the generic append); SELECT `notice_attempts`, `forward_attempts`; `dispatch_surge` (M2 Part C) |
| `op_payments`, `op_pseudonymiser`, `op_retention` | operational | none yet: granted in their milestones | — |
| `audit_appender` | audit | — | EXECUTE `append`, `head`, `save_checkpoint` |
| `audit_reader` | audit | SELECT `entries`, `checkpoints` | — |
| `an_writer` | analytic | SELECT/DELETE `meds_records`, INSERT on its data columns only (not `exported_at`); SELECT `suppression_ledger`, INSERT on its data columns only (not `withdrawn_at`) | — |
| `an_reader` | analytic | SELECT both | — |
| `lk_writer` | linkage | INSERT `pairs` (`study_id`, `incident_id` only: `created_at` is set by the database) | — |
| `lk_reader` | linkage | — | EXECUTE `resolve_incident` (four reasons; accessor must be a staff UUID; every call logged, a denied one with reason `disallowed` and no free text) |
| `md_writer` / `md_reader` | media | INSERT on data columns only (not `created_at`, `transcript_checked_at`) / SELECT `recordings`, `chunks` | — |

No runtime role holds UPDATE or DELETE on events, audit entries or consent rows. Append-only triggers raise `SA403` even for owners on `audit.entries`, `audit.checkpoints`, `analytic.suppression_ledger` and `linkage.access_log`.

## Database-enforced invariants

- **Dispatch (M2, ADR-0025):**
  - availability periods of one responder never overlap (PostgreSQL 18 `PRIMARY KEY (responder_id, period WITHOUT OVERLAPS)`, which needs the contrib extension `btree_gist`; like `pg_trgm` it lives in its own schema, `gist`, with no USAGE for runtime roles), half-open, 8 hours on the database clock read after the responder's lock;
  - a responder registers only at a stage of their ward in the gazetteer in force (`op.register_responder`, migration 0023);
  - one alert per responder per incident (`UNIQUE (incident_id, responder_id)`); the database refuses an alert to a responder who is not active, certified, insured, kit OK and available now, who is assigned to another open incident, or whose role is taken, and an alert on a merged or closed incident;
  - first acceptance per role wins: a partial unique index on `assignments (incident_id, role) WHERE released_at IS NULL`; a responder holds at most one open assignment (`op.accept_alert` locks the incident, then the responder, before its checks);
  - `op.claim_due_dispatch` returns the claimed batch most urgent first (severity, then wake time);
  - each delivery attempt is claimed once (`UNIQUE (alert_id, purpose, channel)`) before the provider call;
  - a forwarded facility's SMS or call is claimed only while its forward is open (not acknowledged or timed out) and its incident is not closed or merged; the claim share-locks the incident, so a claim racing a closure waits for it and then sees it (`op.begin_forward_attempt`, migration 0022; CodeRabbit PR #11);
  - an incident cannot close or merge while an alert is pending; closure and merge wake dispatch so stand-downs are delivered;
  - while dispatch is suspended no alert is issued, no forward recorded and no alert attempt claimed (each share-locks the control row a suspension locks exclusively); an alert attempt is claimed only for a pending alert;
  - every dispatch fact and its event are written together by one function; the generic append refuses dispatch event types; the dispatch logic version is on alerts, acceptances, declines, lapses, stand-downs, forwards and dispatch escalations, not on acknowledgements, forward timeouts or caller notices;
  - one dispatch control row; the control log (the reportable-events register) is append-only (`SA403` even for the owner); resuming always carries a staff identity; every change is an audit entry without an incident (ADR-0026).
- **Operational:**
  - 10-digit reference with a `CHECK` and `UNIQUE`.
  - A logic version must be `<code>+sha256:<12–64 hex>` (the same rule as the ledger); a merge target must exist, differ from the incident and be open.
  - Strict `screen.answered` payloads (codes only) and sealed identifiers (M1, ADR-0021).
  - Lifecycle table.
  - Idempotency and per-incident sequence uniqueness in `op.event_keys`.
  - Monthly partitions with a monitored DEFAULT partition.
  - The audit outbox carries only typed fields and `payload_sha256`.
- **Audit:** typed columns only; `logic_version` matches `^[a-z0-9][a-z0-9._-]{0,63}\+sha256:[0-9a-f]{12,64}$`, so no free text can enter the ledger; the hash is computed in SQL; appends are serialised by an advisory lock.
- **Analytic:** `consent_status` must be in (`consented`, `guardian_permission`, `assent`, `waiver`), and a waiver carries a reason (`deceased`, `untraceable`, `unable_to_understand`). `destination`, `first_action_code` and `status_24_48h` are codes (no free text). Inserting a study ID into `suppression_ledger` deletes its MEDS record, and a suppressed ID can never be inserted again (SA403).
- **Linkage:** resolution only for `withdrawal`, `data_subject_request`, `payment_dispute`, `lawful_rec_uncst_request`.
- **Media:** `destroy_by` is no later than the UTC creation date + 12 calendar months.
