-- Tool 1 projection (M1 Task 1; plan docs/superpowers/plans/2026-11-12-m1-intake.md).
--
-- Every screen answer becomes a screen.answered event whose payload is
-- strictly {screen, field, value[, identifier_id]}. The value is a code (a
-- lower-case letter first), so a phone number or free text can never reach a
-- payload or the audit hash (hard constraints 5 and 8); identifiers are sealed
-- in op.incident_identifiers and referenced by identifier_id. These checks
-- mirror internal/incident/tool1.go exactly: whatever Apply refuses, the
-- write path refuses, so every stored history folds.

ALTER TABLE op.incidents
    ADD COLUMN language         text,
    ADD COLUMN danger           text,
    ADD COLUMN category         text,
    ADD COLUMN age_band         text,
    ADD COLUMN specific_danger  text,
    ADD COLUMN ward             text,
    ADD COLUMN landmark         text,
    ADD COLUMN last_screen      int  NOT NULL DEFAULT 0 CHECK (last_screen BETWEEN 0 AND 8),
    -- UTM 36N (EPSG:32636) has no false northing: rows are negative south of
    -- the equator.
    ADD COLUMN zone_col         int,
    ADD COLUMN zone_row         int,
    ADD COLUMN location_quality text CHECK (location_quality IN ('landmark', 'fuzzy', 'ward')),
    ADD CONSTRAINT incidents_zone_complete
        CHECK ((zone_col IS NULL) = (zone_row IS NULL) AND (zone_col IS NULL) = (location_quality IS NULL));

INSERT INTO op.incident_transitions (from_state, event_type, to_state) VALUES ('*', 'session.resumed', NULL);

-- VOLATILE so the planner never folds (and raises) it early.
CREATE FUNCTION op.reject(p_why text) RETURNS void
LANGUAGE plpgsql VOLATILE
SET search_path = op, pg_temp
AS $$ BEGIN RAISE EXCEPTION 'illegal transition: %', p_why USING ERRCODE = 'SA422'; END $$;

CREATE FUNCTION op.check_screen_answer(p jsonb) RETURNS void
LANGUAGE plpgsql IMMUTABLE
SET search_path = op, pg_temp
AS $$
DECLARE
    v_screen   text := p ->> 'screen';
    v_field    text := p ->> 'field';
    v_value    text := p ->> 'value';
    v_provided boolean;
    v_other    boolean;
