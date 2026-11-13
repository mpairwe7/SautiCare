-- Forwarding calls and coordinator escalation calls (M2 Part B Task 11;
-- Build Prompt §10.4; scenarios 7, 8, 11; ADR-0027).
--
-- * A forwarded facility gets one SMS (dispatch-worker) and one call
--   (ivr-controller; press 1 to acknowledge), each attempt claimed once; a
--   facility that acknowledged or timed out is not called.
-- * Every escalation rings the coordinator line, durable and retried every
--   retry interval until a coordinator presses 1; one acknowledgement covers
--   the incident's escalations recorded so far, a later one rings again.

CREATE TABLE op.forward_attempts (
    id           uuid        PRIMARY KEY,
    incident_id  uuid        NOT NULL,
    facility_id  text        NOT NULL,
    channel      text        NOT NULL CHECK (channel IN ('sms', 'voice')),
    status       text        NOT NULL DEFAULT 'claimed' CHECK (status IN ('claimed', 'sent', 'failed', 'unknown')),
    provider_ref text        CHECK (provider_ref ~ '^[A-Za-z0-9_.:-]{1,128}$'),
    claimed_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    settled_at   timestamptz,
    UNIQUE (incident_id, facility_id, channel),
    FOREIGN KEY (incident_id, facility_id) REFERENCES op.forwards (incident_id, facility_id),
    CHECK ((status = 'claimed') = (settled_at IS NULL))
);

CREATE FUNCTION op.begin_forward_attempt(p_incident uuid, p_facility text, p_channel text)
RETURNS TABLE (attempt_id uuid, fresh boolean)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_id uuid;
BEGIN
    -- Only a recorded forward; a call only while it awaits acknowledgement.
    IF NOT EXISTS (SELECT 1 FROM op.forwards f
                    WHERE f.incident_id = p_incident AND f.facility_id = p_facility
                      AND (p_channel = 'sms' OR (f.acknowledged_at IS NULL AND f.timed_out_at IS NULL))) THEN
        RETURN QUERY SELECT NULL::uuid, false;
        RETURN;
    END IF;
    INSERT INTO op.forward_attempts (id, incident_id, facility_id, channel) VALUES (uuidv7(), p_incident, p_facility, p_channel)
    ON CONFLICT ON CONSTRAINT forward_attempts_incident_id_facility_id_channel_key DO NOTHING
    RETURNING id INTO v_id;
    IF v_id IS NOT NULL THEN
        RETURN QUERY SELECT v_id, true;
        RETURN;
    END IF;
    RETURN QUERY SELECT x.id, false FROM op.forward_attempts x
                  WHERE x.incident_id = p_incident AND x.facility_id = p_facility AND x.channel = p_channel;
END
$$;

CREATE FUNCTION op.finish_forward_attempt(p_attempt uuid, p_status text, p_provider_ref text) RETURNS boolean
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
BEGIN
    IF p_status NOT IN ('sent', 'failed', 'unknown') THEN
        PERFORM op.reject('an attempt settles as sent, failed or unknown');
    END IF;
    UPDATE op.forward_attempts x SET status = p_status, provider_ref = p_provider_ref, settled_at = clock_timestamp()
     WHERE x.id = p_attempt AND x.status = 'claimed';
    RETURN FOUND;
END
$$;

CREATE TABLE op.coordinator_calls (
    incident_id     uuid        PRIMARY KEY REFERENCES op.incidents (id),
    acked_seq       bigint      NOT NULL DEFAULT 0,
    calls           int         NOT NULL DEFAULT 0, -- calls since the last acknowledgement
    last_call_at    timestamptz,
    acknowledged_at timestamptz
);

CREATE INDEX incident_events_escalated ON op.incident_events (recorded_at) WHERE type = 'incident.escalated';

-- Claim the coordinator calls due now: for each open incident, its latest
-- escalation not yet acknowledged, rung when never rung since the last
-- acknowledgement or not for p_retry; the most severe first, then the
-- oldest; at most p_limit (ivr-controller passes the free lines, so one line
-- is never rung twice at once). Escalations of the last day (older ones are
-- on the console's list); a closed or merged incident is not rung about.
CREATE FUNCTION op.claim_coordinator_calls(p_retry interval, p_limit int)
RETURNS TABLE (incident_id uuid, seq bigint, reason text, reference text, calls int)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
#variable_conflict use_column
DECLARE
    r       record;
    v_calls int;
BEGIN
    FOR r IN
        SELECT l.incident_id AS inc, l.seq AS s, l.reason AS why, i.reference AS ref
          FROM (SELECT DISTINCT ON (e.incident_id) e.incident_id, e.seq, e.payload ->> 'reason' AS reason, e.recorded_at
                  FROM op.incident_events e
                  LEFT JOIN op.coordinator_calls c ON c.incident_id = e.incident_id
                 WHERE e.type = 'incident.escalated' AND e.recorded_at > clock_timestamp() - interval '1 day'
                   AND e.seq > coalesce(c.acked_seq, 0)
                 ORDER BY e.incident_id, e.seq DESC) l
          JOIN op.incidents i ON i.id = l.incident_id
          LEFT JOIN op.coordinator_calls c ON c.incident_id = l.incident_id
         WHERE (c.last_call_at IS NULL OR c.last_call_at < clock_timestamp() - p_retry)
           AND i.state NOT IN ('closed', 'merged')
         ORDER BY CASE i.severity WHEN 'red' THEN 0 WHEN 'yellow' THEN 1 WHEN 'green' THEN 2 ELSE 0 END, l.recorded_at
         LIMIT p_limit
    LOOP
        INSERT INTO op.coordinator_calls AS cc (incident_id, calls, last_call_at) VALUES (r.inc, 1, clock_timestamp())
        ON CONFLICT (incident_id) DO UPDATE SET calls = cc.calls + 1, last_call_at = clock_timestamp()
        RETURNING cc.calls INTO v_calls;
        incident_id := r.inc;
        seq := r.s;
        reason := r.why;
        reference := r.ref;
        calls := v_calls;
        RETURN NEXT;
    END LOOP;
END
$$;

-- A coordinator pressed 1: the incident's escalations up to p_seq are
-- acknowledged; a later escalation rings again at once.
CREATE FUNCTION op.acknowledge_coordinator_call(p_incident uuid, p_seq bigint) RETURNS void
LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
    UPDATE op.coordinator_calls c
       SET acked_seq = greatest(c.acked_seq, p_seq), calls = 0, last_call_at = NULL, acknowledged_at = clock_timestamp()
     WHERE c.incident_id = p_incident
$$;

GRANT EXECUTE ON FUNCTION op.begin_forward_attempt(uuid, text, text), op.finish_forward_attempt(uuid, text, text)
    TO op_dispatch, op_ivr;
GRANT EXECUTE ON FUNCTION op.claim_coordinator_calls(interval, int), op.acknowledge_coordinator_call(uuid, bigint) TO op_ivr;
GRANT SELECT ON op.forwards TO op_ivr;
GRANT SELECT ON op.forward_attempts TO op_dispatch, op_ivr, op_readonly;
GRANT SELECT ON op.coordinator_calls TO op_readonly;
