-- SMS delivery (M2 Part B Task 8; Build Prompt §10.4, §16; hard constraint
-- 6; ADR-0027).
--
-- * Delivery reports from the provider land on the attempt that sent the
--   message (alert attempts and caller-notice attempts); a final report is
--   never overwritten by a late intermediate one.
-- * Caller notices (helper on the way, forwarded to a facility) are
--   delivered through attempts claimed once, like alerts.
-- * op.caller_contact gives dispatch the caller's number for SMS — the
--   callback number typed at screen 8 when there is one, otherwise the
--   number called from — sealed, with the row it is bound to. Dispatch
--   never reads op.sessions or op.incident_identifiers itself.
-- * The 8-hour availability-expiry reminder is claimed once per period.

ALTER TABLE op.alert_attempts
    ADD COLUMN delivery_status  text CHECK (delivery_status ~ '^[A-Za-z]{1,32}$'),
    ADD COLUMN delivery_failure text CHECK (delivery_failure ~ '^[A-Za-z]{1,64}$'),
    ADD COLUMN delivery_final   boolean NOT NULL DEFAULT false,
    ADD COLUMN delivery_at      timestamptz;
CREATE INDEX alert_attempts_provider_ref ON op.alert_attempts (provider_ref) WHERE provider_ref IS NOT NULL;

CREATE TABLE op.notice_attempts (
    id               uuid        PRIMARY KEY,
    incident_id      uuid        NOT NULL,
    kind             text        NOT NULL,
    channel          text        NOT NULL CHECK (channel IN ('sms', 'voice')),
    status           text        NOT NULL DEFAULT 'claimed' CHECK (status IN ('claimed', 'sent', 'failed', 'unknown')),
    provider_ref     text        CHECK (provider_ref ~ '^[A-Za-z0-9_.:-]{1,128}$'),
    claimed_at       timestamptz NOT NULL DEFAULT clock_timestamp(),
    settled_at       timestamptz,
    delivery_status  text        CHECK (delivery_status ~ '^[A-Za-z]{1,32}$'),
    delivery_failure text        CHECK (delivery_failure ~ '^[A-Za-z]{1,64}$'),
    delivery_final   boolean     NOT NULL DEFAULT false,
    delivery_at      timestamptz,
    UNIQUE (incident_id, kind, channel),
    FOREIGN KEY (incident_id, kind) REFERENCES op.caller_notices (incident_id, kind),
    CHECK ((status = 'claimed') = (settled_at IS NULL))
);
CREATE INDEX notice_attempts_provider_ref ON op.notice_attempts (provider_ref) WHERE provider_ref IS NOT NULL;

-- Claim a caller-notice attempt before the provider call; fresh only once,
-- and only for a notice dispatch recorded.
CREATE FUNCTION op.begin_notice_attempt(p_incident uuid, p_kind text, p_channel text)
RETURNS TABLE (attempt_id uuid, fresh boolean)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_id uuid;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM op.caller_notices n WHERE n.incident_id = p_incident AND n.kind = p_kind) THEN
        RETURN QUERY SELECT NULL::uuid, false;
        RETURN;
    END IF;
    INSERT INTO op.notice_attempts (id, incident_id, kind, channel) VALUES (uuidv7(), p_incident, p_kind, p_channel)
    ON CONFLICT ON CONSTRAINT notice_attempts_incident_id_kind_channel_key DO NOTHING
    RETURNING id INTO v_id;
    IF v_id IS NOT NULL THEN
        RETURN QUERY SELECT v_id, true;
        RETURN;
    END IF;
    RETURN QUERY SELECT x.id, false FROM op.notice_attempts x
                  WHERE x.incident_id = p_incident AND x.kind = p_kind AND x.channel = p_channel;
END
$$;

