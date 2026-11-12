-- The single write path (Build Prompt §7 "One write path"; spec §6.2;
-- ADR-0003). Application roles hold no INSERT/UPDATE/DELETE on incidents,
-- incident_events, event_keys or outbox; these functions are the only way
-- in. They pin search_path (SECURITY DEFINER hygiene) and raise:
--   SA404 unknown incident · SA409 version conflict · SA422 illegal transition
--   (message prefix "severity lowered" for a severity downgrade).

CREATE FUNCTION op.severity_rank(p text) RETURNS int
LANGUAGE sql IMMUTABLE PARALLEL SAFE
AS $$ SELECT CASE p WHEN 'red' THEN 3 WHEN 'yellow' THEN 2 WHEN 'green' THEN 1 ELSE 0 END $$;

-- Queue the audit record for an event. The payload carries typed fields and
-- the SHA-256 of the event payload, never the payload itself (hard
-- constraint 8). entry_id = outbox id, which makes ledger appends idempotent.
CREATE FUNCTION op.enqueue_audit(
    p_event_id uuid, p_recorded_at timestamptz, p_type text, p_incident uuid,
    p_staff uuid, p_logic text, p_payload jsonb
) RETURNS void
LANGUAGE plpgsql VOLATILE
SET search_path = op, pg_temp
AS $$
DECLARE
    v_id uuid := uuidv7();
BEGIN
    INSERT INTO op.outbox (id, topic, payload)
    VALUES (v_id, 'audit', jsonb_build_object(
        'entry_id',       v_id,
        'event_id',       p_event_id,
        'occurred_at',    p_recorded_at,
        'event_type',     p_type,
        'incident_id',    p_incident,
        'staff_id',       p_staff,
        'logic_version',  p_logic,
        'payload_sha256', encode(sha256(convert_to(p_payload::text, 'UTF8')), 'hex')));
END
$$;

-- Create an incident at the first USSD/IVR screen (Build Prompt §10.1): the
-- incident, its session and the session.started event (T1, database clock).
-- A retried or concurrent first callback for the same provider session
-- returns the existing incident with created = false.
CREATE FUNCTION op.open_incident(
    p_reference text, p_channel text, p_session_id uuid, p_provider_session_id text,
    p_phone_ct bytea, p_phone_key_id text, p_phone_mac bytea, p_phone_mac_key_id text,
    p_network_code text, p_idem_key text
) RETURNS TABLE (incident_id uuid, reference text, event_id uuid, t1 timestamptz, created boolean)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_inc        uuid;
    v_ev         uuid := uuidv7();
    v_at         timestamptz;
    v_payload    jsonb := jsonb_build_object('reference', p_reference, 'channel', p_channel);
    v_constraint text;
BEGIN
    SELECT s.incident_id INTO v_inc FROM op.sessions s
     WHERE s.channel = p_channel AND s.provider_session_id = p_provider_session_id;
    IF FOUND THEN
        RETURN QUERY SELECT i.id, i.reference, k.event_id, i.t1, false
                       FROM op.incidents i JOIN op.event_keys k ON k.incident_id = i.id AND k.seq = 1
                      WHERE i.id = v_inc;
        RETURN;
    END IF;

    v_inc := uuidv7();
    v_at := clock_timestamp();
    BEGIN
        INSERT INTO op.incidents (id, reference, channel, state, version, t1, created_at, updated_at)
        VALUES (v_inc, p_reference, p_channel, 'intake', 1, v_at, v_at, v_at);
        INSERT INTO op.sessions (id, incident_id, channel, provider_session_id, phone_ct, phone_key_id,
                                 phone_mac, phone_mac_key_id, network_code, started_at)
        VALUES (p_session_id, v_inc, p_channel, p_provider_session_id, p_phone_ct, p_phone_key_id,
                p_phone_mac, p_phone_mac_key_id, p_network_code, v_at);
    EXCEPTION WHEN unique_violation THEN
        GET STACKED DIAGNOSTICS v_constraint = CONSTRAINT_NAME;
        IF v_constraint = 'sessions_provider_key' THEN
            -- A concurrent duplicate callback committed first: return its incident.
            RETURN QUERY SELECT i.id, i.reference, k.event_id, i.t1, false
                           FROM op.sessions s
                           JOIN op.incidents i ON i.id = s.incident_id
                           JOIN op.event_keys k ON k.incident_id = i.id AND k.seq = 1
                          WHERE s.channel = p_channel AND s.provider_session_id = p_provider_session_id;
            RETURN;
        END IF;
        RAISE; -- incidents_reference_key: caller retries with a new reference
    END;

    INSERT INTO op.incident_events (id, incident_id, seq, type, recorded_at, actor_kind, payload, idem_key)
    VALUES (v_ev, v_inc, 1, 'session.started', v_at, 'caller', v_payload, p_idem_key);
    INSERT INTO op.event_keys (incident_id, idem_key, event_id, seq, recorded_at)
    VALUES (v_inc, p_idem_key, v_ev, 1, v_at);
    PERFORM op.enqueue_audit(v_ev, v_at, 'session.started', v_inc, NULL, NULL, v_payload);

    RETURN QUERY SELECT v_inc, p_reference, v_ev, v_at, true;