BEGIN
    IF jsonb_typeof(p) IS DISTINCT FROM 'object'
       OR EXISTS (SELECT 1 FROM jsonb_object_keys(p) k WHERE k NOT IN ('screen', 'field', 'value', 'identifier_id')) THEN
        PERFORM op.reject('screen.answered: unexpected key');
    END IF;
    IF jsonb_typeof(p -> 'screen') IS DISTINCT FROM 'number' OR v_screen !~ '^[1-8]$' THEN
        PERFORM op.reject('screen.answered: screen must be 1-8');
    END IF;
    IF jsonb_typeof(p -> 'field') IS DISTINCT FROM 'string'
       OR v_field IS DISTINCT FROM (ARRAY['language', 'danger', 'category', 'age_band', 'specific_danger',
                                          'ward', 'landmark', 'callback'])[v_screen::int] THEN
        PERFORM op.reject('screen.answered: field does not match screen');
    END IF;
    IF jsonb_typeof(p -> 'value') IS DISTINCT FROM 'string' OR v_value !~ '^[a-z][a-z0-9_.-]{0,63}$' THEN
        PERFORM op.reject('screen.answered: value must be a code');
    END IF;
    IF p ? 'identifier_id' AND (jsonb_typeof(p -> 'identifier_id') IS DISTINCT FROM 'string'
       OR (p ->> 'identifier_id') !~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$') THEN
        PERFORM op.reject('screen.answered: identifier_id must be a UUID');
    END IF;
    v_provided := v_field = 'callback' AND v_value = 'provided';
    v_other    := v_field = 'landmark' AND v_value = 'other';
    IF v_provided AND NOT p ? 'identifier_id' THEN
        PERFORM op.reject('screen.answered: a provided callback number needs identifier_id');
    END IF;
    IF p ? 'identifier_id' AND NOT (v_provided OR v_other) THEN
        PERFORM op.reject('screen.answered: identifier_id is allowed only for a provided callback or a free-text landmark');
    END IF;
END
$$;

CREATE FUNCTION op.check_session_resumed(p jsonb) RETURNS void
LANGUAGE plpgsql IMMUTABLE
SET search_path = op, pg_temp
AS $$
BEGIN
    IF jsonb_typeof(p) IS DISTINCT FROM 'object'
       OR EXISTS (SELECT 1 FROM jsonb_object_keys(p) k WHERE k <> 'channel')
       OR jsonb_typeof(p -> 'channel') IS DISTINCT FROM 'string'
       OR (p ->> 'channel') NOT IN ('ussd_at', 'ussd_ucc', 'ivr') THEN
        PERFORM op.reject('session.resumed needs a known channel');
    END IF;
END
$$;

CREATE FUNCTION op.check_dispatch_location(p jsonb) RETURNS void
LANGUAGE plpgsql IMMUTABLE
SET search_path = op, pg_temp
AS $$
BEGIN
    IF jsonb_typeof(p) IS DISTINCT FROM 'object' THEN
        PERFORM op.reject('dispatch_ready: payload must be an object');
    END IF;
    IF NOT (p ? 'zone' OR p ? 'location_quality') THEN
        RETURN;
    END IF;
    IF NOT p ? 'zone' OR jsonb_typeof(p -> 'location_quality') IS DISTINCT FROM 'string'
       OR (p ->> 'location_quality') NOT IN ('landmark', 'fuzzy', 'ward') THEN
        PERFORM op.reject('dispatch_ready: zone and a known location_quality go together');
    END IF;
    IF jsonb_typeof(p -> 'zone') IS DISTINCT FROM 'object'
       OR EXISTS (SELECT 1 FROM jsonb_object_keys(p -> 'zone') k WHERE k NOT IN ('col', 'row'))
       OR jsonb_typeof(p #> '{zone,col}') IS DISTINCT FROM 'number' OR (p #>> '{zone,col}') !~ '^(0|-?[1-9][0-9]{0,6})$'
       OR jsonb_typeof(p #> '{zone,row}') IS DISTINCT FROM 'number' OR (p #>> '{zone,row}') !~ '^(0|-?[1-9][0-9]{0,6})$' THEN
        PERFORM op.reject('dispatch_ready: zone col and row must be integers');
    END IF;
END
$$;

-- Append one event, update the projection and queue its audit record, in
-- the caller's transaction. Idempotent on (incident, idem_key).
CREATE OR REPLACE FUNCTION op.append_incident_event(
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
    v_target text;
    v_field text;
    v_zone  boolean := false;
BEGIN
    IF p_type = 'session.started' THEN
        RAISE EXCEPTION 'illegal transition: session.started is written only by open_incident'
            USING ERRCODE = 'SA422';
    END IF;
    -- The same format audit.entries enforces, so a value the ledger would
    -- reject (or free text) never enters the event log or the outbox.
    IF p_logic_version !~ '^[a-z0-9][a-z0-9._-]{0,63}\+sha256:[0-9a-f]{12,64}$' THEN
        RAISE EXCEPTION 'illegal transition: logic version is not an approved identifier'
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
    ELSIF p_type = 'screen.answered' THEN
        PERFORM op.check_screen_answer(p_payload);
    ELSIF p_type = 'session.resumed' THEN
        PERFORM op.check_session_resumed(p_payload);
    ELSIF p_type = 'incident.dispatch_ready' THEN
        PERFORM op.check_dispatch_location(p_payload);
    ELSIF p_type = 'incident.merged' THEN
        IF coalesce(p_payload ->> 'into', '')
           !~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$' THEN
            RAISE EXCEPTION 'illegal transition: merge needs a target incident ID' USING ERRCODE = 'SA422';
        END IF;
        v_into := (p_payload ->> 'into')::uuid;
        IF v_into = p_incident THEN
            RAISE EXCEPTION 'illegal transition: an incident cannot merge into itself' USING ERRCODE = 'SA422';
        END IF;
        -- FOR SHARE: the target cannot close or merge before this commits,
        -- and two incidents merging into each other deadlock (one aborts)
        -- instead of both leaving the workflow.
        SELECT i.state INTO v_target FROM op.incidents i WHERE i.id = v_into FOR SHARE;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'unknown merge target %', v_into USING ERRCODE = 'SA404';
        END IF;
        IF v_target IN ('closed', 'merged') THEN
            RAISE EXCEPTION 'illegal transition: merge target is in terminal state %', v_target
                USING ERRCODE = 'SA422';
        END IF;
    END IF;

    IF p_type = 'screen.answered' THEN
        v_field := p_payload ->> 'field';
    ELSIF p_type = 'incident.dispatch_ready' THEN
        v_zone := p_payload ? 'zone';
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
        language        = CASE WHEN v_field = 'language'        THEN p_payload ->> 'value' ELSE i.language END,
        danger          = CASE WHEN v_field = 'danger'          THEN p_payload ->> 'value' ELSE i.danger END,
        category        = CASE WHEN v_field = 'category'        THEN p_payload ->> 'value' ELSE i.category END,
        age_band        = CASE WHEN v_field = 'age_band'        THEN p_payload ->> 'value' ELSE i.age_band END,
        specific_danger = CASE WHEN v_field = 'specific_danger' THEN p_payload ->> 'value' ELSE i.specific_danger END,
        ward            = CASE WHEN v_field = 'ward'            THEN p_payload ->> 'value' ELSE i.ward END,
        landmark        = CASE WHEN v_field = 'landmark'        THEN p_payload ->> 'value' ELSE i.landmark END,
        last_screen     = CASE WHEN p_type = 'screen.answered'
                               THEN greatest(i.last_screen, (p_payload ->> 'screen')::int) ELSE i.last_screen END,
        zone_col        = CASE WHEN v_zone THEN (p_payload #>> '{zone,col}')::int ELSE i.zone_col END,
        zone_row        = CASE WHEN v_zone THEN (p_payload #>> '{zone,row}')::int ELSE i.zone_row END,
        location_quality = CASE WHEN v_zone THEN p_payload ->> 'location_quality' ELSE i.location_quality END,
        updated_at    = v_at
    WHERE i.id = p_incident;

    PERFORM op.enqueue_audit(v_ev, v_at, p_type, p_incident, p_staff_id, p_logic_version, p_payload);

    RETURN QUERY SELECT v_ev, v_seq, v_at, coalesce(v_to, v_inc.state), false;
END
$$;


-- New functions above are revoked from PUBLIC by the role-level default
-- (0004); the replaced append function keeps its existing grants.
