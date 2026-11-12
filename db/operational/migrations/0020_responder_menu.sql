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
-- responder's pending alert and current availability, nothing more.

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