END
$$;

-- Append one event, update the projection and queue its audit record, in
-- the caller's transaction. Idempotent on (incident, idem_key).
CREATE FUNCTION op.append_incident_event(
    p_incident uuid, p_expected_version bigint, p_type text, p_payload jsonb,
    p_actor_kind text, p_staff_id uuid, p_idem_key text, p_logic_version text
) RETURNS TABLE (event_id uuid, seq bigint, recorded_at timestamptz, new_state text, duplicate boolean)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_inc   op.incidents%ROWTYPE;
    v_key   op.event_keys%ROWTYPE;
    v_to    text;
    v_ev    uuid := uuidv7();
    v_at    timestamptz;
    v_seq   bigint;
    v_sev   text;
    v_flag  text;
    v_into  uuid;
BEGIN
    IF p_type = 'session.started' THEN
        RAISE EXCEPTION 'illegal transition: session.started is written only by open_incident'
            USING ERRCODE = 'SA422';
    END IF;

    -- Lock first: a concurrent duplicate waits here, then finds the key.
    SELECT * INTO v_inc FROM op.incidents WHERE id = p_incident FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'unknown incident %', p_incident USING ERRCODE = 'SA404';
    END IF;

    SELECT * INTO v_key FROM op.event_keys k WHERE k.incident_id = p_incident AND k.idem_key = p_idem_key;
    IF FOUND THEN
        RETURN QUERY SELECT v_key.event_id, v_key.seq, v_key.recorded_at, v_inc.state, true;
        RETURN;
    END IF;

    IF p_expected_version IS NOT NULL AND p_expected_version <> 0 AND p_expected_version <> v_inc.version THEN
        RAISE EXCEPTION 'version conflict: expected %, have %', p_expected_version, v_inc.version
            USING ERRCODE = 'SA409';
    END IF;

    IF v_inc.state IN ('closed', 'merged') THEN
        RAISE EXCEPTION 'illegal transition: % in terminal state %', p_type, v_inc.state USING ERRCODE = 'SA422';
    END IF;
    SELECT t.to_state INTO v_to FROM op.incident_transitions t
     WHERE t.from_state = v_inc.state AND t.event_type = p_type;
    IF NOT FOUND THEN
        SELECT t.to_state INTO v_to FROM op.incident_transitions t
         WHERE t.from_state = '*' AND t.event_type = p_type;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'illegal transition: % in state %', p_type, v_inc.state USING ERRCODE = 'SA422';
        END IF;
    END IF;

    IF p_type = 'incident.classified' THEN
        v_sev := p_payload ->> 'severity';
        IF op.severity_rank(v_sev) = 0 THEN
            RAISE EXCEPTION 'illegal transition: classification needs a valid severity' USING ERRCODE = 'SA422';
        END IF;
        IF op.severity_rank(v_sev) < op.severity_rank(v_inc.severity) THEN
            RAISE EXCEPTION 'severity lowered: % -> %', v_inc.severity, v_sev USING ERRCODE = 'SA422';
        END IF;
    ELSIF p_type = 'incident.flagged' THEN
        v_flag := p_payload ->> 'flag';
        IF coalesce(v_flag, '') = '' THEN
            RAISE EXCEPTION 'illegal transition: flag event needs a flag' USING ERRCODE = 'SA422';
        END IF;
    ELSIF p_type = 'incident.merged' THEN
        IF coalesce(p_payload ->> 'into', '') !~ '^[0-9a-fA-F-]{36}$' THEN
            RAISE EXCEPTION 'illegal transition: merge needs a target' USING ERRCODE = 'SA422';
        END IF;
        v_into := (p_payload ->> 'into')::uuid;
    END IF;

    v_at := clock_timestamp();
    v_seq := v_inc.version + 1;

    INSERT INTO op.incident_events (id, incident_id, seq, type, recorded_at, actor_kind, staff_id,
                                    payload, idem_key, logic_version)
    VALUES (v_ev, p_incident, v_seq, p_type, v_at, p_actor_kind, p_staff_id,
            p_payload, p_idem_key, p_logic_version);
    INSERT INTO op.event_keys (incident_id, idem_key, event_id, seq, recorded_at)
    VALUES (p_incident, p_idem_key, v_ev, v_seq, v_at);

    -- Projection update. Mirrors internal/incident/fold.go Apply exactly;
    -- `sautictl verify-projections` proves it.
    UPDATE op.incidents i SET
        state         = coalesce(v_to, i.state),
        version       = v_seq,
        severity      = CASE WHEN p_type = 'incident.classified' THEN v_sev ELSE i.severity END,
        logic_version = CASE WHEN p_type = 'incident.classified' THEN p_logic_version ELSE i.logic_version END,
        t2 = CASE WHEN p_type = 'incident.dispatch_ready' THEN coalesce(i.t2, v_at) ELSE i.t2 END,
        t3 = CASE WHEN p_type = 'alert.issued'            THEN coalesce(i.t3, v_at) ELSE i.t3 END,
        t4 = CASE WHEN p_type = 'responder.accepted'      THEN coalesce(i.t4, v_at) ELSE i.t4 END,
        t5 = CASE WHEN p_type = 'responder.arrived'       THEN coalesce(i.t5, v_at) ELSE i.t5 END,
        t6 = CASE WHEN p_type = 'responder.departed'      THEN coalesce(i.t6, v_at) ELSE i.t6 END,
        t7 = CASE WHEN p_type = 'responder.at_facility'   THEN coalesce(i.t7, v_at) ELSE i.t7 END,
        escalated     = i.escalated OR p_type = 'incident.escalated',
        flags         = CASE WHEN p_type = 'incident.flagged' AND NOT (v_flag = ANY (i.flags))
                             THEN array_append(i.flags, v_flag) ELSE i.flags END,
        merged_into   = CASE WHEN p_type = 'incident.merged' THEN v_into ELSE i.merged_into END,
        updated_at    = v_at
    WHERE i.id = p_incident;

    PERFORM op.enqueue_audit(v_ev, v_at, p_type, p_incident, p_staff_id, p_logic_version, p_payload);

    RETURN QUERY SELECT v_ev, v_seq, v_at, coalesce(v_to, v_inc.state), false;
END
$$;

-- Outbox relay functions: claim pending rows (locked until the caller's
-- transaction ends) and mark them delivered.
CREATE FUNCTION op.claim_outbox(p_topic text, p_limit int) RETURNS SETOF op.outbox
LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
    SELECT * FROM op.outbox
     WHERE topic = p_topic AND delivered_at IS NULL
     ORDER BY id
     LIMIT p_limit
       FOR UPDATE SKIP LOCKED
$$;

CREATE FUNCTION op.mark_outbox_delivered(p_ids uuid[]) RETURNS int
LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
    WITH u AS (
        UPDATE op.outbox SET delivered_at = clock_timestamp()
         WHERE id = ANY (p_ids) AND delivered_at IS NULL
        RETURNING 1)
    SELECT count(*)::int FROM u
$$;
