-- One alert per responder at a time, for the nearest incident (UX audit F-78;
-- ADR-0044).
--
-- A responder could hold several pending alerts at once (a surge alerted
-- VHT 4 for five calls), while the menu offers only the newest: the others
-- could not be answered, and the planner counted on someone who could take
-- one call at most. Now:
--
-- 1. op.issue_alert refuses a responder who already holds a pending alert for
--    another open incident (dispatching, assigned or on scene). The responder
--    row is locked for update, so two incidents planned at once cannot both
--    alert the same responder. The planner leaves such a responder out, or
--    moves them (2); the refusal is the last guard behind it, as the other
--    refusals here are (review focus 4). Otherwise the function is 0018's.
-- 2. op.move_alert(p_alert) stands a pending alert down because its responder
--    is wanted at an incident nearer to them (dispatch-worker decides, by
--    zone distance, never away from a more urgent incident), and wakes the
--    first incident's planner at once so it looks for someone else and the
--    responder is told it is no longer needed. False if the alert was no
--    longer pending.
-- 3. op.wake_open_dispatch() wakes the planner of every incident still
--    looking for help, when a responder is freed (an alert stood down or
--    lapsed), so the incidents waiting for one try again now rather than at
--    their next ring step. It only moves due times earlier.

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
    -- For update (0018: for share): one responder, one pending alert, even
    -- when two incidents are planned at the same moment.
    SELECT * INTO v_r FROM op.responders r WHERE r.id = p_responder FOR UPDATE;
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
    IF EXISTS (SELECT 1 FROM op.alerts a JOIN op.incidents i ON i.id = a.incident_id
                WHERE a.responder_id = p_responder AND a.outcome = 'pending' AND a.incident_id <> p_incident
                  AND i.state IN ('dispatching', 'assigned', 'on_scene')) THEN
        PERFORM op.reject('the responder holds an alert for another incident');
    END IF;
    SELECT * INTO v_ev FROM op.append_event_core(p_incident, NULL, 'alert.issued',
        jsonb_build_object('alert', v_id, 'responder', p_responder, 'role', p_role, 'ring', p_ring, 'wave', p_wave),
        'system', NULL, 'alert:' || p_responder, p_logic_version);
    INSERT INTO op.alerts (id, incident_id, responder_id, role, ring, wave, sent_at, logic_version)
    VALUES (v_id, p_incident, p_responder, p_role, p_ring, p_wave, v_ev.recorded_at, p_logic_version);
    RETURN QUERY SELECT v_id, v_ev.recorded_at, false;
END
$$;

CREATE FUNCTION op.move_alert(p_alert uuid) RETURNS boolean
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_incident uuid;
BEGIN
    SELECT a.incident_id INTO v_incident FROM op.alerts a WHERE a.id = p_alert;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'unknown alert %', p_alert USING ERRCODE = 'SA404';
    END IF;
    IF NOT op.close_alert(p_alert, 'stood_down') THEN
        RETURN false;
    END IF;
    PERFORM op.wake_dispatch(v_incident);
    RETURN true;
END
$$;

CREATE FUNCTION op.wake_open_dispatch() RETURNS integer
LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
    WITH woken AS (
        UPDATE op.dispatch_timers t SET due_at = clock_timestamp(), gen = t.gen + 1
          FROM op.incidents i
         WHERE i.id = t.incident_id AND i.state IN ('dispatching', 'assigned', 'on_scene') AND t.due_at > clock_timestamp()
        RETURNING 1)
    SELECT count(*)::integer FROM woken
$$;

REVOKE ALL ON FUNCTION op.move_alert(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION op.wake_open_dispatch() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION op.move_alert(uuid) TO op_dispatch;
GRANT EXECUTE ON FUNCTION op.wake_open_dispatch() TO op_dispatch;
