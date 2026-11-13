-- CodeRabbit review of PR #10 (M2 Part A) at the database write boundary
-- (ADR-0024, ADR-0025, ADR-0026). Every signature is unchanged, so the
-- grants of 0013 and 0015 stand.
--
-- 4557205834  set_availability reads the clock after the responder lock: a
--             change that waited for a concurrent one starts when it held the
--             lock, so the history never shows availability during the wait.
-- 4557205847  record_forward refuses while dispatch is suspended, under the
--             share lock on op.dispatch_control that issue_alert and
--             begin_attempt take (0018): a suspension holds that row
--             exclusively until it commits, so a forward that waited for it
--             sees it.
-- 4557205856  accept_alert locks the responder before the busy check: one
--             responder accepting alerts for two incidents holds two
--             different incident locks, so only the responder lock orders the
--             acceptances; the second hears already_assigned.
-- 4557205859  claim_due_dispatch returns the claimed rows most urgent first
--             (red before yellow before green, then the earliest wake): an
--             UPDATE ... RETURNING has no order, and the worker processes the
--             batch in the order returned.
-- 4557206046  register_responder refuses a stage that is not in the
--             responder's ward in the gazetteer in force: dispatch places a
--             responder by that stage's zone, and one it cannot place could
--             switch on and never be alerted.

CREATE OR REPLACE FUNCTION op.set_availability(p_id uuid, p_available boolean, p_source text)
RETURNS TABLE (period_from timestamptz, period_until timestamptz)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_now timestamptz;
BEGIN
    PERFORM 1 FROM op.responders r WHERE r.id = p_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'unknown responder %', p_id USING ERRCODE = 'SA404';
    END IF;
    -- After the lock, never at block entry: a call that waited starts now.
    v_now := clock_timestamp();
    UPDATE op.availability a SET period = tstzrange(lower(a.period), v_now, '[)')
     WHERE a.responder_id = p_id AND a.period @> v_now AND lower(a.period) < v_now;
    DELETE FROM op.availability a WHERE a.responder_id = p_id AND lower(a.period) >= v_now;
    IF p_available THEN
        INSERT INTO op.availability (responder_id, period, source)
        VALUES (p_id, tstzrange(v_now, v_now + interval '8 hours', '[)'), p_source);
        RETURN QUERY SELECT v_now, v_now + interval '8 hours';
    ELSE
        RETURN QUERY SELECT v_now, v_now;
    END IF;
END
$$;

CREATE OR REPLACE FUNCTION op.record_forward(p_incident uuid, p_facility text, p_gazetteer_version text, p_logic_version text)
RETURNS boolean
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_ev record;
BEGIN
    PERFORM 1 FROM op.incidents i WHERE i.id = p_incident FOR UPDATE;
    -- Never while suspended (O6): nothing new is forwarded. The share lock
    -- orders this after a suspension that is committing.
    IF (SELECT c.suspended FROM op.dispatch_control c FOR SHARE) THEN
        PERFORM op.reject('dispatch is suspended');
    END IF;
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

-- First acceptance per role wins (the partial unique index); a responder
-- holds one open assignment (the responder lock orders the busy check).
CREATE OR REPLACE FUNCTION op.accept_alert(p_alert uuid, p_responder uuid)
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
    -- Incident, then responder (issue_alert's order). NO KEY UPDATE leaves
    -- foreign-key checks on the row free and serialises acceptances.
    PERFORM 1 FROM op.responders r WHERE r.id = p_responder FOR NO KEY UPDATE;
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

-- Due incidents, most urgent first, each leased so a second worker skips it.
CREATE OR REPLACE FUNCTION op.claim_due_dispatch(p_limit int, p_lease interval)
RETURNS TABLE (incident_id uuid, gen bigint)
LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
    WITH due AS (
        SELECT t.incident_id, op.severity_rank(i.severity) AS urgency, t.due_at
          FROM op.dispatch_timers t JOIN op.incidents i ON i.id = t.incident_id
         WHERE t.due_at <= clock_timestamp() AND (t.lease_until IS NULL OR t.lease_until < clock_timestamp())
         ORDER BY op.severity_rank(i.severity) DESC, t.due_at
         LIMIT p_limit
           FOR UPDATE OF t SKIP LOCKED),
    claimed AS (
        UPDATE op.dispatch_timers t SET lease_until = clock_timestamp() + p_lease
          FROM due WHERE t.incident_id = due.incident_id
        RETURNING t.incident_id, t.gen, due.urgency, due.due_at)
    SELECT c.incident_id, c.gen FROM claimed c ORDER BY c.urgency DESC, c.due_at, c.incident_id
$$;

CREATE OR REPLACE FUNCTION op.register_responder(
    p_id uuid, p_role text, p_name_ct bytea, p_name_key_id text, p_phone_ct bytea, p_phone_key_id text,
    p_phone_mac bytea, p_phone_mac_key_id text, p_ward text, p_stage text, p_certified_at timestamptz,
    p_insured_until timestamptz, p_kit_ok boolean, p_research_consent boolean
) RETURNS void
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
BEGIN
    -- The gazetteer in force is the latest loaded version (CurrentGazetteer).
    IF NOT EXISTS (
        SELECT 1 FROM op.stages s
         WHERE s.id = p_stage AND s.ward_id = p_ward
           AND s.version = (SELECT c.version FROM op.config_versions c WHERE c.kind = 'gazetteer'
                             ORDER BY c.effective_from DESC, c.version DESC LIMIT 1)) THEN
        PERFORM op.reject('the stage is not in the responder''s ward in the gazetteer in force');
    END IF;
    INSERT INTO op.responders (id, role, name_ct, name_key_id, phone_ct, phone_key_id, phone_mac, phone_mac_key_id,
                               ward, stage, certified_at, insured_until, kit_ok, research_consent)
    VALUES (p_id, p_role, p_name_ct, p_name_key_id, p_phone_ct, p_phone_key_id, p_phone_mac, p_phone_mac_key_id,
            p_ward, p_stage, p_certified_at, p_insured_until, p_kit_ok, p_research_consent);
END
$$;
