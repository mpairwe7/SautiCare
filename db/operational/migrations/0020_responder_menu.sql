-- The responder menu (M2 Part B Task 9; Build Prompt §10.4, §10.5;
-- decision 6; ADR-0027).
--
-- A decline may carry a reason code: the responder service offers the
-- configured closed list per role (config/gated/responder.yaml) only once the
-- amendment for decision 6 is approved, and the database keeps the code on
-- the alert and in the alert.declined event (scenario 10). Free text is
-- impossible: a reason is a code.
--
-- intake-gw and ivr-controller serve the responder's own menu: they read the
-- responder's own alert and current availability, nothing more.
--
-- A menu session is pinned to one alert at its first screen
-- (op.responder_menu_alert), so a newer alert arriving mid-session cannot
-- redirect an answer already under way; the first screen shows the newest
-- pending alert of the hour, else the responder's active assignment (to see
-- where to go again), else the newest alert of the hour.

CREATE OR REPLACE FUNCTION op.decline_alert(p_alert uuid, p_responder uuid, p_reason text) RETURNS text
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
    IF p_reason IS NOT NULL AND p_reason !~ '^[a-z][a-z0-9_.-]{0,63}$' THEN
        PERFORM op.reject('a decline reason is a code from the configured list');
    END IF;
    SELECT * INTO v_ev FROM op.append_event_core(v_a.incident_id, NULL, 'alert.declined',
        CASE WHEN p_reason IS NULL THEN jsonb_build_object('alert', v_a.id, 'responder', v_a.responder_id)
             ELSE jsonb_build_object('alert', v_a.id, 'responder', v_a.responder_id, 'reason', p_reason) END,
        'responder', NULL, 'decline:' || v_a.id, v_a.logic_version);
    UPDATE op.alerts x SET outcome = 'declined', decline_reason = p_reason, responded_at = v_ev.recorded_at, closed_at = v_ev.recorded_at
     WHERE x.id = v_a.id;
    PERFORM op.wake_dispatch(v_a.incident_id);
    RETURN 'declined';
END
$$;

GRANT SELECT (id, incident_id, responder_id, role, outcome, sent_at) ON op.alerts TO op_intake, op_ivr;
GRANT SELECT ON op.availability TO op_intake, op_ivr;

CREATE TABLE op.responder_menu_pins (
    session_key  bytea       NOT NULL CHECK (octet_length(session_key) = 32), -- SHA-256 of provider and session ID
    responder_id uuid        NOT NULL REFERENCES op.responders (id),
    alert_id     uuid        REFERENCES op.alerts (id),                       -- NULL: the session began with no alert
    pinned_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (session_key, responder_id)
);
CREATE INDEX responder_menu_pins_age ON op.responder_menu_pins (pinned_at);

CREATE FUNCTION op.responder_menu_alert(p_session bytea, p_responder uuid)
RETURNS TABLE (alert_id uuid, incident_id uuid, role text, outcome text, reference text, severity text, category text,
               ward text, landmark text, language text, assigned boolean)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
#variable_conflict use_column
DECLARE
    v_alert  uuid;
    v_pinned boolean := false;
BEGIN
    SELECT p.alert_id, true INTO v_alert, v_pinned FROM op.responder_menu_pins p
     WHERE p.session_key = p_session AND p.responder_id = p_responder;
    IF NOT coalesce(v_pinned, false) THEN
        DELETE FROM op.responder_menu_pins p WHERE p.pinned_at < clock_timestamp() - interval '1 hour';
        SELECT a.id INTO v_alert FROM op.alerts a
         WHERE a.responder_id = p_responder AND a.outcome = 'pending' AND a.sent_at > clock_timestamp() - interval '1 hour'
         ORDER BY a.sent_at DESC LIMIT 1;
        IF v_alert IS NULL THEN
            SELECT s.alert_id INTO v_alert FROM op.assignments s JOIN op.incidents i ON i.id = s.incident_id
             WHERE s.responder_id = p_responder AND s.released_at IS NULL AND i.state NOT IN ('closed', 'merged')
             ORDER BY s.accepted_at DESC LIMIT 1;
        END IF;
        IF v_alert IS NULL THEN
            SELECT a.id INTO v_alert FROM op.alerts a
             WHERE a.responder_id = p_responder AND a.sent_at > clock_timestamp() - interval '1 hour'
             ORDER BY a.sent_at DESC LIMIT 1;
        END IF;
        INSERT INTO op.responder_menu_pins (session_key, responder_id, alert_id) VALUES (p_session, p_responder, v_alert)
        ON CONFLICT (session_key, responder_id) DO NOTHING;
        -- A concurrent first callback may have pinned first: its pin stands.
        v_alert := NULL;
        SELECT p.alert_id INTO v_alert FROM op.responder_menu_pins p
         WHERE p.session_key = p_session AND p.responder_id = p_responder;
    END IF;
    RETURN QUERY
    SELECT a.id, a.incident_id, a.role, a.outcome, i.reference, i.severity, i.category, i.ward, i.landmark, i.language,
           (i.state NOT IN ('closed', 'merged') AND EXISTS (SELECT 1 FROM op.assignments s WHERE s.alert_id = a.id AND s.released_at IS NULL))
      FROM op.alerts a JOIN op.incidents i ON i.id = a.incident_id
     WHERE a.id = v_alert AND a.responder_id = p_responder;
END
$$;

GRANT EXECUTE ON FUNCTION op.responder_menu_alert(bytea, uuid) TO op_intake;
GRANT SELECT ON op.responder_menu_pins TO op_readonly;
