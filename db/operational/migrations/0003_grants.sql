-- Least-privilege grants for the operational store (spec §6.1; ADR-0006).
-- Functions default to EXECUTE for PUBLIC in PostgreSQL, so revoke first.

REVOKE ALL ON ALL TABLES IN SCHEMA op FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA op FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA op REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;

GRANT USAGE ON SCHEMA op TO op_intake, op_ivr, op_dispatch, op_console, op_payments,
                            op_relay, op_pseudonymiser, op_retention, op_readonly;

-- intake-gw: the write functions, reads it needs for resume and replies.
GRANT EXECUTE ON FUNCTION
    op.open_incident(text, text, uuid, text, bytea, text, bytea, text, text, text),
    op.append_incident_event(uuid, bigint, text, jsonb, text, uuid, text, text)
TO op_intake;
GRANT SELECT ON op.incidents, op.incident_events, op.sessions TO op_intake;
GRANT SELECT, INSERT ON op.ussd_steps TO op_intake;

-- outbox-relay: the two outbox functions and nothing else.
GRANT EXECUTE ON FUNCTION op.claim_outbox(text, int), op.mark_outbox_delivered(uuid[]) TO op_relay;

-- Read-only verification and monitoring: no access to sessions (identifiers).
GRANT SELECT ON op.incidents, op.incident_events, op.event_keys, op.incident_transitions,
                op.outbox, op.config_versions, op.key_periods TO op_readonly;
GRANT EXECUTE ON FUNCTION op.default_partition_rows() TO op_readonly, op_relay;
