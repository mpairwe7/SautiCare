-- Review fixes to the dispatch store (PR #10 review I1, I4, I5, I7; PR #8
-- finding 4556490881; ADR-0024, ADR-0025).
--
-- I1  issue_alert and begin_attempt('alert') refuse while dispatch is
--     suspended (share lock on op.dispatch_control, which a suspension locks
--     exclusively) and begin_attempt only claims a pending alert.
-- I4  incident.severity_raised and incident.merge_absorbed wake dispatch.
-- I5  an escalation is once per reason AND role: key
--     escalate:<reason>:<role or -> (internal/incident EscalationKey), the
--     same key intake uses, so the coordinator is alerted once.
-- I7  closure and merge wake dispatch instead of deleting the wake (the
--     planner stands responders down and delivers, then parks it); a merge
--     is refused while an alert is pending.
-- PR #8 4556490881: incident.split is accepted on a closed survivor, so a
--     call wrongly merged into it is revived and dispatched; a closed
--     incident still accepts nothing else. Mirrors internal/incident.

INSERT INTO op.incident_transitions (from_state, event_type, to_state) VALUES ('closed', 'incident.split', NULL);

CREATE OR REPLACE FUNCTION op.append_event_core(
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
    -- PR #8 (4556490881): a coordinator may split a wrongly merged call off a
    -- survivor that has since closed, or that call would never be dispatched.
    IF (v_inc.state = 'closed' AND p_type <> 'incident.split')
       OR (v_inc.state = 'merged' AND p_type NOT IN ('incident.unmerged', 'screen.answered')) THEN
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
        -- Review I7: a merged call is never dispatched, so it cannot merge
        -- with a responder on the way.
        IF EXISTS (SELECT 1 FROM op.alerts a WHERE a.incident_id = p_incident AND a.outcome = 'pending') THEN
            PERFORM op.reject('stand down the alerts in flight before merging');
        END IF;
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

    -- Dispatch wakes (ADR-0024): T2 or a split starts dispatch at once.
    IF p_type IN ('incident.dispatch_ready', 'incident.unmerged') THEN
        INSERT INTO op.dispatch_timers (incident_id, due_at) VALUES (p_incident, v_at)
        ON CONFLICT (incident_id) DO UPDATE SET due_at = v_at, gen = op.dispatch_timers.gen + 1, lease_until = NULL;
    ELSIF p_type IN ('incident.merged', 'incident.closed', 'incident.severity_raised', 'incident.merge_absorbed') THEN
        -- Review I4, I7: a raise or an absorbed call re-plans at once (a red
        -- wave goes out now); a merge or closure re-plans so stand-downs are
        -- delivered, then the planner parks the wake.
        UPDATE op.dispatch_timers t SET due_at = v_at, gen = t.gen + 1 WHERE t.incident_id = p_incident;
    END IF;

    PERFORM op.enqueue_audit(v_ev, v_at, p_type, p_incident, p_staff_id, p_logic_version, p_payload);

    RETURN QUERY SELECT v_ev, v_seq, v_at, coalesce(v_to, v_inc.state), false;
END
$$;

CREATE OR REPLACE FUNCTION op.issue_alert(p_incident uuid, p_responder uuid, p_role text, p_ring int, p_wave int, p_logic_version text)
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
    -- Review I1: never while suspended. The share lock orders this after a
    -- suspension that is committing (set_dispatch_suspended locks the row).
    IF (SELECT c.suspended FROM op.dispatch_control c FOR SHARE) THEN
        PERFORM op.reject('dispatch is suspended');
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

CREATE OR REPLACE FUNCTION op.begin_attempt(p_alert uuid, p_purpose text, p_channel text)
RETURNS TABLE (attempt_id uuid, fresh boolean)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_id uuid;
BEGIN
    -- Review I1: an alert is delivered only while it is pending and dispatch
    -- is not suspended; otherwise nothing is claimed (fresh = false, no ID)
    -- and the alert lapses. Stand-downs are always delivered.
    IF p_purpose = 'alert' AND ((SELECT c.suspended FROM op.dispatch_control c FOR SHARE)
       OR NOT EXISTS (SELECT 1 FROM op.alerts a WHERE a.id = p_alert AND a.outcome = 'pending')) THEN
        RETURN QUERY SELECT NULL::uuid, false;
        RETURN;
    END IF;
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

-- The coordinator's alert from dispatch: once per reason and role.
CREATE OR REPLACE FUNCTION op.dispatch_escalate(p_incident uuid, p_reason text, p_role text, p_logic_version text) RETURNS boolean
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_ev record;
BEGIN
    SELECT * INTO v_ev FROM op.append_event_core(p_incident, NULL, 'incident.escalated',
        CASE WHEN p_role IS NULL THEN jsonb_build_object('reason', p_reason)
             ELSE jsonb_build_object('reason', p_reason, 'role', p_role) END,
        'system', NULL, 'escalate:' || p_reason || ':' || coalesce(p_role, '-'), p_logic_version);
    RETURN NOT v_ev.duplicate;
END
$$;

-- Review I2: the stopping rule judges only incidents dispatched since the
-- latest resumption, which dispatch reads from the register (no personal data).
GRANT SELECT ON op.dispatch_control_log TO op_dispatch;
