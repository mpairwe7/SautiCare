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

-- The replaced helpers keep their existing grants (none to runtime roles:
-- only op.append_incident_event, SECURITY DEFINER, calls them).
