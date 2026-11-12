-- Operational store core (Build Prompt §9; spec §6.2; ADR-0003).
--
-- incident_events is the append-only log; incidents is its projection. Only
-- the SECURITY DEFINER functions in 0002_write_path.sql write either table.
-- Every timestamp is timestamptz in UTC and set by the database.

CREATE TABLE op.incidents (
    id            uuid        PRIMARY KEY DEFAULT uuidv7(),
    reference     text        NOT NULL CONSTRAINT incidents_reference_key UNIQUE
                              CHECK (reference ~ '^[0-9]{10}$'),
    channel       text        NOT NULL CHECK (channel IN ('ussd_at', 'ussd_ucc', 'ivr')),
    state         text        NOT NULL CHECK (state IN ('intake', 'triaged', 'dispatching', 'assigned',
                                  'on_scene', 'transporting', 'at_facility', 'closed', 'merged')),
    version       bigint      NOT NULL CHECK (version >= 1),
    severity      text        CHECK (severity IN ('red', 'yellow', 'green')),
    logic_version text,
    t1            timestamptz NOT NULL,
    t2            timestamptz,
    t3            timestamptz,
    t4            timestamptz,
    t5            timestamptz,
    t6            timestamptz,
    t7            timestamptz,
    escalated     boolean     NOT NULL DEFAULT false,
    flags         text[]      NOT NULL DEFAULT '{}',
    merged_into   uuid        REFERENCES op.incidents (id),
    created_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at    timestamptz NOT NULL DEFAULT clock_timestamp()
);

-- Partitioned by month so a retention period maps to whole partitions and
-- one key period (Build Prompt §9). Uniqueness that must span partitions
-- (idempotency key, per-incident sequence) lives in op.event_keys.
CREATE TABLE op.incident_events (
    id            uuid        NOT NULL DEFAULT uuidv7(),
    incident_id   uuid        NOT NULL REFERENCES op.incidents (id),
    seq           bigint      NOT NULL CHECK (seq >= 1),
    type          text        NOT NULL,
    recorded_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    actor_kind    text        NOT NULL CHECK (actor_kind IN ('system', 'caller', 'responder', 'staff')),
    staff_id      uuid,
    payload       jsonb       NOT NULL DEFAULT '{}',
    idem_key      text        NOT NULL CHECK (idem_key <> ''),
    logic_version text,
    PRIMARY KEY (id, recorded_at)
) PARTITION BY RANGE (recorded_at);

CREATE INDEX incident_events_incident_seq ON op.incident_events (incident_id, seq);

-- Catch-all so a missing monthly partition never fails a dispatch-path write.
-- op.default_partition_rows() > 0 raises an alert (runbook:
-- docs/runbooks/event-partition-maintenance.md).
CREATE TABLE op.incident_events_default PARTITION OF op.incident_events DEFAULT;

CREATE TABLE op.event_keys (
    incident_id uuid        NOT NULL REFERENCES op.incidents (id),
    idem_key    text        NOT NULL,
    event_id    uuid        NOT NULL,
    seq         bigint      NOT NULL,
    recorded_at timestamptz NOT NULL,
    PRIMARY KEY (incident_id, idem_key),
    CONSTRAINT event_keys_seq_key UNIQUE (incident_id, seq)
);

CREATE TABLE op.sessions (
    id                  uuid        PRIMARY KEY,
    incident_id         uuid        NOT NULL REFERENCES op.incidents (id),
    channel             text        NOT NULL CHECK (channel IN ('ussd_at', 'ussd_ucc', 'ivr')),
    provider_session_id text        NOT NULL,
    phone_ct            bytea,
    phone_key_id        text,
    phone_mac           bytea,
    phone_mac_key_id    text,
    network_code        text,
    started_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT sessions_provider_key UNIQUE (channel, provider_session_id),
    CHECK ((phone_ct IS NULL) = (phone_key_id IS NULL)),
    CHECK ((phone_mac IS NULL) = (phone_mac_key_id IS NULL))
);

CREATE INDEX sessions_phone_mac ON op.sessions (phone_mac) WHERE phone_mac IS NOT NULL;