CREATE FUNCTION op.finish_notice_attempt(p_attempt uuid, p_status text, p_provider_ref text) RETURNS boolean
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
BEGIN
    IF p_status NOT IN ('sent', 'failed', 'unknown') THEN
        PERFORM op.reject('an attempt settles as sent, failed or unknown');
    END IF;
    UPDATE op.notice_attempts x SET status = p_status, provider_ref = p_provider_ref, settled_at = clock_timestamp()
     WHERE x.id = p_attempt AND x.status = 'claimed';
    RETURN FOUND;
END
$$;

-- A provider delivery report (intake-gw's callback). False when no attempt
-- carries that message ID.
CREATE FUNCTION op.record_delivery_report(p_provider_ref text, p_status text, p_failure text, p_final boolean) RETURNS boolean
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
BEGIN
    UPDATE op.alert_attempts x SET delivery_status = p_status, delivery_failure = p_failure, delivery_final = p_final,
                                   delivery_at = clock_timestamp()
     WHERE x.provider_ref = p_provider_ref AND NOT x.delivery_final;
    UPDATE op.notice_attempts x SET delivery_status = p_status, delivery_failure = p_failure, delivery_final = p_final,
                                    delivery_at = clock_timestamp()
     WHERE x.provider_ref = p_provider_ref AND NOT x.delivery_final;
    RETURN EXISTS (SELECT 1 FROM op.alert_attempts x WHERE x.provider_ref = p_provider_ref)
        OR EXISTS (SELECT 1 FROM op.notice_attempts x WHERE x.provider_ref = p_provider_ref);
END
$$;

-- The caller's number for SMS, sealed, with the row it is bound to: the
-- callback number from the latest screen 8 answer when the caller gave one,
-- otherwise the number of the incident's latest session that has one.
CREATE FUNCTION op.caller_contact(p_incident uuid)
RETURNS TABLE (source text, row_id uuid, ct bytea, key_id text)
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_pl jsonb;
BEGIN
    SELECT e.payload INTO v_pl FROM op.incident_events e
     WHERE e.incident_id = p_incident AND e.type = 'screen.answered' AND e.payload ->> 'field' = 'callback'
     ORDER BY e.seq DESC LIMIT 1;
    IF v_pl ->> 'value' = 'provided' THEN
        RETURN QUERY SELECT 'incident_identifiers'::text, i.id, i.ct, i.key_id FROM op.incident_identifiers i
                      WHERE i.id = (v_pl ->> 'identifier_id')::uuid AND i.incident_id = p_incident AND i.kind = 'callback';
        RETURN;
    END IF;
    RETURN QUERY SELECT 'sessions'::text, s.id, s.phone_ct, s.phone_key_id FROM op.sessions s
                  WHERE s.incident_id = p_incident AND s.phone_ct IS NOT NULL
                  ORDER BY s.started_at DESC, s.id DESC LIMIT 1;
END
$$;

-- Review focus 4: claim the expiry reminders of availability periods that
-- end within p_within, once per period.
CREATE FUNCTION op.claim_availability_reminders(p_within interval, p_limit int)
RETURNS TABLE (responder_id uuid, until timestamptz)
LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
    WITH due AS (
        SELECT a.responder_id, a.period
          FROM op.availability a JOIN op.responders r ON r.id = a.responder_id
         WHERE a.reminded_at IS NULL AND r.status = 'active'
           AND a.period @> clock_timestamp() AND upper(a.period) <= clock_timestamp() + p_within
         ORDER BY upper(a.period)
         LIMIT p_limit
           FOR UPDATE OF a SKIP LOCKED)
    UPDATE op.availability a SET reminded_at = clock_timestamp()
      FROM due WHERE a.responder_id = due.responder_id AND a.period = due.period
    RETURNING a.responder_id, upper(a.period)
$$;

GRANT EXECUTE ON FUNCTION
    op.begin_notice_attempt(uuid, text, text),
    op.finish_notice_attempt(uuid, text, text),
    op.caller_contact(uuid),
    op.claim_availability_reminders(interval, int)
TO op_dispatch;
GRANT EXECUTE ON FUNCTION op.record_delivery_report(text, text, text, boolean) TO op_intake;
GRANT SELECT ON op.notice_attempts TO op_dispatch, op_readonly;
