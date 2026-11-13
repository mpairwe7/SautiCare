-- Stages and health facilities in the gazetteer (M2 Task 1; Build Prompt §9
-- Reference: "stages (boda stage, zone); facilities (name, public phone, GPS
-- point, zone, hours, forwarding_enabled)"). Versioned with the gazetteer,
-- frozen like it (0007), no personal data: a facility's phone is the
-- institution's public number.

CREATE TABLE op.stages (
    version    text             NOT NULL,
    id         text             NOT NULL CHECK (id ~ '^[a-z][a-z0-9_.-]{0,63}$'),
    list_order int              NOT NULL,
    ward_id    text             NOT NULL,
    name_en    text             NOT NULL CHECK (name_en <> ''),
    name_lg    text             NOT NULL CHECK (name_lg <> ''),
    lat        double precision NOT NULL,
    lon        double precision NOT NULL,
    zone_col   int              NOT NULL,
    zone_row   int              NOT NULL,
    PRIMARY KEY (version, id),
    UNIQUE (version, list_order),
    FOREIGN KEY (version, ward_id) REFERENCES op.wards (version, id)
);

CREATE TABLE op.facilities (
    version            text             NOT NULL REFERENCES op.gazetteer_versions (version),
    id                 text             NOT NULL CHECK (id ~ '^[a-z][a-z0-9_.-]{0,63}$'),
    list_order         int              NOT NULL,
    name_en            text             NOT NULL CHECK (name_en <> ''),
    name_lg            text             NOT NULL CHECK (name_lg <> ''),
    lat                double precision NOT NULL,
    lon                double precision NOT NULL,
    zone_col           int              NOT NULL,
    zone_row           int              NOT NULL,
    phone              text             NOT NULL DEFAULT '' CHECK (phone = '' OR phone ~ '^\+[1-9][0-9]{7,14}$'),
    forwarding_enabled boolean          NOT NULL DEFAULT false CHECK (NOT forwarding_enabled OR phone <> ''),
    PRIMARY KEY (version, id),
    UNIQUE (version, list_order)
);

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['stages', 'facilities'] LOOP
        EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE OR DELETE ON op.%I FOR EACH ROW EXECUTE FUNCTION op.gazetteer_frozen()',
                       t || '_frozen', t);
        EXECUTE format('CREATE TRIGGER %I BEFORE TRUNCATE ON op.%I FOR EACH STATEMENT EXECUTE FUNCTION op.gazetteer_frozen()',
                       t || '_frozen_truncate', t);
    END LOOP;
END
$$;

GRANT SELECT ON op.stages, op.facilities TO op_intake, op_ivr, op_dispatch, op_readonly;
