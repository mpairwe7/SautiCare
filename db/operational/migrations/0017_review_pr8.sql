-- Review fixes to deduplication (CodeRabbit, PR #8; ADR-0022).
--
-- op.append_incident_event is not replaced here: only the helpers it calls,
-- with their signatures unchanged.
--
-- Absorbed and raising calls are real members. merge_absorbed names a call
-- whose own row is merged into this survivor and carries that call's stored
-- T1, so no caller can attach an unrelated call or lower the survivor's T1
-- (the start of the primary outcome) with a forged timestamp. severity_raised
-- names a call in the survivor's absorbed set, merged into it, whose own
-- severity is exactly the one raised. internal/incident Apply checks the
-- absorbed set too; the other incident's row is visible only here. Both
-- helpers now read op.incidents, so they are VOLATILE (FOR SHARE: the member
-- cannot change before the event commits).

CREATE OR REPLACE FUNCTION op.check_merge_absorbed(p jsonb, p_incident uuid, p_absorbed jsonb) RETURNS void
LANGUAGE plpgsql VOLATILE
SET search_path = op, pg_temp
AS $$
DECLARE
    v_member op.incidents%ROWTYPE;
BEGIN
    IF jsonb_typeof(p) IS DISTINCT FROM 'object'
       OR EXISTS (SELECT 1 FROM jsonb_object_keys(p) k WHERE k NOT IN ('from', 't1'))
       OR jsonb_typeof(p -> 'from') IS DISTINCT FROM 'string'
       OR (p ->> 'from') !~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
       OR jsonb_typeof(p -> 't1') IS DISTINCT FROM 'string'
       OR (p ->> 't1') !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,6})?(Z|[+-][0-9]{2}:[0-9]{2})$' THEN
        PERFORM op.reject('merge_absorbed needs exactly "from" (incident ID) and "t1" (timestamp)');
    END IF;
    IF (p ->> 'from')::uuid = p_incident OR p_absorbed ? ((p ->> 'from')::uuid)::text THEN
        PERFORM op.reject('merge_absorbed: the call is already absorbed or is this incident');
    END IF;
    -- The member's incident.merged is appended first, in the same
    -- transaction (opstore.DispatchOrMerge).
    SELECT * INTO v_member FROM op.incidents i WHERE i.id = (p ->> 'from')::uuid FOR SHARE;
    IF NOT FOUND OR v_member.merged_into IS DISTINCT FROM p_incident THEN
        PERFORM op.reject('merge_absorbed: the call is not merged into this incident');
    END IF;
    IF v_member.t1 IS DISTINCT FROM (p ->> 't1')::timestamptz THEN
        PERFORM op.reject('merge_absorbed: t1 is not the merged call''s T1');
    END IF;
END
$$;

CREATE OR REPLACE FUNCTION op.check_severity_raised(p jsonb, p_incident uuid, p_current text) RETURNS void
LANGUAGE plpgsql VOLATILE
SET search_path = op, pg_temp
AS $$
DECLARE
    v_member op.incidents%ROWTYPE;
BEGIN
    IF jsonb_typeof(p) IS DISTINCT FROM 'object'
       OR EXISTS (SELECT 1 FROM jsonb_object_keys(p) k WHERE k NOT IN ('severity', 'from'))
       OR jsonb_typeof(p -> 'severity') IS DISTINCT FROM 'string'
       OR op.severity_rank(p ->> 'severity') = 0
       OR jsonb_typeof(p -> 'from') IS DISTINCT FROM 'string'
       OR (p ->> 'from') !~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$' THEN
        PERFORM op.reject('severity_raised needs exactly "severity" (red, yellow or green) and "from" (incident ID)');
    END IF;
    IF (p ->> 'from')::uuid = p_incident THEN
        PERFORM op.reject('severity_raised: an incident cannot raise itself');
    END IF;
    -- The survivor's row is already locked by the append that called us.
    IF NOT EXISTS (SELECT 1 FROM op.incidents i WHERE i.id = p_incident AND i.absorbed ? ((p ->> 'from')::uuid)::text) THEN
        PERFORM op.reject('severity_raised: the call was not merged into this incident');
    END IF;
    SELECT * INTO v_member FROM op.incidents i WHERE i.id = (p ->> 'from')::uuid FOR SHARE;
    IF NOT FOUND OR v_member.merged_into IS DISTINCT FROM p_incident THEN
        PERFORM op.reject('severity_raised: the call is not merged into this incident');
    END IF;
    IF v_member.severity IS DISTINCT FROM p ->> 'severity' THEN
        PERFORM op.reject('severity_raised: the severity is not the merged call''s');
    END IF;
    IF op.severity_rank(p ->> 'severity') <= op.severity_rank(p_current) THEN
        PERFORM op.reject('severity_raised must raise the severity');
    END IF;
