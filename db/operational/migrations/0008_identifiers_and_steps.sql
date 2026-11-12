-- Sealed screen-answer identifiers and keyed USSD step keys (M1 Task 6;
-- ADR-0021; M0 review rule I3/M13).
--
-- A callback number (screen 8) or a typed landmark (screen 7 option 6) is
-- sealed under the identifiers key class with AAD naming its own row, stored
-- here, and referenced from the screen.answered event by id. It never enters
-- an event payload, the audit hash or a log.

CREATE TABLE op.incident_identifiers (
    id          uuid        PRIMARY KEY,
    incident_id uuid        NOT NULL REFERENCES op.incidents (id),
    kind        text        NOT NULL CHECK (kind IN ('callback', 'landmark_text')),
    ct          bytea       NOT NULL,
    key_id      text        NOT NULL,
    idem_key    text        NOT NULL CHECK (idem_key <> ''),
    created_at  timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT incident_identifiers_idem UNIQUE (incident_id, idem_key)
);

-- Insert once per (incident, idem_key); a retry returns the stored row's id
-- and its new ciphertext is discarded.
CREATE FUNCTION op.store_identifier(
    p_id uuid, p_incident uuid, p_kind text, p_ct bytea, p_key_id text, p_idem_key text
) RETURNS uuid
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_id uuid;
BEGIN
    INSERT INTO op.incident_identifiers (id, incident_id, kind, ct, key_id, idem_key)
    VALUES (p_id, p_incident, p_kind, p_ct, p_key_id, p_idem_key)
    ON CONFLICT ON CONSTRAINT incident_identifiers_idem DO NOTHING
    RETURNING id INTO v_id;
    IF v_id IS NULL THEN
        SELECT i.id INTO v_id FROM op.incident_identifiers i
         WHERE i.incident_id = p_incident AND i.idem_key = p_idem_key;
    END IF;
    RETURN v_id;
END
$$;

GRANT EXECUTE ON FUNCTION op.store_identifier(uuid, uuid, text, bytea, text, text) TO op_intake, op_ivr;

-- USSD steps were keyed by sha256(text); the text can hold a callback number,
-- and an unkeyed hash of ~10^9 numbers is reversible. Steps are now keyed by
-- an HMAC under the identifiers key class (HKDF info sauti/ussd-step/v1) and
-- record the consumed input length. Existing rows are short-lived replay
-- entries with reversible keys: they are discarded, not migrated.
TRUNCATE op.ussd_steps;
ALTER TABLE op.ussd_steps
    DROP CONSTRAINT ussd_steps_pkey,
    DROP COLUMN input_sha256,
    ADD COLUMN input_mac    bytea NOT NULL CHECK (octet_length(input_mac) = 32),
    ADD COLUMN mac_key_id   text  NOT NULL,
    ADD COLUMN consumed_len int   NOT NULL CHECK (consumed_len >= 0),
    ADD PRIMARY KEY (provider, provider_session_id, input_mac);

CREATE INDEX ussd_steps_latest ON op.ussd_steps (provider, provider_session_id, consumed_len DESC);
