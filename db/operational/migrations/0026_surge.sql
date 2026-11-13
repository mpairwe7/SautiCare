-- Surge (M2 Part C Task 14; Build Prompt §10.4; ADR-0028): more emergencies
-- than responders. Incidents queue by severity, then by T2 — the oldest call
-- first — and nothing is dropped: an incident with a role still open wakes
-- every ring interval until someone accepts (ADR-0024). The surge flag
-- compares the incidents waiting for a first acceptance with the responders
-- free now; it feeds sautictl and the console (M5).

-- claim_due_dispatch rebuilt on 0023's body: most severe first, then the
-- earliest T2 (it was the earliest wake), then the earliest wake.
CREATE OR REPLACE FUNCTION op.claim_due_dispatch(p_limit int, p_lease interval)
RETURNS TABLE (incident_id uuid, gen bigint)
LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
    WITH due AS (
        SELECT t.incident_id, op.severity_rank(i.severity) AS urgency, i.t2, t.due_at
          FROM op.dispatch_timers t JOIN op.incidents i ON i.id = t.incident_id
         WHERE t.due_at <= clock_timestamp() AND (t.lease_until IS NULL OR t.lease_until < clock_timestamp())
         ORDER BY op.severity_rank(i.severity) DESC, i.t2 NULLS LAST, t.due_at
         LIMIT p_limit
           FOR UPDATE OF t SKIP LOCKED),
    claimed AS (
        UPDATE op.dispatch_timers t SET lease_until = clock_timestamp() + p_lease
          FROM due WHERE t.incident_id = due.incident_id
        RETURNING t.incident_id, t.gen, due.urgency, due.t2, due.due_at)
    SELECT c.incident_id, c.gen FROM claimed c ORDER BY c.urgency DESC, c.t2 NULLS LAST, c.due_at, c.incident_id
$$;

-- Waiting: incidents in dispatch with no acceptance yet. Available:
-- responders eligible now (op.responder_eligible_now) who hold no open
-- assignment. Surge: more waiting than available.
CREATE FUNCTION op.dispatch_surge() RETURNS TABLE (waiting int, available int, surge boolean)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
    WITH w AS (SELECT count(*)::int AS n FROM op.incidents i WHERE i.state = 'dispatching'),
         a AS (SELECT count(*)::int AS n FROM op.responders r
                WHERE op.responder_eligible_now(r.id)
                  AND NOT EXISTS (SELECT 1 FROM op.assignments s JOIN op.incidents i ON i.id = s.incident_id
                                   WHERE s.responder_id = r.id AND s.released_at IS NULL AND i.state NOT IN ('closed', 'merged')))
    SELECT w.n, a.n, w.n > a.n FROM w, a
$$;

CREATE INDEX incidents_dispatching ON op.incidents (t2) WHERE state = 'dispatching';

REVOKE ALL ON FUNCTION op.dispatch_surge() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION op.dispatch_surge() TO op_readonly, op_console, op_dispatch;
