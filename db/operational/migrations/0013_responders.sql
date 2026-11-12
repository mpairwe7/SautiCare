-- Responder registry and availability (M2 Task 1; Build Prompt §9
-- Responders, §10.4, §10.11; decision 5).
--
-- A responder is a VHT member or a transporter. Name and number are sealed
-- to their own row (identifiers key class) with a keyed phone hash to
-- recognise the responder's own calls; dispatch reads the registry facts.
-- Availability periods use a PostgreSQL 18 temporal key (WITHOUT OVERLAPS),
-- so overlapping periods are impossible; "available" lasts 8 hours on the
-- database clock. Only the console registers responders and changes gates;
-- responders switch themselves on and off through intake (USSD) and IVR.

-- btree_gist gives uuid a GiST operator class for the temporal key. Like
-- pg_trgm (0007), a trusted extension keeps PUBLIC EXECUTE on its functions,
-- so it lives in its own schema with no USAGE for runtime roles.
CREATE SCHEMA gist;
REVOKE ALL ON SCHEMA gist FROM PUBLIC;
CREATE EXTENSION IF NOT EXISTS btree_gist WITH SCHEMA gist;

CREATE TABLE op.responders (
    id               uuid        PRIMARY KEY,
    role             text        NOT NULL CHECK (role IN ('vht', 'transporter')),
    name_ct          bytea       NOT NULL,
    name_key_id      text        NOT NULL,
    phone_ct         bytea       NOT NULL,
    phone_key_id     text        NOT NULL,
    phone_mac        bytea       NOT NULL CHECK (octet_length(phone_mac) = 32),
    phone_mac_key_id text        NOT NULL,
    ward             text        NOT NULL CHECK (ward ~ '^[a-z][a-z0-9_.-]{0,63}$'),
    stage            text        NOT NULL CHECK (stage ~ '^[a-z][a-z0-9_.-]{0,63}$'),
    certified_at     timestamptz,
    insured_until    timestamptz,
    kit_ok           boolean     NOT NULL DEFAULT false,
    research_consent boolean     NOT NULL DEFAULT false,
    status           text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'retired')),
    created_at       timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at       timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX responders_phone_mac ON op.responders (phone_mac);

CREATE TABLE op.availability (
    responder_id uuid        NOT NULL REFERENCES op.responders (id),
    period       tstzrange   NOT NULL CHECK (NOT isempty(period) AND lower_inc(period) AND NOT upper_inc(period)),
    source       text        NOT NULL CHECK (source IN ('ussd', 'ivr', 'console')),
    reminded_at  timestamptz
);

-- The temporal key is PostgreSQL 18 syntax sqlc cannot parse: added through
-- EXECUTE (ADR-0011). No two periods of one responder may overlap.
DO $$
BEGIN
    EXECUTE 'ALTER TABLE op.availability ADD CONSTRAINT availability_pkey PRIMARY KEY (responder_id, period WITHOUT OVERLAPS)';
END
$$;

CREATE FUNCTION op.register_responder(
    p_id uuid, p_role text, p_name_ct bytea, p_name_key_id text, p_phone_ct bytea, p_phone_key_id text,
    p_phone_mac bytea, p_phone_mac_key_id text, p_ward text, p_stage text, p_certified_at timestamptz,
    p_insured_until timestamptz, p_kit_ok boolean, p_research_consent boolean
) RETURNS void
LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
    INSERT INTO op.responders (id, role, name_ct, name_key_id, phone_ct, phone_key_id, phone_mac, phone_mac_key_id,
                               ward, stage, certified_at, insured_until, kit_ok, research_consent)
    VALUES (p_id, p_role, p_name_ct, p_name_key_id, p_phone_ct, p_phone_key_id, p_phone_mac, p_phone_mac_key_id,
            p_ward, p_stage, p_certified_at, p_insured_until, p_kit_ok, p_research_consent)
$$;

CREATE FUNCTION op.set_responder_gates(
    p_id uuid, p_certified_at timestamptz, p_insured_until timestamptz, p_kit_ok boolean, p_status text
) RETURNS void
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
BEGIN
    UPDATE op.responders SET certified_at = p_certified_at, insured_until = p_insured_until, kit_ok = p_kit_ok,
                             status = p_status, updated_at = clock_timestamp()
     WHERE id = p_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'unknown responder %', p_id USING ERRCODE = 'SA404';
    END IF;
END
$$;

-- On: close any open period at now and open [now, now + 8 h). Off: close the
-- open period at now. Half-open ranges meet without overlapping.
CREATE FUNCTION op.set_availability(p_id uuid, p_available boolean, p_source text)
RETURNS TABLE (period_from timestamptz, period_until timestamptz)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_now timestamptz := clock_timestamp();
BEGIN
    PERFORM 1 FROM op.responders r WHERE r.id = p_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'unknown responder %', p_id USING ERRCODE = 'SA404';
    END IF;
    UPDATE op.availability a SET period = tstzrange(lower(a.period), v_now, '[)')
     WHERE a.responder_id = p_id AND a.period @> v_now AND lower(a.period) < v_now;
    DELETE FROM op.availability a WHERE a.responder_id = p_id AND lower(a.period) >= v_now;
    IF p_available THEN
        INSERT INTO op.availability (responder_id, period, source)
        VALUES (p_id, tstzrange(v_now, v_now + interval '8 hours', '[)'), p_source);
        RETURN QUERY SELECT v_now, v_now + interval '8 hours';
    ELSE
        RETURN QUERY SELECT v_now, v_now;
    END IF;
END
$$;

GRANT EXECUTE ON FUNCTION op.register_responder(uuid, text, bytea, text, bytea, text, bytea, text, text, text,
                                               timestamptz, timestamptz, boolean, boolean),
                          op.set_responder_gates(uuid, timestamptz, timestamptz, boolean, text)
    TO op_console;
GRANT EXECUTE ON FUNCTION op.set_availability(uuid, boolean, text) TO op_console, op_intake, op_ivr;
GRANT SELECT ON op.responders, op.availability TO op_dispatch;
GRANT SELECT (id, phone_mac, status) ON op.responders TO op_intake, op_ivr;
GRANT SELECT (id, role, ward, stage, certified_at, insured_until, kit_ok, research_consent, status, created_at, updated_at)
    ON op.responders TO op_readonly;
GRANT SELECT ON op.availability TO op_readonly;
