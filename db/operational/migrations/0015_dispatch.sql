-- Dispatch store and its single write path (M2 Task 4; Build Prompt §9
-- Dispatch, §10.4; ADR-0024, ADR-0025).
--
-- Every dispatch fact is a row in its own table AND an incident event in the
-- same transaction, written by one SECURITY DEFINER function per command:
-- issue_alert (alert.issued; the first is T3), accept_alert
-- (responder.accepted; the first is T4; first acceptance per role wins by a
-- partial unique index), decline_alert, alert_no_response, stand_down_alert,
-- record_forward / acknowledge_forward / time_out_forward, notify_caller and
-- dispatch_escalate. The generic append refuses dispatch event types, so the
-- tables and the event log cannot disagree. Payload checks mirror
-- internal/incident/dispatch.go exactly.
--
-- op.dispatch_timers holds one wake per incident in dispatch (the pure
-- planner's next_wake); facts that need a fresh plan (an acceptance, a
-- decline, an acknowledgement) wake it at once and bump its generation, so
-- a worker that planned on older facts never pushes the wake back.
-- op.alert_attempts records each delivery attempt BEFORE the provider call;
-- an attempt is claimed once, so a crash never sends it twice (scenario 14).

INSERT INTO op.incident_transitions (from_state, event_type, to_state) VALUES
    ('on_scene', 'alert.issued',                 NULL),
    ('on_scene', 'responder.accepted',           NULL),
    ('*',        'alert.declined',               NULL),
    ('*',        'alert.no_response',            NULL),
    ('*',        'alert.stood_down',             NULL),
    ('*',        'incident.forwarded',           NULL),
    ('*',        'incident.forward_acknowledged', NULL),
    ('*',        'incident.forward_timed_out',   NULL),
    ('*',        'caller.notified',              NULL);

-- Payload checks (mirror internal/incident/dispatch.go).
CREATE FUNCTION op.check_alert_fact(p_type text, p jsonb) RETURNS void
LANGUAGE plpgsql IMMUTABLE
SET search_path = op, pg_temp
AS $$
DECLARE
    v_keys text[];
    v_opt  text[] := '{}';
    k      text;
