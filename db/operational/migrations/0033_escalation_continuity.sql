-- UX-1 (ADR-0034; ADR-0029 rule 8): escalation continuity.
--
-- 1. The coordinator call announces every reason not yet acknowledged (F-06).
--    0022's claim read only the incident's latest escalation, while one
--    acknowledgement covers every earlier one: a reason could be
--    acknowledged unheard. The claim now returns each distinct reason with
--    seq > acked_seq, in seq order, with the role of its newest event ('' for
--    none) and the newest seq, which the call's acknowledgement covers: a
--    reason recorded after the claim rings again. It also returns the
--    severity (UX-3 speaks it and the roles; returning them now avoids a
--    second DROP). The return type changes, so the function is dropped and
--    created; 0022's body is otherwise unchanged.
-- 2. intake-gw (op_intake) reads which escalations are unacknowledged, to
--    export the dead-man gauges (tier 3, F-01); intake on either channel
--    (op_intake, op_ivr) reads it when a merge must carry an unacknowledged
--    escalation to the survivor (F-06).
DROP FUNCTION op.claim_coordinator_calls(interval, int);

CREATE FUNCTION op.claim_coordinator_calls(p_retry interval, p_limit int)
RETURNS TABLE (incident_id uuid, seq bigint, reasons text[], roles text[], reference text, calls int, severity text)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
#variable_conflict use_column
DECLARE
    r       record;
    v_calls int;
BEGIN
    FOR r IN
        SELECT i.id AS inc, l.seq AS s, l.reasons AS why, l.roles AS rl, i.reference AS ref, i.severity AS sev
          FROM op.incidents i
          LEFT JOIN op.coordinator_calls c ON c.incident_id = i.id
          CROSS JOIN LATERAL (
              SELECT max(d.seq) AS seq, max(d.recorded_at) AS recorded_at,
                     array_agg(d.reason ORDER BY d.seq) AS reasons, array_agg(d.role ORDER BY d.seq) AS roles
                FROM (SELECT DISTINCT ON (e.payload ->> 'reason') e.payload ->> 'reason' AS reason,
                             coalesce(e.payload ->> 'role', '') AS role, e.seq, e.recorded_at
                        FROM op.incident_events e
                       WHERE e.incident_id = i.id AND e.type = 'incident.escalated' AND e.seq > coalesce(c.acked_seq, 0)
                       ORDER BY e.payload ->> 'reason', e.seq DESC) d) l
         WHERE i.state NOT IN ('closed', 'merged')
           AND l.seq IS NOT NULL
           AND (c.last_call_at IS NULL OR c.last_call_at < clock_timestamp() - p_retry)
         ORDER BY CASE i.severity WHEN 'red' THEN 0 WHEN 'yellow' THEN 1 WHEN 'green' THEN 2 ELSE 0 END, l.recorded_at
         LIMIT p_limit
    LOOP
        INSERT INTO op.coordinator_calls AS cc (incident_id, calls, last_call_at) VALUES (r.inc, 1, clock_timestamp())
        ON CONFLICT (incident_id) DO UPDATE SET calls = cc.calls + 1, last_call_at = clock_timestamp()
        RETURNING cc.calls INTO v_calls;
        incident_id := r.inc;
        seq := r.s;
        reasons := r.why;
        roles := r.rl;
        reference := r.ref;
        calls := v_calls;
        severity := coalesce(r.sev, '');
        RETURN NEXT;
    END LOOP;
END
$$;

REVOKE ALL ON FUNCTION op.claim_coordinator_calls(interval, int) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION op.claim_coordinator_calls(interval, int) TO op_ivr;

GRANT SELECT (incident_id, acked_seq) ON op.coordinator_calls TO op_intake, op_ivr;

-- 3. intake-gw's watchdog (op_intake) sees a dispatch timer overdue and
--    unleased: a stall after T3 (F-05).
GRANT SELECT (incident_id, due_at, lease_until) ON op.dispatch_timers TO op_intake;
