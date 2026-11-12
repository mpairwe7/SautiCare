-- Resume (M1 Task 8; Engineering Guide §4; spec D4, ADR-0009).
--
-- A caller who redials within the resume window, on either channel, resumes
-- the same incident at the first unanswered screen. The redial gets its own
-- session row (its own sealed number); the incident keeps its reference and
-- T1, and gains a session.resumed event. A retried first callback of the same
-- provider session returns the incident with created = false.

CREATE FUNCTION op.resume_session(
    p_incident uuid, p_channel text, p_session_id uuid, p_provider_session_id text,
    p_phone_ct bytea, p_phone_key_id text, p_phone_mac bytea, p_phone_mac_key_id text,
    p_network_code text, p_idem_key text
) RETURNS TABLE (incident_id uuid, reference text, event_id uuid, t1 timestamptz, created boolean)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_inc        uuid;
    v_ev         uuid;
    v_constraint text;
BEGIN
    SELECT s.incident_id INTO v_inc FROM op.sessions s
     WHERE s.channel = p_channel AND s.provider_session_id = p_provider_session_id;
    IF FOUND THEN
        RETURN QUERY SELECT i.id, i.reference, k.event_id, i.t1, false
                       FROM op.incidents i
                       LEFT JOIN op.event_keys k ON k.incident_id = i.id AND k.idem_key = p_idem_key
                      WHERE i.id = v_inc;
        RETURN;
    END IF;
    BEGIN
        INSERT INTO op.sessions (id, incident_id, channel, provider_session_id, phone_ct, phone_key_id,
                                 phone_mac, phone_mac_key_id, network_code)
        VALUES (p_session_id, p_incident, p_channel, p_provider_session_id, p_phone_ct, p_phone_key_id,
                p_phone_mac, p_phone_mac_key_id, p_network_code);
    EXCEPTION WHEN unique_violation THEN
        GET STACKED DIAGNOSTICS v_constraint = CONSTRAINT_NAME;
        IF v_constraint <> 'sessions_provider_key' THEN
            RAISE;
        END IF;
        -- A concurrent duplicate first callback committed first.
        RETURN QUERY SELECT i.id, i.reference, k.event_id, i.t1, false
                       FROM op.sessions s JOIN op.incidents i ON i.id = s.incident_id
                       LEFT JOIN op.event_keys k ON k.incident_id = i.id AND k.idem_key = p_idem_key
                      WHERE s.channel = p_channel AND s.provider_session_id = p_provider_session_id;
        RETURN;
    WHEN foreign_key_violation THEN
        RAISE EXCEPTION 'unknown incident %', p_incident USING ERRCODE = 'SA404';
    END;
    SELECT a.event_id INTO v_ev
      FROM op.append_incident_event(p_incident, 0, 'session.resumed', jsonb_build_object('channel', p_channel),
                                    'caller', NULL, p_idem_key, NULL) AS a;
    RETURN QUERY SELECT i.id, i.reference, v_ev, i.t1, true FROM op.incidents i WHERE i.id = p_incident;
END
$$;

GRANT EXECUTE ON FUNCTION op.resume_session(uuid, text, uuid, text, bytea, text, bytea, text, text, text) TO op_intake, op_ivr;