END
$$;

-- A split is keyed by the merge it undoes: the member's latest
-- incident.merged event. A retry finds the same event and the same keys (a
-- no-op); a later merge of the same pair is a new event, so its split gets
-- new keys instead of returning the first split's events as duplicates while
-- the call stays merged. The member is locked first, so no merge can land
-- between reading its latest merge and the split, and the lock order
-- (member, then survivor) is the merge path's.
CREATE OR REPLACE FUNCTION op.split_incident(p_survivor uuid, p_member uuid, p_staff uuid) RETURNS void
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_merge uuid;
    v_key   text;
BEGIN
    IF p_staff IS NULL THEN
        PERFORM op.reject('a split needs the coordinator''s staff identity');
    END IF;
    PERFORM 1 FROM op.incidents i WHERE i.id = p_member FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'unknown incident %', p_member USING ERRCODE = 'SA404';
    END IF;
    SELECT e.id INTO v_merge FROM op.incident_events e
     WHERE e.incident_id = p_member AND e.type = 'incident.merged'
     ORDER BY e.seq DESC
     LIMIT 1;
    IF v_merge IS NULL THEN
        PERFORM op.reject('split: the call was never merged');
    END IF;
    v_key := 'split:' || p_survivor || ':' || p_member || ':' || v_merge;
    PERFORM op.append_incident_event(p_survivor, 0, 'incident.split', jsonb_build_object('from', p_member),
                                     'staff', p_staff, v_key || ':split', NULL);
    PERFORM op.append_incident_event(p_member, 0, 'incident.unmerged', jsonb_build_object('from', p_survivor),
                                     'staff', p_staff, v_key || ':unmerge', NULL);
END
$$;

-- Every dispatch decision reads its merge candidates (DedupCandidates) under
-- the dedup advisory lock: incidents already dispatching, of one category,
-- with T1 in a window. A partial index over exactly those incidents keeps
-- that read small however many closed or merged incidents are retained.
CREATE INDEX incidents_dedup ON op.incidents (category, t1)
    WHERE t2 IS NOT NULL AND state NOT IN ('closed', 'merged');

-- The merge candidates are read and locked in one statement. A candidate
-- chosen from an unlocked read could close, merge, or lose an absorbed call
-- to a split (its T1 moving later) before the merge is recorded: the merge
-- then failed and rolled T2 back, or joined a survivor no longer within the
-- window. FOR SHARE makes the read wait for any transaction changing a
-- candidate and re-evaluates the conditions on the row it committed
-- (READ COMMITTED), so the rule decides on current rows, which then cannot
-- change until the merge commits. FOR SHARE needs UPDATE on op.incidents,
-- which no runtime role has; this function, SECURITY DEFINER, is the only
-- way the intake roles lock incidents outside the append. The caller holds
-- the dedup advisory lock (opstore.DispatchOrMerge).
CREATE FUNCTION op.dedup_candidates(p_category text, p_col int, p_row int, p_dist int, p_t1 timestamptz,
                                    p_window_seconds int, p_id uuid)
RETURNS TABLE (id uuid, category text, zone_col int, zone_row int, location_quality text, severity text,
               escalated boolean, t1 timestamptz, t2 timestamptz)
LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
    SELECT i.id, i.category, i.zone_col, i.zone_row, i.location_quality, i.severity, i.escalated, i.t1, i.t2
      FROM op.incidents i
     WHERE i.category = p_category AND i.t2 IS NOT NULL AND i.state NOT IN ('closed', 'merged')
       AND i.zone_col BETWEEN p_col - p_dist AND p_col + p_dist
       AND i.zone_row BETWEEN p_row - p_dist AND p_row + p_dist
       AND i.t1 BETWEEN p_t1 - p_window_seconds * interval '1 second' AND p_t1 + p_window_seconds * interval '1 second'
       AND i.id <> p_id
       FOR SHARE
$$;

-- Intake runs the dedup decision; the IVR controller runs the same intake
-- path (granted ahead of its milestone, like 0007-0009).
GRANT EXECUTE ON FUNCTION op.dedup_candidates(text, int, int, int, timestamptz, int, uuid) TO op_intake, op_ivr;

-- The replaced functions keep their existing grants: the helpers none to
-- runtime roles (only op.append_incident_event, SECURITY DEFINER, calls
-- them), op.split_incident EXECUTE to op_console only.
