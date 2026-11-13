-- Part B review I1 (review focus 4; ADR-0027): an alert is delivered only
-- while its responder is still eligible. A responder who switches off, or
-- whose gate lapses (status, certification, insurance, kit), after the SMS
-- went out is not rung at 20 s: the voice rung is not due and no alert
-- attempt can be claimed; the alert lapses at the cascade timeout and the
-- cascade moves on. Stand-downs are always delivered.
--
-- op.begin_attempt is rebuilt on 0018's body (suspension under the share
-- lock on op.dispatch_control, pending alerts only); its signature and
-- grants are unchanged.

-- The same test as op.issue_alert's: active, certified, insured, kit ready
-- and available now.
CREATE FUNCTION op.responder_eligible_now(p_responder uuid) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
    SELECT EXISTS (
        SELECT 1 FROM op.responders r
         WHERE r.id = p_responder AND r.status = 'active'
           AND r.certified_at IS NOT NULL AND r.certified_at <= clock_timestamp()
           AND r.insured_until IS NOT NULL AND r.insured_until > clock_timestamp()
           AND r.kit_ok
           AND EXISTS (SELECT 1 FROM op.availability v WHERE v.responder_id = r.id AND v.period @> clock_timestamp()))
$$;

CREATE OR REPLACE FUNCTION op.begin_attempt(p_alert uuid, p_purpose text, p_channel text)
RETURNS TABLE (attempt_id uuid, fresh boolean)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_id uuid;
BEGIN
    -- An alert is delivered only while it is pending, dispatch is not
    -- suspended and its responder is still eligible; otherwise nothing is
    -- claimed (fresh = false, no ID) and the alert lapses. Stand-downs are
    -- always delivered.
    IF p_purpose = 'alert' AND ((SELECT c.suspended FROM op.dispatch_control c FOR SHARE)
       OR NOT EXISTS (SELECT 1 FROM op.alerts a WHERE a.id = p_alert AND a.outcome = 'pending'
                         AND op.responder_eligible_now(a.responder_id))) THEN
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

REVOKE ALL ON FUNCTION op.responder_eligible_now(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION op.responder_eligible_now(uuid) TO op_dispatch, op_ivr;
