-- Analytic store (Build Prompt §7, §9; Guide §10–11; spec §6.1).
-- Pseudonymised MEDS records keyed by a random study ID. No phone numbers,
-- names, free text or precise location: location is parish + landmark only.
-- CODEOWNERS: the PI approves every change under db/analytic/ (rule 3.11).

CREATE TABLE analytic.meds_records (
    study_id                   uuid        PRIMARY KEY,
    -- The ten MEDS fields (Engineering Guide §10).
    parish                     text        NOT NULL,
    landmark_code              text,
    time_of_call               timestamptz NOT NULL,          -- T1
    category                   text,
    severity                   text        CHECK (severity IN ('red', 'yellow', 'green')),
    responder_types            text[]      NOT NULL DEFAULT '{}',
    interval_alert_to_arrival_s numeric(10,3),                -- T3 -> T5
    first_action_code          text,                          -- Tool 2 closed list (protocol pending)
    facility_disposition       text,
    transport_mode             text,
    destination                text,
    status_24_48h              text,                          -- Tool 4, three values (protocol pending)
    -- Captured alongside the MEDS.
    age_band                   text,
    sex                        text,
    -- Full-resolution offsets from T1, in seconds (primary outcome T1 -> T5).
    s_t2 numeric(10,3), s_t3 numeric(10,3), s_t4 numeric(10,3), s_t5 numeric(10,3),
    s_t6 numeric(10,3), s_t7 numeric(10,3),
    -- Provenance the analysis needs to stay interpretable (Guide §11, §14).
    logic_version              text        NOT NULL,
    zone_set_version           text        NOT NULL,
    arrival_grade              text,
    evidence_flags             text[]      NOT NULL DEFAULT '{}',
    -- Only consented / permitted / waived records may enter (rule 3.10).
    consent_status             text        NOT NULL
        CHECK (consent_status IN ('consented', 'guardian_permission', 'assent', 'waiver')),
    -- A waiver always carries its reason (Guide §14); no other status may.
    waiver_reason              text        CHECK (waiver_reason IN ('deceased', 'untraceable', 'unable_to_understand')),
    exported_at                timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT meds_waiver_reason CHECK ((consent_status = 'waiver') = (waiver_reason IS NOT NULL))
);

-- Withdrawals: the study ID stays suppressed forever; every restore replays
-- this ledger before the store goes back online (Build Prompt §13).
CREATE TABLE analytic.suppression_ledger (
    study_id     uuid        PRIMARY KEY,
    withdrawn_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    reason       text        NOT NULL CHECK (reason IN ('withdrawal', 'dsr_deletion'))
);

CREATE FUNCTION analytic.forbid_change() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'suppression ledger is append-only (%)', TG_OP USING ERRCODE = 'SA403';
END
$$;

CREATE TRIGGER suppression_append_only BEFORE UPDATE OR DELETE ON analytic.suppression_ledger
    FOR EACH ROW EXECUTE FUNCTION analytic.forbid_change();
CREATE TRIGGER suppression_no_truncate BEFORE TRUNCATE ON analytic.suppression_ledger
    FOR EACH STATEMENT EXECUTE FUNCTION analytic.forbid_change();

REVOKE ALL ON ALL TABLES IN SCHEMA analytic FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA analytic FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA analytic REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;
GRANT USAGE ON SCHEMA analytic TO an_writer, an_reader;
GRANT SELECT, INSERT, DELETE ON analytic.meds_records TO an_writer;   -- DELETE: withdrawal
GRANT SELECT, INSERT ON analytic.suppression_ledger TO an_writer;
GRANT SELECT ON analytic.meds_records, analytic.suppression_ledger TO an_reader;
