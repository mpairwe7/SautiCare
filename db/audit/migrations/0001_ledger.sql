-- Audit ledger (Build Prompt rule 3.8; ISP §5.5; spec §6.3; ADR-0005).
--
-- Append-only and hash-chained. Entries hold typed columns only — there is
-- no free-form column in which a caller identifier could be stored (hard
-- constraint 8). Each hash = sha256(prev_hash ‖ audit.canonical(...)); the
-- encoding is normative and mirrored by internal/audit/canonical.go
-- (docs/reference/audit-canonical-encoding.md). Signed checkpoints anchor
-- the chain outside the database.

CREATE TABLE audit.entries (
    seq            bigint      PRIMARY KEY CHECK (seq > 0),
    entry_id       uuid        NOT NULL UNIQUE,
    occurred_at    timestamptz NOT NULL,
    recorded_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    event_type     text        NOT NULL CHECK (event_type ~ '^[a-z][a-z0-9_.]{2,63}$'),
    incident_id    uuid,
    staff_id       uuid,
    logic_version  text        CHECK (logic_version <> '' AND length(logic_version) <= 200),
    payload_sha256 bytea       NOT NULL CHECK (octet_length(payload_sha256) = 32),
    prev_hash      bytea       NOT NULL CHECK (octet_length(prev_hash) = 32),
    hash           bytea       NOT NULL CHECK (octet_length(hash) = 32)
);

CREATE INDEX entries_incident_idx ON audit.entries (incident_id) WHERE incident_id IS NOT NULL;

CREATE TABLE audit.checkpoints (
    id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    origin      text        NOT NULL CHECK (origin <> ''),
    seq         bigint      NOT NULL CHECK (seq >= 0),
    head        bytea       NOT NULL CHECK (octet_length(head) = 32),
    signed_at   timestamptz NOT NULL,
    key_id      text        NOT NULL CHECK (key_id <> ''),
    body        bytea       NOT NULL,
    signature   bytea       NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

-- Length-prefixed field: 4-byte big-endian length, then the bytes.
CREATE FUNCTION audit.lp(b bytea) RETURNS bytea
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$ SELECT int4send(octet_length(b)) || b $$;

-- Normative canonical encoding of an entry (v1).
CREATE FUNCTION audit.canonical(
    p_seq bigint, p_entry_id uuid, p_occurred_at timestamptz, p_event_type text,
    p_incident_id uuid, p_staff_id uuid, p_logic_version text, p_payload_sha256 bytea
) RETURNS bytea
LANGUAGE sql STABLE PARALLEL SAFE
SET search_path = audit, pg_temp
AS $$
    SELECT convert_to('sauti-audit-entry/v1', 'UTF8') || '\x00'::bytea
        || audit.lp(int8send(p_seq))
        || audit.lp(uuid_send(p_entry_id))
        || audit.lp(int8send((extract(epoch FROM p_occurred_at) * 1000000)::bigint))
        || audit.lp(convert_to(p_event_type, 'UTF8'))
        || audit.lp(coalesce(uuid_send(p_incident_id), '\x'::bytea))
        || audit.lp(coalesce(uuid_send(p_staff_id), '\x'::bytea))
        || audit.lp(coalesce(convert_to(p_logic_version, 'UTF8'), '\x'::bytea))
        || audit.lp(p_payload_sha256)
$$;

-- Append one entry. Idempotent on entry_id (the relay delivers at least
-- once). The advisory lock is taken in its own statement before the head is
-- read, so under READ COMMITTED the head read sees every committed append.
CREATE FUNCTION audit.append(
    p_entry_id uuid, p_occurred_at timestamptz, p_event_type text,
    p_incident_id uuid, p_staff_id uuid, p_logic_version text, p_payload_sha256 bytea
) RETURNS SETOF audit.entries
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = audit, pg_temp
AS $$
DECLARE
    v_head_seq  bigint;
    v_head_hash bytea;
    v_seq       bigint;
BEGIN
    PERFORM pg_advisory_xact_lock(7347001);

    RETURN QUERY SELECT * FROM audit.entries WHERE entry_id = p_entry_id;
    IF FOUND THEN
        RETURN;
    END IF;

    SELECT e.seq, e.hash INTO v_head_seq, v_head_hash
      FROM audit.entries e ORDER BY e.seq DESC LIMIT 1;
    IF NOT FOUND THEN
        v_head_seq  := 0;
        v_head_hash := decode(repeat('00', 32), 'hex');
    END IF;
    v_seq := v_head_seq + 1;

    RETURN QUERY
    INSERT INTO audit.entries (seq, entry_id, occurred_at, event_type, incident_id,
                               staff_id, logic_version, payload_sha256, prev_hash, hash)
    VALUES (v_seq, p_entry_id, p_occurred_at, p_event_type, p_incident_id,
            p_staff_id, p_logic_version, p_payload_sha256, v_head_hash,
            sha256(v_head_hash || audit.canonical(v_seq, p_entry_id, p_occurred_at, p_event_type,
                                                  p_incident_id, p_staff_id, p_logic_version,
                                                  p_payload_sha256)))
    RETURNING *;
END
$$;

-- The current chain head; no row when the ledger is empty.
CREATE FUNCTION audit.head() RETURNS TABLE (seq bigint, hash bytea)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = audit, pg_temp
AS $$ SELECT e.seq, e.hash FROM audit.entries e ORDER BY e.seq DESC LIMIT 1 $$;

CREATE FUNCTION audit.save_checkpoint(
    p_origin text, p_seq bigint, p_head bytea, p_signed_at timestamptz,
    p_key_id text, p_body bytea, p_signature bytea
) RETURNS bigint
LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = audit, pg_temp
AS $$
    INSERT INTO audit.checkpoints (origin, seq, head, signed_at, key_id, body, signature)
    VALUES (p_origin, p_seq, p_head, p_signed_at, p_key_id, p_body, p_signature)
    RETURNING id
$$;

-- Append-only, even for the owner. Disabling these triggers is a deliberate,
-- privileged act that the signed checkpoints make detectable.
CREATE FUNCTION audit.forbid_change() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'audit ledger is append-only (% on %)', TG_OP, TG_TABLE_NAME
        USING ERRCODE = 'SA403';
END
$$;

CREATE TRIGGER entries_append_only BEFORE UPDATE OR DELETE ON audit.entries
    FOR EACH ROW EXECUTE FUNCTION audit.forbid_change();
CREATE TRIGGER entries_no_truncate BEFORE TRUNCATE ON audit.entries
    FOR EACH STATEMENT EXECUTE FUNCTION audit.forbid_change();
CREATE TRIGGER checkpoints_append_only BEFORE UPDATE OR DELETE ON audit.checkpoints
    FOR EACH ROW EXECUTE FUNCTION audit.forbid_change();
CREATE TRIGGER checkpoints_no_truncate BEFORE TRUNCATE ON audit.checkpoints
    FOR EACH STATEMENT EXECUTE FUNCTION audit.forbid_change();

-- Privileges: functions default to EXECUTE for PUBLIC, so revoke first.
REVOKE ALL ON ALL TABLES IN SCHEMA audit FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA audit FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA audit REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;

GRANT USAGE ON SCHEMA audit TO audit_appender, audit_reader;
GRANT EXECUTE ON FUNCTION
    audit.append(uuid, timestamptz, text, uuid, uuid, text, bytea),
    audit.head(),
    audit.save_checkpoint(text, bigint, bytea, timestamptz, text, bytea, bytea)
TO audit_appender;
GRANT SELECT ON audit.entries, audit.checkpoints TO audit_reader;
