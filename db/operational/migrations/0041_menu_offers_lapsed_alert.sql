-- The responder menu offers a lapsed alert while its role is still open
-- (UX audit F-26; the menu half of docs/superpowers/plans/2026-11-13-ux-hardening.md
-- Task 3.1, which reserved 0034 for the whole task: this file carries only the
-- menu change, so the rest of that task keeps its number).
--
-- op.accept_alert already counts a late acceptance after a lapse while the
-- role is open (0015, kept by 0023): a rider who answers the SMS two minutes
-- late still helps. The menu hid it: op.responder_menu_alert returned the
-- lapsed alert with outcome no_response, and the service offered Accept only
-- for a pending one (internal/respond/ussd.go). The rider read "no active
-- alert" and went home while nobody else could be found.
--
-- The function now also returns actionable: true for a no_response alert
-- whose incident is still looking for help (dispatching, assigned or
-- on_scene) and whose role has no open assignment, exactly when
-- op.accept_alert would record the acceptance. It is computed when read, so a
-- pinned session sees the role being taken in the meantime. At the first
-- screen the order is: the responder's open assignment, the newest pending
-- alert, the newest actionable one, the newest alert of the hour.
--
-- Whether a late acceptance should count is the PI's policy (audit R-6); the
-- store has counted it since 0015, and this makes the menu agree with it.
--
-- The return type changes, so the function is dropped and created (as 0033
-- does); its grant is restored below.

DROP FUNCTION op.responder_menu_alert(bytea, uuid);

CREATE FUNCTION op.responder_menu_alert(p_session bytea, p_responder uuid)
RETURNS TABLE (alert_id uuid, incident_id uuid, role text, outcome text, reference text, severity text, category text,
               ward text, landmark text, language text, assigned boolean, actionable boolean)
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
        SELECT s.alert_id INTO v_alert FROM op.assignments s JOIN op.incidents i ON i.id = s.incident_id
         WHERE s.responder_id = p_responder AND s.released_at IS NULL AND i.state NOT IN ('closed', 'merged')
         ORDER BY s.accepted_at DESC LIMIT 1;
        IF v_alert IS NULL THEN
            SELECT a.id INTO v_alert FROM op.alerts a
             WHERE a.responder_id = p_responder AND a.outcome = 'pending' AND a.sent_at > clock_timestamp() - interval '1 hour'
             ORDER BY a.sent_at DESC LIMIT 1;
        END IF;
        IF v_alert IS NULL THEN
            SELECT a.id INTO v_alert FROM op.alerts a JOIN op.incidents i ON i.id = a.incident_id
             WHERE a.responder_id = p_responder AND a.outcome = 'no_response'
               AND a.sent_at > clock_timestamp() - interval '1 hour'
               AND i.state IN ('dispatching', 'assigned', 'on_scene')
               AND NOT EXISTS (SELECT 1 FROM op.assignments s
                                WHERE s.incident_id = a.incident_id AND s.role = a.role AND s.released_at IS NULL)
             ORDER BY a.sent_at DESC LIMIT 1;
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
           (i.state NOT IN ('closed', 'merged') AND EXISTS (SELECT 1 FROM op.assignments s WHERE s.alert_id = a.id AND s.released_at IS NULL)),
           (a.outcome = 'no_response' AND i.state IN ('dispatching', 'assigned', 'on_scene')
            AND NOT EXISTS (SELECT 1 FROM op.assignments s
                             WHERE s.incident_id = a.incident_id AND s.role = a.role AND s.released_at IS NULL))
      FROM op.alerts a JOIN op.incidents i ON i.id = a.incident_id
     WHERE a.id = v_alert AND a.responder_id = p_responder;
END
$$;

REVOKE ALL ON FUNCTION op.responder_menu_alert(bytea, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION op.responder_menu_alert(bytea, uuid) TO op_intake;
