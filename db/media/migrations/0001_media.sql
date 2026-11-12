-- Media store (Build Prompt §5, §7; RMRP §3). Consented interview audio
-- ONLY — no call or consent audio exists in this system (rule 3.12). One
-- key per recording; destroyed by destroying that key. Interview audio is
-- kept until its transcript is checked and never beyond 12 calendar months:
-- the CHECK uses created_at (UTC date), which is never later than the window
-- close, so it is at least as strict as the policy's clock. created_at is set
-- by the database; no writer can supply it and so extend retention.

CREATE TABLE media.recordings (
    id                    uuid        PRIMARY KEY DEFAULT uuidv7(),
    key_id                text        NOT NULL CHECK (key_id <> ''),
    consent_ref           text        NOT NULL CHECK (consent_ref <> ''),
    study_id              uuid,
    created_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    transcript_checked_at timestamptz,
    destroy_by            date        NOT NULL,
    CHECK (destroy_by <= (created_at AT TIME ZONE 'UTC')::date + interval '12 months')
);

CREATE TABLE media.chunks (
    recording_id uuid  NOT NULL REFERENCES media.recordings (id) ON DELETE CASCADE,
    n            int   NOT NULL CHECK (n >= 0),
    ciphertext   bytea NOT NULL,
    PRIMARY KEY (recording_id, n)
);

REVOKE ALL ON ALL TABLES IN SCHEMA media FROM PUBLIC;
GRANT USAGE ON SCHEMA media TO md_writer, md_reader;
-- Per column on recordings: created_at (and the later transcript check
-- time) are never writer-supplied. The writer supplies id so it can insert
-- the chunks without reading recordings back.
GRANT INSERT (id, key_id, consent_ref, study_id, destroy_by) ON media.recordings TO md_writer;
GRANT INSERT ON media.chunks TO md_writer;
GRANT SELECT ON media.recordings, media.chunks TO md_reader;