BEGIN
    v_keys := CASE p_type
        WHEN 'alert.issued'       THEN ARRAY['alert', 'responder', 'role', 'ring', 'wave']
        WHEN 'responder.accepted' THEN ARRAY['alert', 'responder', 'role']
        WHEN 'alert.declined'     THEN ARRAY['alert', 'responder']
        ELSE ARRAY['alert'] END;
    IF p_type = 'alert.declined' THEN
        v_opt := ARRAY['reason'];
    END IF;
    IF jsonb_typeof(p) IS DISTINCT FROM 'object'
       OR EXISTS (SELECT 1 FROM jsonb_object_keys(p) x WHERE NOT x = ANY (v_keys || v_opt)) THEN
        PERFORM op.reject(p_type || ': unexpected key');
    END IF;
    FOREACH k IN ARRAY v_keys LOOP
        IF NOT p ? k THEN
            PERFORM op.reject(format('%s needs "%s"', p_type, k));
        END IF;
    END LOOP;
    FOREACH k IN ARRAY ARRAY['alert', 'responder'] LOOP
        IF p ? k AND (jsonb_typeof(p -> k) IS DISTINCT FROM 'string'
           OR (p ->> k) !~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$') THEN
            PERFORM op.reject(format('%s: %s must be a UUID', p_type, k));
        END IF;
    END LOOP;
    IF p ? 'role' AND (jsonb_typeof(p -> 'role') IS DISTINCT FROM 'string' OR (p ->> 'role') NOT IN ('vht', 'transporter')) THEN
        PERFORM op.reject(p_type || ': role must be vht or transporter');
    END IF;
    IF p ? 'ring' AND (jsonb_typeof(p -> 'ring') IS DISTINCT FROM 'number' OR (p ->> 'ring') !~ '^[1-9][0-9]?$') THEN
        PERFORM op.reject(p_type || ': ring must be 1-99');
    END IF;
    IF p ? 'wave' AND (jsonb_typeof(p -> 'wave') IS DISTINCT FROM 'number' OR (p ->> 'wave') !~ '^[1-9][0-9]{0,3}$') THEN
        PERFORM op.reject(p_type || ': wave must be 1-9999');
    END IF;
    IF p ? 'reason' AND (jsonb_typeof(p -> 'reason') IS DISTINCT FROM 'string' OR (p ->> 'reason') !~ '^[a-z][a-z0-9_.-]{0,63}$') THEN
        PERFORM op.reject(p_type || ': reason must be a code');
    END IF;
END
$$;

CREATE FUNCTION op.check_facility(p jsonb) RETURNS void
LANGUAGE plpgsql IMMUTABLE
SET search_path = op, pg_temp
AS $$
BEGIN
    IF jsonb_typeof(p) IS DISTINCT FROM 'object'
       OR EXISTS (SELECT 1 FROM jsonb_object_keys(p) k WHERE k <> 'facility')
       OR jsonb_typeof(p -> 'facility') IS DISTINCT FROM 'string'
       OR (p ->> 'facility') !~ '^[a-z][a-z0-9_.-]{0,63}$' THEN
        PERFORM op.reject('payload needs exactly a "facility" code');
    END IF;
END
$$;

CREATE FUNCTION op.check_caller_notified(p jsonb) RETURNS void
LANGUAGE plpgsql IMMUTABLE
SET search_path = op, pg_temp
AS $$
BEGIN
    IF jsonb_typeof(p) IS DISTINCT FROM 'object'
       OR EXISTS (SELECT 1 FROM jsonb_object_keys(p) k WHERE k NOT IN ('kind', 'facility'))
       OR jsonb_typeof(p -> 'kind') IS DISTINCT FROM 'string'
       OR NOT ((p ->> 'kind' = 'helper_on_the_way' AND NOT p ? 'facility')
            OR (p ->> 'kind' = 'forwarded_to_facility' AND jsonb_typeof(p -> 'facility') = 'string'
                AND (p ->> 'facility') ~ '^[a-z][a-z0-9_.-]{0,63}$')) THEN
        PERFORM op.reject('caller.notified needs a known kind, with a facility only when forwarded');
    END IF;
END
$$;

CREATE FUNCTION op.check_escalated(p jsonb) RETURNS void
LANGUAGE plpgsql IMMUTABLE
SET search_path = op, pg_temp
AS $$
BEGIN
    IF jsonb_typeof(p) IS DISTINCT FROM 'object'
       OR EXISTS (SELECT 1 FROM jsonb_object_keys(p) k WHERE k NOT IN ('reason', 'role'))
       OR jsonb_typeof(p -> 'reason') IS DISTINCT FROM 'string'
       OR (p ->> 'reason') !~ '^[a-z][a-z0-9_.-]{0,63}$'
       OR (p ? 'role' AND (jsonb_typeof(p -> 'role') IS DISTINCT FROM 'string' OR (p ->> 'role') NOT IN ('vht', 'transporter'))) THEN
        PERFORM op.reject('incident.escalated needs a reason code and at most a role');
    END IF;
END
$$;

CREATE TABLE op.alerts (
    id            uuid        PRIMARY KEY,
    incident_id   uuid        NOT NULL REFERENCES op.incidents (id),
    responder_id  uuid        NOT NULL REFERENCES op.responders (id),
    role          text        NOT NULL CHECK (role IN ('vht', 'transporter')),
    ring          int         NOT NULL CHECK (ring BETWEEN 1 AND 99),
    wave          int         NOT NULL CHECK (wave BETWEEN 1 AND 9999),
    sent_at       timestamptz NOT NULL,
    logic_version text        NOT NULL,
    outcome       text        NOT NULL DEFAULT 'pending'
                              CHECK (outcome IN ('pending', 'accepted', 'declined', 'no_response', 'stood_down')),
    decline_reason text       CHECK (decline_reason ~ '^[a-z][a-z0-9_.-]{0,63}$'),
    responded_at  timestamptz,
    closed_at     timestamptz,
    UNIQUE (incident_id, responder_id),
    CHECK ((outcome = 'pending') = (closed_at IS NULL)),
    CHECK (responded_at IS NULL OR outcome IN ('accepted', 'declined')),
    CHECK (decline_reason IS NULL OR outcome = 'declined')
);
CREATE INDEX alerts_pending ON op.alerts (incident_id) WHERE outcome = 'pending';
CREATE INDEX alerts_responder ON op.alerts (responder_id, sent_at);

-- First acceptance per role wins (Build Prompt §10.4): at most one active
-- assignment per incident and role, enforced by the database.
CREATE TABLE op.assignments (
    incident_id  uuid        NOT NULL REFERENCES op.incidents (id),
    role         text        NOT NULL CHECK (role IN ('vht', 'transporter')),
    responder_id uuid        NOT NULL REFERENCES op.responders (id),
    alert_id     uuid        NOT NULL UNIQUE REFERENCES op.alerts (id),
    accepted_at  timestamptz NOT NULL,
    released_at  timestamptz
);
CREATE UNIQUE INDEX assignments_first_accept_wins ON op.assignments (incident_id, role) WHERE released_at IS NULL;
CREATE INDEX assignments_responder ON op.assignments (responder_id) WHERE released_at IS NULL;

-- Delivery attempts, recorded before the provider is called. One attempt
-- per alert, purpose and channel: claimed once, never re-sent; a result the
-- provider never confirmed stays 'claimed' and is settled 'unknown'.
CREATE TABLE op.alert_attempts (
    id           uuid        PRIMARY KEY,
    alert_id     uuid        NOT NULL REFERENCES op.alerts (id),
    purpose      text        NOT NULL CHECK (purpose IN ('alert', 'stand_down')),
    channel      text        NOT NULL CHECK (channel IN ('ussd_push', 'sms', 'voice', 'ussd_pull')),
    status       text        NOT NULL DEFAULT 'claimed' CHECK (status IN ('claimed', 'sent', 'failed', 'unknown')),
    provider_ref text        CHECK (provider_ref ~ '^[A-Za-z0-9_.:-]{1,128}$'),
    claimed_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    settled_at   timestamptz,
    UNIQUE (alert_id, purpose, channel),
    CHECK ((status = 'claimed') = (settled_at IS NULL))
);

CREATE TABLE op.forwards (
    incident_id       uuid        NOT NULL REFERENCES op.incidents (id),
    facility_id       text        NOT NULL,
    gazetteer_version text        NOT NULL,
    sent_at           timestamptz NOT NULL,
    acknowledged_at   timestamptz,
    timed_out_at      timestamptz,
    PRIMARY KEY (incident_id, facility_id),
    FOREIGN KEY (gazetteer_version, facility_id) REFERENCES op.facilities (version, id)
);

CREATE TABLE op.caller_notices (
    incident_id uuid        NOT NULL REFERENCES op.incidents (id),
    kind        text        NOT NULL CHECK (kind IN ('helper_on_the_way', 'forwarded_to_facility')),
    facility_id text,
    created_at  timestamptz NOT NULL,
    PRIMARY KEY (incident_id, kind),
    CHECK ((kind = 'forwarded_to_facility') = (facility_id IS NOT NULL))
);

CREATE TABLE op.dispatch_timers (
    incident_id uuid        PRIMARY KEY REFERENCES op.incidents (id),
    due_at      timestamptz NOT NULL,
    gen         bigint      NOT NULL DEFAULT 0,
    lease_until timestamptz
);
CREATE INDEX dispatch_timers_due ON op.dispatch_timers (due_at);

CREATE FUNCTION op.append_event_core(
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
    -- Only a coordinator's console session records a staff action, a split
    -- or an unmerge (ADR-0022): intake, IVR and every other runtime role are
    -- refused here whatever the payload. session_user is the login role even
    -- inside this SECURITY DEFINER function; op.split_incident is called by
    -- op_console, so its appends pass.
    IF (p_actor_kind = 'staff' OR p_type IN ('incident.split', 'incident.unmerged'))
       AND NOT pg_has_role(session_user, 'op_console', 'MEMBER') THEN
        RAISE EXCEPTION 'illegal transition: only the console records staff actions, splits and unmerges'
            USING ERRCODE = 'SA422';
    END IF;
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
    -- A closed incident never has a responder on the way: whoever closes it
    -- stands the alerts in flight down first, in the same transaction.
    IF p_type = 'incident.closed' AND EXISTS (SELECT 1 FROM op.alerts a WHERE a.incident_id = p_incident AND a.outcome = 'pending') THEN
        PERFORM op.reject('stand down the alerts in flight before closing');
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
    ELSIF p_type = 'incident.severity_raised' THEN
        PERFORM op.check_severity_raised(p_payload, p_incident, v_inc.severity);
        v_sev := p_payload ->> 'severity';
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
    ELSIF p_type IN ('alert.issued', 'responder.accepted', 'alert.declined', 'alert.no_response', 'alert.stood_down') THEN
        PERFORM op.check_alert_fact(p_type, p_payload);
    ELSIF p_type IN ('incident.forwarded', 'incident.forward_acknowledged', 'incident.forward_timed_out') THEN
        PERFORM op.check_facility(p_payload);
    ELSIF p_type = 'caller.notified' THEN
        PERFORM op.check_caller_notified(p_payload);
    ELSIF p_type = 'incident.escalated' THEN
        PERFORM op.check_escalated(p_payload);
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
        severity      = CASE WHEN p_type IN ('incident.classified', 'incident.severity_raised') THEN v_sev ELSE i.severity END,
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

    -- Dispatch wakes (ADR-0024): T2 or a split revives dispatch at once; a
    -- merge or a closure ends it.
    IF p_type IN ('incident.dispatch_ready', 'incident.unmerged') THEN
        INSERT INTO op.dispatch_timers (incident_id, due_at) VALUES (p_incident, v_at)
        ON CONFLICT (incident_id) DO UPDATE SET due_at = v_at, gen = op.dispatch_timers.gen + 1, lease_until = NULL;
    ELSIF p_type IN ('incident.merged', 'incident.closed') THEN
        DELETE FROM op.dispatch_timers t WHERE t.incident_id = p_incident;
    END IF;

    PERFORM op.enqueue_audit(v_ev, v_at, p_type, p_incident, p_staff_id, p_logic_version, p_payload);

    RETURN QUERY SELECT v_ev, v_seq, v_at, coalesce(v_to, v_inc.state), false;
END
$$;
REVOKE ALL ON FUNCTION op.append_event_core(uuid, bigint, text, jsonb, text, uuid, text, text) FROM PUBLIC;

-- The generic append: everything but the dispatch facts, which only the
-- dispatch functions below write (so op.alerts and the log never disagree).
CREATE OR REPLACE FUNCTION op.append_incident_event(
    p_incident uuid, p_expected_version bigint, p_type text, p_payload jsonb,
    p_actor_kind text, p_staff_id uuid, p_idem_key text, p_logic_version text
) RETURNS TABLE (event_id uuid, seq bigint, recorded_at timestamptz, new_state text, duplicate boolean)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
BEGIN
    IF p_type IN ('alert.issued', 'responder.accepted', 'alert.declined', 'alert.no_response', 'alert.stood_down',
                  'incident.forwarded', 'incident.forward_acknowledged', 'incident.forward_timed_out', 'caller.notified') THEN
        PERFORM op.reject(p_type || ' is written only by the dispatch functions');
    END IF;
    RETURN QUERY SELECT * FROM op.append_event_core(p_incident, p_expected_version, p_type, p_payload,
                                                    p_actor_kind, p_staff_id, p_idem_key, p_logic_version);
END
$$;

-- Wake the dispatch planner for an incident now (a new fact to plan on).
CREATE FUNCTION op.wake_dispatch(p_incident uuid) RETURNS void
LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
    UPDATE op.dispatch_timers t SET due_at = clock_timestamp(), gen = t.gen + 1 WHERE t.incident_id = p_incident
$$;

-- Alert one responder. Defence in depth behind the planner (review focus 4):
-- the responder must match the role, be active, certified, insured, kit OK
-- and available now, not already assigned elsewhere, and the role must be
-- untaken. One alert per responder per incident: a retry returns it.
CREATE FUNCTION op.issue_alert(p_incident uuid, p_responder uuid, p_role text, p_ring int, p_wave int, p_logic_version text)
RETURNS TABLE (alert_id uuid, sent_at timestamptz, duplicate boolean)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
#variable_conflict use_column
DECLARE
    v_a   op.alerts%ROWTYPE;
    v_r   op.responders%ROWTYPE;
    v_id  uuid := uuidv7();
    v_now timestamptz;
    v_ev  record;
BEGIN
    IF p_logic_version IS NULL THEN
        PERFORM op.reject('an alert carries the dispatch parameter version');
    END IF;
    PERFORM 1 FROM op.incidents i WHERE i.id = p_incident FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'unknown incident %', p_incident USING ERRCODE = 'SA404';
    END IF;
    SELECT * INTO v_a FROM op.alerts a WHERE a.incident_id = p_incident AND a.responder_id = p_responder;
    IF FOUND THEN
        RETURN QUERY SELECT v_a.id, v_a.sent_at, true;
        RETURN;
    END IF;
    SELECT * INTO v_r FROM op.responders r WHERE r.id = p_responder FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'unknown responder %', p_responder USING ERRCODE = 'SA404';
    END IF;
    v_now := clock_timestamp();
    IF v_r.role <> p_role THEN
        PERFORM op.reject('the alert role does not match the responder');
    END IF;
    IF v_r.status <> 'active' OR v_r.certified_at IS NULL OR v_r.certified_at > v_now
       OR v_r.insured_until IS NULL OR v_r.insured_until <= v_now OR NOT v_r.kit_ok
       OR NOT EXISTS (SELECT 1 FROM op.availability v WHERE v.responder_id = p_responder AND v.period @> v_now) THEN
        PERFORM op.reject('the responder is not eligible now (gates or availability)');
    END IF;
    IF EXISTS (SELECT 1 FROM op.assignments s WHERE s.incident_id = p_incident AND s.role = p_role AND s.released_at IS NULL) THEN
        PERFORM op.reject('the role is already taken');
    END IF;
    IF EXISTS (SELECT 1 FROM op.assignments s JOIN op.incidents i ON i.id = s.incident_id
                WHERE s.responder_id = p_responder AND s.released_at IS NULL AND i.state NOT IN ('closed', 'merged')) THEN
        PERFORM op.reject('the responder is assigned to another incident');
    END IF;
    SELECT * INTO v_ev FROM op.append_event_core(p_incident, NULL, 'alert.issued',
        jsonb_build_object('alert', v_id, 'responder', p_responder, 'role', p_role, 'ring', p_ring, 'wave', p_wave),
        'system', NULL, 'alert:' || p_responder, p_logic_version);
    INSERT INTO op.alerts (id, incident_id, responder_id, role, ring, wave, sent_at, logic_version)
    VALUES (v_id, p_incident, p_responder, p_role, p_ring, p_wave, v_ev.recorded_at, p_logic_version);
    RETURN QUERY SELECT v_id, v_ev.recorded_at, false;
END
$$;

-- Lock the alert's incident and return the alert as it stands under the lock.
CREATE FUNCTION op.lock_alert(p_alert uuid, OUT a op.alerts, OUT state text)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_incident uuid;
BEGIN
    SELECT x.incident_id INTO v_incident FROM op.alerts x WHERE x.id = p_alert;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'unknown alert %', p_alert USING ERRCODE = 'SA404';
    END IF;
    SELECT i.state INTO state FROM op.incidents i WHERE i.id = v_incident FOR UPDATE;
    SELECT * INTO a FROM op.alerts x WHERE x.id = p_alert;
END
$$;

-- First acceptance per role wins (the partial unique index is the
-- guarantee; the incident lock orders concurrent accepts). A responder's
-- late acceptance after a lapse still counts while the role is open.
CREATE FUNCTION op.accept_alert(p_alert uuid, p_responder uuid)
RETURNS TABLE (result text, incident_id uuid, role text)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
#variable_conflict use_column
DECLARE
    v_l  record;
    v_a  op.alerts%ROWTYPE;
    v_ev record;
BEGIN
    SELECT * INTO v_l FROM op.lock_alert(p_alert);
    v_a := v_l.a;
    IF v_a.responder_id <> p_responder THEN
        RAISE EXCEPTION 'alert % belongs to another responder', p_alert USING ERRCODE = 'SA403';
    END IF;
    IF v_a.outcome = 'accepted' THEN
        RETURN QUERY SELECT 'accepted'::text, v_a.incident_id, v_a.role;
        RETURN;
    END IF;
    IF v_l.state NOT IN ('dispatching', 'assigned', 'on_scene') THEN
        RETURN QUERY SELECT 'closed'::text, v_a.incident_id, v_a.role;
        RETURN;
    END IF;
    IF EXISTS (SELECT 1 FROM op.assignments s WHERE s.incident_id = v_a.incident_id AND s.role = v_a.role AND s.released_at IS NULL) THEN
        RETURN QUERY SELECT 'already_taken'::text, v_a.incident_id, v_a.role;
        RETURN;
    END IF;
    IF EXISTS (SELECT 1 FROM op.assignments s JOIN op.incidents i ON i.id = s.incident_id
                WHERE s.responder_id = p_responder AND s.released_at IS NULL AND i.state NOT IN ('closed', 'merged')) THEN
        RETURN QUERY SELECT 'already_assigned'::text, v_a.incident_id, v_a.role;
        RETURN;
    END IF;
    SELECT * INTO v_ev FROM op.append_event_core(v_a.incident_id, NULL, 'responder.accepted',
        jsonb_build_object('alert', v_a.id, 'responder', v_a.responder_id, 'role', v_a.role),
        'responder', NULL, 'accept:' || v_a.id, v_a.logic_version);
    UPDATE op.alerts x SET outcome = 'accepted', decline_reason = NULL,
                           responded_at = v_ev.recorded_at, closed_at = v_ev.recorded_at
     WHERE x.id = v_a.id;
    INSERT INTO op.assignments (incident_id, role, responder_id, alert_id, accepted_at)
    VALUES (v_a.incident_id, v_a.role, v_a.responder_id, v_a.id, v_ev.recorded_at);
    PERFORM op.wake_dispatch(v_a.incident_id);
    RETURN QUERY SELECT 'accepted'::text, v_a.incident_id, v_a.role;
END
$$;

-- A decline moves the cascade on at once. Decline reasons (decision 6) sit
-- behind the amendment gate and arrive with their configured list (Task 9):
-- until then a decline carries no reason.
CREATE FUNCTION op.decline_alert(p_alert uuid, p_responder uuid, p_reason text) RETURNS text
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_l  record;
    v_a  op.alerts%ROWTYPE;
    v_ev record;
BEGIN
    SELECT * INTO v_l FROM op.lock_alert(p_alert);
    v_a := v_l.a;
    IF v_a.responder_id <> p_responder THEN
        RAISE EXCEPTION 'alert % belongs to another responder', p_alert USING ERRCODE = 'SA403';
    END IF;
    IF v_a.outcome <> 'pending' THEN
        RETURN v_a.outcome;
    END IF;
    IF p_reason IS NOT NULL THEN
        PERFORM op.reject('decline reasons are behind the amendment gate (decision 6)');
    END IF;
    SELECT * INTO v_ev FROM op.append_event_core(v_a.incident_id, NULL, 'alert.declined',
        jsonb_build_object('alert', v_a.id, 'responder', v_a.responder_id),
        'responder', NULL, 'decline:' || v_a.id, v_a.logic_version);
    UPDATE op.alerts x SET outcome = 'declined', responded_at = v_ev.recorded_at, closed_at = v_ev.recorded_at
     WHERE x.id = v_a.id;
    PERFORM op.wake_dispatch(v_a.incident_id);
    RETURN 'declined';
END
$$;

-- Settle a pending alert the responder never answered (no_response, at the
-- cascade timeout) or no longer needed (stood_down). False if already settled.
CREATE FUNCTION op.close_alert(p_alert uuid, p_outcome text) RETURNS boolean
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_l  record;
    v_a  op.alerts%ROWTYPE;
    v_ev record;
BEGIN
    IF p_outcome NOT IN ('no_response', 'stood_down') THEN
        PERFORM op.reject('an alert is closed as no_response or stood_down');
    END IF;
    SELECT * INTO v_l FROM op.lock_alert(p_alert);
    v_a := v_l.a;
    IF v_a.outcome <> 'pending' THEN
        RETURN false;
    END IF;
    SELECT * INTO v_ev FROM op.append_event_core(v_a.incident_id, NULL, 'alert.' || p_outcome,
        jsonb_build_object('alert', v_a.id), 'system', NULL, p_outcome || ':' || v_a.id, v_a.logic_version);
    UPDATE op.alerts x SET outcome = p_outcome, closed_at = v_ev.recorded_at WHERE x.id = v_a.id;
    RETURN true;
END
$$;

-- Forward to a facility of the gazetteer version in force (amended mode).
CREATE FUNCTION op.record_forward(p_incident uuid, p_facility text, p_gazetteer_version text, p_logic_version text)
RETURNS boolean
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_ev record;
BEGIN
    PERFORM 1 FROM op.incidents i WHERE i.id = p_incident FOR UPDATE;
    IF EXISTS (SELECT 1 FROM op.forwards f WHERE f.incident_id = p_incident AND f.facility_id = p_facility) THEN
        RETURN false;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM op.facilities f
                    WHERE f.version = p_gazetteer_version AND f.id = p_facility AND f.forwarding_enabled) THEN
        PERFORM op.reject('not a forwarding facility in that gazetteer version');
    END IF;
    SELECT * INTO v_ev FROM op.append_event_core(p_incident, NULL, 'incident.forwarded',
        jsonb_build_object('facility', p_facility), 'system', NULL, 'forward:' || p_facility, p_logic_version);
    INSERT INTO op.forwards (incident_id, facility_id, gazetteer_version, sent_at)
    VALUES (p_incident, p_facility, p_gazetteer_version, v_ev.recorded_at);
    RETURN true;
END
$$;

-- The facility pressed 1 (ivr-controller). A late acknowledgement counts.
CREATE FUNCTION op.acknowledge_forward(p_incident uuid, p_facility text) RETURNS boolean
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_f  op.forwards%ROWTYPE;
    v_ev record;
BEGIN
    PERFORM 1 FROM op.incidents i WHERE i.id = p_incident FOR UPDATE;
    SELECT * INTO v_f FROM op.forwards f WHERE f.incident_id = p_incident AND f.facility_id = p_facility;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'no forward of % to %', p_incident, p_facility USING ERRCODE = 'SA404';
    END IF;
    IF v_f.acknowledged_at IS NOT NULL THEN
        RETURN false;
    END IF;
    SELECT * INTO v_ev FROM op.append_event_core(p_incident, NULL, 'incident.forward_acknowledged',
        jsonb_build_object('facility', p_facility), 'system', NULL, 'forward_ack:' || p_facility, NULL);
    UPDATE op.forwards f SET acknowledged_at = v_ev.recorded_at WHERE f.incident_id = p_incident AND f.facility_id = p_facility;
    PERFORM op.wake_dispatch(p_incident);
    RETURN true;
END
$$;

CREATE FUNCTION op.time_out_forward(p_incident uuid, p_facility text) RETURNS boolean
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_f  op.forwards%ROWTYPE;
    v_ev record;
BEGIN
    PERFORM 1 FROM op.incidents i WHERE i.id = p_incident FOR UPDATE;
    SELECT * INTO v_f FROM op.forwards f WHERE f.incident_id = p_incident AND f.facility_id = p_facility;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'no forward of % to %', p_incident, p_facility USING ERRCODE = 'SA404';
    END IF;
    IF v_f.acknowledged_at IS NOT NULL OR v_f.timed_out_at IS NOT NULL THEN
        RETURN false;
    END IF;
    SELECT * INTO v_ev FROM op.append_event_core(p_incident, NULL, 'incident.forward_timed_out',
        jsonb_build_object('facility', p_facility), 'system', NULL, 'forward_timeout:' || p_facility, NULL);
    UPDATE op.forwards f SET timed_out_at = v_ev.recorded_at WHERE f.incident_id = p_incident AND f.facility_id = p_facility;
    RETURN true;
END
$$;

-- A message to the caller, once per kind (the SMS itself is Part B's).
CREATE FUNCTION op.notify_caller(p_incident uuid, p_kind text, p_facility text) RETURNS boolean
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_ev record;
BEGIN
    PERFORM 1 FROM op.incidents i WHERE i.id = p_incident FOR UPDATE;
    IF EXISTS (SELECT 1 FROM op.caller_notices n WHERE n.incident_id = p_incident AND n.kind = p_kind) THEN
        RETURN false;
    END IF;
    SELECT * INTO v_ev FROM op.append_event_core(p_incident, NULL, 'caller.notified',
        CASE WHEN p_facility IS NULL THEN jsonb_build_object('kind', p_kind)
             ELSE jsonb_build_object('kind', p_kind, 'facility', p_facility) END,
        'system', NULL, 'notify:' || p_kind, NULL);
    INSERT INTO op.caller_notices (incident_id, kind, facility_id, created_at)
    VALUES (p_incident, p_kind, p_facility, v_ev.recorded_at);
    RETURN true;
END
$$;

-- The coordinator's alert from dispatch: once per reason per incident.
CREATE FUNCTION op.dispatch_escalate(p_incident uuid, p_reason text, p_role text, p_logic_version text) RETURNS boolean
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_ev record;
BEGIN
    SELECT * INTO v_ev FROM op.append_event_core(p_incident, NULL, 'incident.escalated',
        CASE WHEN p_role IS NULL THEN jsonb_build_object('reason', p_reason)
             ELSE jsonb_build_object('reason', p_reason, 'role', p_role) END,
        'system', NULL, 'escalate:' || p_reason, p_logic_version);
    RETURN NOT v_ev.duplicate;
END
$$;

-- Claim a delivery attempt before calling the provider. Fresh only once:
-- a retry after a crash finds it claimed and does not send again.
CREATE FUNCTION op.begin_attempt(p_alert uuid, p_purpose text, p_channel text)
RETURNS TABLE (attempt_id uuid, fresh boolean)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_id uuid;
BEGIN
    INSERT INTO op.alert_attempts (id, alert_id, purpose, channel) VALUES (uuidv7(), p_alert, p_purpose, p_channel)
    ON CONFLICT ON CONSTRAINT alert_attempts_alert_id_purpose_channel_key DO NOTHING
    RETURNING id INTO v_id;
    IF v_id IS NOT NULL THEN
        RETURN QUERY SELECT v_id, true;
        RETURN;
    END IF;
    RETURN QUERY SELECT x.id, false FROM op.alert_attempts x
                  WHERE x.alert_id = p_alert AND x.purpose = p_purpose AND x.channel = p_channel;
END
$$;

CREATE FUNCTION op.finish_attempt(p_attempt uuid, p_status text, p_provider_ref text) RETURNS boolean
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
BEGIN
    IF p_status NOT IN ('sent', 'failed', 'unknown') THEN
        PERFORM op.reject('an attempt settles as sent, failed or unknown');
    END IF;
    UPDATE op.alert_attempts x SET status = p_status, provider_ref = p_provider_ref, settled_at = clock_timestamp()
     WHERE x.id = p_attempt AND x.status = 'claimed';
    RETURN FOUND;
END
$$;

-- Due incidents, most urgent first, each leased so a second worker skips it.
CREATE FUNCTION op.claim_due_dispatch(p_limit int, p_lease interval)
RETURNS TABLE (incident_id uuid, gen bigint)
LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
    WITH due AS (
        SELECT t.incident_id
          FROM op.dispatch_timers t JOIN op.incidents i ON i.id = t.incident_id
         WHERE t.due_at <= clock_timestamp() AND (t.lease_until IS NULL OR t.lease_until < clock_timestamp())
         ORDER BY op.severity_rank(i.severity) DESC, t.due_at
         LIMIT p_limit
           FOR UPDATE OF t SKIP LOCKED)
    UPDATE op.dispatch_timers t SET lease_until = clock_timestamp() + p_lease
      FROM due WHERE t.incident_id = due.incident_id
    RETURNING t.incident_id, t.gen
$$;

-- Store the planner's next wake (NULL: none until a new fact). A fact that
-- arrived after the claim (a newer generation) keeps the incident due now.
CREATE FUNCTION op.schedule_dispatch(p_incident uuid, p_gen bigint, p_due timestamptz) RETURNS void
LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
    UPDATE op.dispatch_timers t
       SET due_at = CASE WHEN t.gen = p_gen THEN coalesce(p_due, 'infinity') ELSE least(t.due_at, coalesce(p_due, 'infinity')) END,
           lease_until = NULL
     WHERE t.incident_id = p_incident
$$;

GRANT EXECUTE ON FUNCTION
    op.issue_alert(uuid, uuid, text, int, int, text),
    op.close_alert(uuid, text),
    op.record_forward(uuid, text, text, text),
    op.time_out_forward(uuid, text),
    op.notify_caller(uuid, text, text),
    op.dispatch_escalate(uuid, text, text, text),
    op.begin_attempt(uuid, text, text),
    op.finish_attempt(uuid, text, text),
    op.claim_due_dispatch(int, interval),
    op.schedule_dispatch(uuid, bigint, timestamptz)
TO op_dispatch;
GRANT EXECUTE ON FUNCTION op.accept_alert(uuid, uuid), op.decline_alert(uuid, uuid, text) TO op_intake, op_ivr;
GRANT EXECUTE ON FUNCTION op.acknowledge_forward(uuid, text) TO op_ivr;
GRANT SELECT ON op.incidents, op.incident_events, op.alerts, op.assignments, op.alert_attempts, op.forwards,
                op.caller_notices, op.dispatch_timers, op.facilities, op.stages, op.config_versions
    TO op_dispatch;
GRANT SELECT ON op.alerts, op.assignments, op.alert_attempts, op.forwards, op.caller_notices, op.dispatch_timers
    TO op_readonly;
