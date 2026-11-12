-- ivr-controller (M1 Part C; Build Prompt §10.2): op_ivr runs Tool 1 on the
-- voice line through the same write path as intake-gw. It already holds the
-- gazetteer reads and op.match_landmark (0007), op.store_identifier (0008)
-- and op.resume_session (0009). It never touches op.ussd_steps.
GRANT EXECUTE ON FUNCTION
    op.open_incident(text, text, uuid, text, bytea, text, bytea, text, text, text),
    op.append_incident_event(uuid, bigint, text, jsonb, text, uuid, text, text)
TO op_ivr;
GRANT SELECT ON op.incidents, op.incident_events, op.sessions TO op_ivr;
