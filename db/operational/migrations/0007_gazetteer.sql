-- Gazetteer reference data (Build Prompt §9, §12; M1 Task 3).
--
-- Each ward's landmark list maps to a GPS point and a 500 m UTM 36N cell,
-- with English and Luganda names and normalised aliases for pg_trgm fuzzy
-- matching. No personal data. Versions are immutable once loaded: the
-- gazetteer is frozen during the study window, and a correction is a new
-- version plus a deviation record. Only the store owner loads versions
-- (`sautictl gazetteer load`); runtime roles read.

-- pg_trgm is a trusted extension: its script runs as the bootstrap
-- superuser, which owns its functions and leaves them executable by PUBLIC;
-- the store owner cannot revoke that. So the extension gets its own schema,
-- owned by the store owner, with no USAGE for anyone else. Calling a function
-- needs USAGE on its schema, so runtime roles reach pg_trgm only through the
-- SECURITY DEFINER op.match_landmark below.
CREATE SCHEMA trgm;
REVOKE ALL ON SCHEMA trgm FROM PUBLIC;
CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA trgm;

CREATE TABLE op.gazetteer_versions (
    version   text        PRIMARY KEY CHECK (version <> ''),
    sha256    text        NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    status    text        NOT NULL CHECK (status IN ('placeholder', 'approved')),
    loaded_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE op.wards (
    version    text NOT NULL REFERENCES op.gazetteer_versions (version),
    id         text NOT NULL CHECK (id ~ '^[a-z][a-z0-9_.-]{0,63}$' AND id NOT IN ('elsewhere', 'other')),
    menu_order int  NOT NULL CHECK (menu_order >= 1),
    name_en    text NOT NULL CHECK (name_en <> ''),
    name_lg    text NOT NULL CHECK (name_lg <> ''),
    centre_col int  NOT NULL,
    centre_row int  NOT NULL,
    PRIMARY KEY (version, id),
    UNIQUE (version, menu_order)
);

CREATE TABLE op.ward_zones (
    version  text NOT NULL,
    ward_id  text NOT NULL,
    zone_col int  NOT NULL,
    zone_row int  NOT NULL,
    PRIMARY KEY (version, ward_id, zone_col, zone_row),
    FOREIGN KEY (version, ward_id) REFERENCES op.wards (version, id)
);

CREATE TABLE op.landmarks (
    version    text             NOT NULL,
    id         text             NOT NULL CHECK (id ~ '^[a-z][a-z0-9_.-]{0,63}$' AND id NOT IN ('elsewhere', 'other')),
    list_order int              NOT NULL,
    ward_id    text             NOT NULL,
    name_en    text             NOT NULL CHECK (name_en <> ''),
    name_lg    text             NOT NULL CHECK (name_lg <> ''),
    lat        double precision NOT NULL,
    lon        double precision NOT NULL,
    zone_col   int              NOT NULL,
    zone_row   int              NOT NULL,
    menu       int              NOT NULL DEFAULT 0 CHECK (menu BETWEEN 0 AND 5),
    PRIMARY KEY (version, id),
    UNIQUE (version, list_order),
    FOREIGN KEY (version, ward_id) REFERENCES op.wards (version, id)
);

CREATE UNIQUE INDEX landmarks_menu_slot ON op.landmarks (version, ward_id, menu) WHERE menu > 0;

CREATE TABLE op.landmark_aliases (
    version     text NOT NULL,
    landmark_id text NOT NULL,
    alias_order int  NOT NULL,
    ward_id     text NOT NULL,
    alias_norm  text NOT NULL CHECK (alias_norm ~ '^[a-z0-9]+( [a-z0-9]+)*$'),
    PRIMARY KEY (version, landmark_id, alias_order),
    UNIQUE (version, landmark_id, alias_norm),
    FOREIGN KEY (version, landmark_id) REFERENCES op.landmarks (version, id)
);

CREATE INDEX landmark_aliases_ward ON op.landmark_aliases (version, ward_id);
CREATE INDEX landmark_aliases_trgm ON op.landmark_aliases USING gin (alias_norm trgm.gin_trgm_ops);

-- Frozen: no UPDATE, DELETE or TRUNCATE, even by the owner.
CREATE FUNCTION op.gazetteer_frozen() RETURNS trigger
LANGUAGE plpgsql
SET search_path = op, pg_temp
AS $$
BEGIN
    RAISE EXCEPTION 'gazetteer is frozen: load a new version (a correction during the study window needs a deviation record)'
        USING ERRCODE = 'SA403';
END
$$;

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['gazetteer_versions', 'wards', 'ward_zones', 'landmarks', 'landmark_aliases'] LOOP
        EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE OR DELETE ON op.%I FOR EACH ROW EXECUTE FUNCTION op.gazetteer_frozen()',
                       t || '_frozen', t);
        EXECUTE format('CREATE TRIGGER %I BEFORE TRUNCATE ON op.%I FOR EACH STATEMENT EXECUTE FUNCTION op.gazetteer_frozen()',
                       t || '_frozen_truncate', t);
    END LOOP;
END
$$;

-- Best alias match within one ward. SECURITY DEFINER: only the owner has
-- USAGE on schema trgm.
CREATE FUNCTION op.match_landmark(p_version text, p_ward text, p_text text)
RETURNS TABLE (landmark_id text, similarity real)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
    SELECT a.landmark_id, max(trgm.similarity(a.alias_norm, p_text)) AS s
      FROM op.landmark_aliases a
     WHERE a.version = p_version AND a.ward_id = p_ward
     GROUP BY a.landmark_id
     ORDER BY s DESC, a.landmark_id
     LIMIT 1
$$;

GRANT SELECT ON op.gazetteer_versions, op.wards, op.ward_zones, op.landmarks, op.landmark_aliases, op.config_versions
    TO op_intake, op_ivr, op_dispatch, op_readonly;
GRANT EXECUTE ON FUNCTION op.match_landmark(text, text, text) TO op_intake, op_ivr;
