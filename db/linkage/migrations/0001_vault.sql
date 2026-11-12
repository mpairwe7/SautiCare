-- Linkage vault (ISP §5.1, §5.5; Build Prompt rule 3.9; spec §6.1).
-- Study ID paired with the operational incident ID, nothing else. Opened only
-- by the PI and one named delegate (role lk_reader), only through
-- linkage.resolve_incident with one of the four allowed reasons, and every
-- attempt is logged for the monthly review by the Company Secretary.

CREATE TABLE linkage.pairs (
    study_id    uuid        PRIMARY KEY,
    incident_id uuid        NOT NULL UNIQUE,
    created_at  timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE linkage.access_log (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at           timestamptz NOT NULL DEFAULT clock_timestamp(),
    session_role text        NOT NULL,
    accessor     text        NOT NULL,
    reason       text        NOT NULL,
    study_id     uuid,
    incident_id  uuid,
    granted      boolean     NOT NULL
);

-- The four lawful reasons (ISP §5.5).
CREATE FUNCTION linkage.allowed_reason(p text) RETURNS boolean
LANGUAGE sql IMMUTABLE
AS $$ SELECT p IN ('withdrawal', 'data_subject_request', 'payment_dispute', 'lawful_rec_uncst_request') $$;

-- Resolve a study ID to its incident. A disallowed reason returns NULL and is
-- still logged (granted = false); every call leaves an access_log row.
CREATE FUNCTION linkage.resolve_incident(p_study_id uuid, p_reason text, p_accessor text) RETURNS uuid
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = linkage, pg_temp
AS $$
DECLARE
    v_inc uuid;
    v_ok  boolean := linkage.allowed_reason(p_reason) AND coalesce(p_accessor, '') <> '';
BEGIN
    IF v_ok THEN
        SELECT p.incident_id INTO v_inc FROM linkage.pairs p WHERE p.study_id = p_study_id;
    END IF;
    INSERT INTO linkage.access_log (session_role, accessor, reason, study_id, incident_id, granted)
    VALUES (session_user, coalesce(p_accessor, ''), coalesce(p_reason, ''), p_study_id, v_inc, v_ok);
    RETURN v_inc;
END
$$;

CREATE FUNCTION linkage.forbid_change() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'linkage access log is append-only (%)', TG_OP USING ERRCODE = 'SA403';
END
$$;
CREATE TRIGGER access_log_append_only BEFORE UPDATE OR DELETE ON linkage.access_log
    FOR EACH ROW EXECUTE FUNCTION linkage.forbid_change();

REVOKE ALL ON ALL TABLES IN SCHEMA linkage FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA linkage FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA linkage REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;
GRANT USAGE ON SCHEMA linkage TO lk_writer, lk_reader;
GRANT INSERT ON linkage.pairs TO lk_writer;                              -- pseudonymiser, insert-only
GRANT EXECUTE ON FUNCTION linkage.resolve_incident(uuid, text, text) TO lk_reader;
