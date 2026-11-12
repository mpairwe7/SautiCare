-- Deduplication: merge, split and unmerge (M1 Part B, Task 12; Build Prompt
-- §10.3; ADR-0022).
--
-- Calls about one emergency are merged into a survivor; the earliest call
-- gives T1. The survivor records each merged call's T1 (incident.merge_absorbed,
-- kept in absorbed: {incident id: t1}); its T1 is the earliest of its own first
-- call (created_at, set with T1 by op.open_incident) and those. A coordinator
-- splits a wrong merge (incident.split, staff only): the call leaves absorbed
-- and T1 is recomputed. The member, merged at dispatch, is revived with
-- incident.unmerged (merged -> dispatching). A merged incident accepts nothing
-- else. Mirrors internal/incident (fold.go, tool1.go) exactly.

ALTER TABLE op.incidents ADD COLUMN absorbed jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(absorbed) = 'object');

INSERT INTO op.incident_transitions (from_state, event_type, to_state) VALUES
    ('*',      'incident.merge_absorbed', NULL),
    ('*',      'incident.split',          NULL),
    ('merged', 'incident.unmerged',       'dispatching'),
    -- Dedup happens at T2: a merged call's own caller is still answering
    -- (screen 8), and every answer is persisted.
    ('merged', 'screen.answered',         NULL);

CREATE FUNCTION op.check_from(p jsonb) RETURNS void
LANGUAGE plpgsql IMMUTABLE
SET search_path = op, pg_temp
AS $$
BEGIN
    IF jsonb_typeof(p) IS DISTINCT FROM 'object'
       OR EXISTS (SELECT 1 FROM jsonb_object_keys(p) k WHERE k <> 'from')
       OR jsonb_typeof(p -> 'from') IS DISTINCT FROM 'string'
       OR (p ->> 'from') !~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$' THEN
        PERFORM op.reject('payload needs exactly a "from" incident ID');
    END IF;
END
$$;

CREATE FUNCTION op.check_merge_absorbed(p jsonb, p_incident uuid, p_absorbed jsonb) RETURNS void
LANGUAGE plpgsql IMMUTABLE
SET search_path = op, pg_temp
AS $$
BEGIN
    IF jsonb_typeof(p) IS DISTINCT FROM 'object'
       OR EXISTS (SELECT 1 FROM jsonb_object_keys(p) k WHERE k NOT IN ('from', 't1'))
       OR jsonb_typeof(p -> 'from') IS DISTINCT FROM 'string'
       OR (p ->> 'from') !~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
       OR jsonb_typeof(p -> 't1') IS DISTINCT FROM 'string'
       OR (p ->> 't1') !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,6})?(Z|[+-][0-9]{2}:[0-9]{2})$' THEN
        PERFORM op.reject('merge_absorbed needs exactly "from" (incident ID) and "t1" (timestamp)');
    END IF;
    IF (p ->> 'from')::uuid = p_incident OR p_absorbed ? ((p ->> 'from')::uuid)::text THEN
        PERFORM op.reject('merge_absorbed: the call is already absorbed or is this incident');
    END IF;
END
$$;

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

    -- A closed incident accepts nothing; a merged one only its unmerge and
    -- its own caller's answers.
    IF v_inc.state = 'closed' OR (v_inc.state = 'merged' AND p_type NOT IN ('incident.unmerged', 'screen.answered')) THEN
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
    ELSIF p_type = 'incident.merge_absorbed' THEN
        PERFORM op.check_merge_absorbed(p_payload, p_incident, v_inc.absorbed);
    ELSIF p_type = 'incident.split' THEN
        IF p_actor_kind <> 'staff' THEN
            PERFORM op.reject('only a coordinator splits a merge');
        END IF;
        PERFORM op.check_from(p_payload);
        IF NOT v_inc.absorbed ? ((p_payload ->> 'from')::uuid)::text THEN
            PERFORM op.reject('split: the call was not merged into this incident');
        END IF;
    ELSIF p_type = 'incident.unmerged' THEN
        PERFORM op.check_from(p_payload);
        IF v_inc.merged_into IS NULL OR v_inc.merged_into <> (p_payload ->> 'from')::uuid OR v_inc.t2 IS NULL THEN
            PERFORM op.reject('unmerged needs its survivor and a T2 (merged at dispatch)');
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
        merged_into   = CASE WHEN p_type = 'incident.merged' THEN v_into
                             WHEN p_type = 'incident.unmerged' THEN NULL ELSE i.merged_into END,
        t1            = CASE WHEN p_type = 'incident.merge_absorbed'
                             THEN least(i.t1, (p_payload ->> 't1')::timestamptz)
                             WHEN p_type = 'incident.split'
                             THEN least(i.created_at, (SELECT min(a.value::timestamptz)
                                                         FROM jsonb_each_text(i.absorbed - ((p_payload ->> 'from')::uuid)::text) AS a))
                             ELSE i.t1 END,
        absorbed      = CASE WHEN p_type = 'incident.merge_absorbed'
                             THEN i.absorbed || jsonb_build_object(((p_payload ->> 'from')::uuid)::text, (p_payload ->> 't1')::timestamptz)
                             WHEN p_type = 'incident.split' THEN i.absorbed - ((p_payload ->> 'from')::uuid)::text
                             ELSE i.absorbed END,
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

-- A coordinator's split of a wrong merge (Build Prompt §10.3: "the
-- coordinator can split one, also logged"): incident.split on the survivor
-- and incident.unmerged on the member, atomically, with the staff identity on
-- both events. Idempotent per (survivor, member). The console role (M5) is the
-- only runtime role allowed to call it.
CREATE FUNCTION op.split_incident(p_survivor uuid, p_member uuid, p_staff uuid) RETURNS void
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_key text := 'split:' || p_survivor || ':' || p_member;
BEGIN
    IF p_staff IS NULL THEN
        PERFORM op.reject('a split needs the coordinator''s staff identity');
    END IF;
    PERFORM op.append_incident_event(p_survivor, 0, 'incident.split', jsonb_build_object('from', p_member),
                                     'staff', p_staff, v_key || ':split', NULL);
    PERFORM op.append_incident_event(p_member, 0, 'incident.unmerged', jsonb_build_object('from', p_survivor),
                                     'staff', p_staff, v_key || ':unmerge', NULL);
END
$$;

GRANT EXECUTE ON FUNCTION op.split_incident(uuid, uuid, uuid) TO op_console;
GRANT SELECT ON op.incidents, op.incident_events TO op_console;