-- USSD step idempotency (Build Prompt §7): provider session + sha256(input).
-- The input itself (which may hold a callback number) is never stored here.
CREATE TABLE op.ussd_steps (
    provider            text        NOT NULL,
    provider_session_id text        NOT NULL,
    input_sha256        bytea       NOT NULL CHECK (octet_length(input_sha256) = 32),
    response            text        NOT NULL,
    end_session         boolean     NOT NULL,
    incident_id         uuid        REFERENCES op.incidents (id),
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (provider, provider_session_id, input_sha256)
);

-- Transactional outbox (spec §6.3). Rows are written in the same
-- transaction as the event; outbox-relay delivers them at least once.
CREATE TABLE op.outbox (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    topic        text        NOT NULL CHECK (topic IN ('audit')),
    created_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    payload      jsonb       NOT NULL,
    delivered_at timestamptz
);

CREATE INDEX outbox_pending ON op.outbox (topic, id) WHERE delivered_at IS NULL;

-- Lifecycle table. MUST equal internal/incident/transitions.go; the test
-- TestTransitionsTableMatchesGo fails on any divergence. '*' = any
-- non-terminal state; NULL to_state = state unchanged.
CREATE TABLE op.incident_transitions (
    from_state text NOT NULL,
    event_type text NOT NULL,
    to_state   text,
    PRIMARY KEY (from_state, event_type)
);

INSERT INTO op.incident_transitions (from_state, event_type, to_state) VALUES
    ('intake',       'incident.classified',     'triaged'),
    ('triaged',      'incident.dispatch_ready', 'dispatching'),
    ('dispatching',  'alert.issued',            NULL),
    ('assigned',     'alert.issued',            NULL),
    ('dispatching',  'responder.accepted',      'assigned'),
    ('assigned',     'responder.accepted',      NULL),
    ('assigned',     'responder.arrived',       'on_scene'),
    ('on_scene',     'responder.arrived',       NULL),
    ('on_scene',     'responder.departed',      'transporting'),
    ('transporting', 'responder.at_facility',   'at_facility'),
    ('*',            'screen.answered',         NULL),
    ('*',            'incident.flagged',        NULL),
    ('*',            'incident.escalated',      NULL),
    ('*',            'incident.closed',         'closed'),
    ('intake',       'incident.merged',         'merged'),
    ('triaged',      'incident.merged',         'merged'),
    ('dispatching',  'incident.merged',         'merged');

-- Versioned configuration in force (Build Prompt §9 reference data).
CREATE TABLE op.config_versions (
    kind           text        NOT NULL,
    version        text        NOT NULL,
    fingerprint    text        NOT NULL,
    effective_from timestamptz NOT NULL DEFAULT clock_timestamp(),
    amendment_ref  text,
    amendment_date date,
    PRIMARY KEY (kind, version)
);

-- Registry of key periods; key material lives in OpenBao/HSM, never here.
CREATE TABLE op.key_periods (
    id           text        PRIMARY KEY,
    class        text        NOT NULL,
    starts_at    timestamptz NOT NULL,
    ends_at      timestamptz,
    destroyed_at timestamptz
);

-- Create monthly partitions from the current month up to p_months_ahead.
CREATE FUNCTION op.ensure_event_partitions(p_months_ahead int) RETURNS int
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_month   date := date_trunc('month', clock_timestamp() AT TIME ZONE 'UTC')::date;
    v_lo      timestamptz;
    v_hi      timestamptz;
    v_name    text;
    v_created int := 0;
BEGIN
    FOR i IN 0 .. p_months_ahead LOOP
        v_lo := (v_month + make_interval(months => i))::timestamp AT TIME ZONE 'UTC';
        v_hi := (v_month + make_interval(months => i + 1))::timestamp AT TIME ZONE 'UTC';
        v_name := 'incident_events_y' || to_char(v_lo AT TIME ZONE 'UTC', 'YYYY')
                  || 'm' || to_char(v_lo AT TIME ZONE 'UTC', 'MM');
        IF to_regclass('op.' || v_name) IS NULL THEN
            EXECUTE format('CREATE TABLE op.%I PARTITION OF op.incident_events FOR VALUES FROM (%L) TO (%L)',
                           v_name, v_lo, v_hi);
            v_created := v_created + 1;
        END IF;
    END LOOP;
    RETURN v_created;
END
$$;

SELECT op.ensure_event_partitions(3);

CREATE FUNCTION op.default_partition_rows() RETURNS bigint
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$ SELECT count(*) FROM op.incident_events_default $$;
