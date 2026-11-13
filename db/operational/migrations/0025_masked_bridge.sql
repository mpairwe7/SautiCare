-- The masked bridge (M2 Part C Task 13; Build Prompt §10.5; hard
-- constraints 5 and 8; ADR-0028).
--
-- A responder calls the bridge number: ivr-controller recognises them by
-- their number's MAC and their open assignments, and bridges them to the
-- incident's caller, showing Sauti's number. A caller calls the same number:
-- they are recognised by the MAC of the number they called Sauti from and
-- bridged to the responder assigned to their open incident. Every bridge
-- call is logged — when it started, was answered and ended, and its result —
-- with identifiers only. Nothing is recorded, and no number is stored here.

CREATE TABLE op.bridge_calls (
    id           uuid        PRIMARY KEY,
    incident_id  uuid        NOT NULL REFERENCES op.incidents (id),
    initiated_by text        NOT NULL CHECK (initiated_by IN ('vht', 'transporter', 'caller')),
    started_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    answered_at  timestamptz,
    ended_at     timestamptz,
    result       text        CHECK (result IN ('connected', 'no_answer', 'failed')),
    CHECK ((result IS NULL) = (ended_at IS NULL)),
    CHECK (result IS DISTINCT FROM 'connected' OR answered_at IS NOT NULL)
);
CREATE INDEX bridge_calls_incident ON op.bridge_calls (incident_id, started_at);

-- The open assignments a responder holds (the incidents they may bridge to).
CREATE FUNCTION op.bridge_targets_for_responder(p_responder uuid)
RETURNS TABLE (incident_id uuid, reference text, role text, responder_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
    SELECT s.incident_id, i.reference, s.role, s.responder_id
      FROM op.assignments s JOIN op.incidents i ON i.id = s.incident_id
     WHERE s.responder_id = p_responder AND s.released_at IS NULL AND i.state NOT IN ('closed', 'merged')
     ORDER BY s.accepted_at DESC
$$;

-- The responders assigned to the open incidents a caller's number reported
-- (by the MAC of the number of any of the incident's sessions).
CREATE FUNCTION op.bridge_targets_for_caller(p_macs bytea[])
RETURNS TABLE (incident_id uuid, reference text, role text, responder_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
    SELECT s.incident_id, i.reference, s.role, s.responder_id
      FROM op.assignments s JOIN op.incidents i ON i.id = s.incident_id
     WHERE s.released_at IS NULL AND i.state NOT IN ('closed', 'merged')
       AND EXISTS (SELECT 1 FROM op.sessions x WHERE x.incident_id = i.id AND x.phone_mac = ANY (p_macs))
     ORDER BY s.accepted_at DESC
$$;

-- A bridge call begins: only on an incident someone holds an open
-- assignment on.
CREATE FUNCTION op.begin_bridge_call(p_incident uuid, p_initiated_by text) RETURNS uuid
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_id uuid := uuidv7();
BEGIN
    IF NOT EXISTS (SELECT 1 FROM op.assignments s JOIN op.incidents i ON i.id = s.incident_id
                    WHERE s.incident_id = p_incident AND s.released_at IS NULL AND i.state NOT IN ('closed', 'merged')) THEN
        PERFORM op.reject('a bridge call needs an open assignment on the incident');
    END IF;
    INSERT INTO op.bridge_calls (id, incident_id, initiated_by) VALUES (v_id, p_incident, p_initiated_by);
    RETURN v_id;
END
$$;

CREATE FUNCTION op.bridge_call_answered(p_id uuid) RETURNS void
LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
    UPDATE op.bridge_calls b SET answered_at = clock_timestamp() WHERE b.id = p_id AND b.answered_at IS NULL AND b.ended_at IS NULL
$$;

-- A bridge call ends once; a later end changes nothing. "connected" needs an
-- answer.
CREATE FUNCTION op.finish_bridge_call(p_id uuid, p_result text) RETURNS boolean
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
BEGIN
    IF p_result = 'connected' AND EXISTS (SELECT 1 FROM op.bridge_calls b WHERE b.id = p_id AND b.answered_at IS NULL AND b.ended_at IS NULL) THEN
        PERFORM op.reject('a bridge call is connected only once answered');
    END IF;
    UPDATE op.bridge_calls b SET ended_at = clock_timestamp(), result = p_result WHERE b.id = p_id AND b.ended_at IS NULL;
    RETURN FOUND;
END
$$;

GRANT EXECUTE ON FUNCTION op.bridge_targets_for_responder(uuid), op.bridge_targets_for_caller(bytea[]),
    op.begin_bridge_call(uuid, text), op.bridge_call_answered(uuid), op.finish_bridge_call(uuid, text),
    op.caller_contact(uuid) TO op_ivr;
GRANT SELECT ON op.bridge_calls TO op_readonly;
