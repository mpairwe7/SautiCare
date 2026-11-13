-- Salted audit payload hash (review finding I3; ADR-0005).
--
-- The audit ledger stores a hash of each event payload, never the payload.
-- An unsalted SHA-256 of a payload that holds a phone number is reversible by
-- enumerating ~10^9 candidates, and the five-year ledger cannot be shredded.
-- Each event now carries a 16-byte random salt, kept here in the operational
-- store (destroyed with its retention period), and the ledger records
-- sha256(payload_salt ‖ payload::text). Holders of the ledger alone cannot
-- reverse it; anyone holding both stores can still prove the link.

ALTER TABLE op.incident_events
    ADD COLUMN payload_salt bytea NOT NULL DEFAULT uuid_send(gen_random_uuid())
    CHECK (octet_length(payload_salt) = 16);

CREATE OR REPLACE FUNCTION op.enqueue_audit(
    p_event_id uuid, p_recorded_at timestamptz, p_type text, p_incident uuid,
    p_staff uuid, p_logic text, p_payload jsonb
) RETURNS void
LANGUAGE plpgsql VOLATILE
SET search_path = op, pg_temp
AS $$
DECLARE
    v_id   uuid := uuidv7();
    v_salt bytea;
BEGIN
    SELECT e.payload_salt INTO v_salt FROM op.incident_events e
     WHERE e.id = p_event_id AND e.recorded_at = p_recorded_at;
    IF v_salt IS NULL THEN
        RAISE EXCEPTION 'enqueue_audit: event % not found', p_event_id;
    END IF;
    INSERT INTO op.outbox (id, topic, payload)
    VALUES (v_id, 'audit', jsonb_build_object(
        'entry_id',       v_id,
        'event_id',       p_event_id,
        'occurred_at',    p_recorded_at,
        'event_type',     p_type,
        'incident_id',    p_incident,
        'staff_id',       p_staff,
        'logic_version',  p_logic,
        'payload_sha256', encode(sha256(v_salt || convert_to(p_payload::text, 'UTF8')), 'hex')));
END
$$;
