-- CodeRabbit review of PR #3 (head ead62cf). Applied migrations are
-- immutable (the migrator refuses an edited history), so the fixes to 0002
-- and 0003 land here.

-- 1. A flag is a JSON string.
--
-- The write path checks a flag with p_payload ->> 'flag', which turns
-- {"flag":123} into the non-empty text '123' and accepts it. incident.Apply
-- decodes the flag into a Go string and rejects such an event, so a stored
-- history holding one can no longer be replayed by incident.Fold.
--
-- incident.flagged is the only event type that carries a flag. The rule is
-- a table constraint, not a change to op.append_event_core: later migrations
-- replace that function, and a constraint holds whatever the write path
-- becomes, and for every partition, present or future. A flag event with no
-- flag fails it too (jsonb_typeof of a missing key is NULL); the write path
-- already rejects those, and an empty string, first, with SA422.
--
-- Added NOT VALID: every new write is checked, but existing rows are not
-- scanned here, so the migration neither holds the table's lock through a
-- scan of the event store nor fails on a numeric flag stored before it.
-- Validating existing rows is a separate operator step on a populated store
-- (docs/runbooks/database-migrations.md): count the rows that would fail,
--   SELECT count(*) FROM op.incident_events
--    WHERE type = 'incident.flagged' AND jsonb_typeof(payload -> 'flag') IS DISTINCT FROM 'string';
-- repair them by an approved correction if any, then
--   ALTER TABLE op.incident_events VALIDATE CONSTRAINT incident_events_flag_is_string;
-- which takes only a SHARE UPDATE EXCLUSIVE lock (reads and writes go on).
ALTER TABLE op.incident_events
    ADD CONSTRAINT incident_events_flag_is_string
    CHECK (type <> 'incident.flagged' OR jsonb_typeof(payload -> 'flag') IS NOT DISTINCT FROM 'string') NOT VALID;

-- 2. op.ussd_steps.created_at stays under database control.
--
-- 0003 granted op_intake table-wide INSERT, which lets it supply created_at
-- and backdate or future-date a cached reply. It keeps SELECT, and INSERT
-- only on the data columns. Revoking the table-level privilege also revokes
-- any column-level INSERT, so the grant below is the whole of it.
--
-- The column list is read from the catalogue rather than written out: the
-- table's columns change along the migration history (0008 rekeys steps by
-- a keyed MAC), and in a fresh database this migration runs after all of
-- them. A migration that later adds a column to op.ussd_steps must grant
-- INSERT on that column to op_intake itself.
REVOKE INSERT ON op.ussd_steps FROM op_intake;

DO $$
DECLARE
    v_cols text;
BEGIN
    SELECT string_agg(quote_ident(a.attname), ', ' ORDER BY a.attnum)
      INTO STRICT v_cols
      FROM pg_attribute a
     WHERE a.attrelid = 'op.ussd_steps'::regclass
       AND a.attnum > 0
       AND NOT a.attisdropped
       AND a.attname <> 'created_at';
    EXECUTE format('GRANT INSERT (%s) ON op.ussd_steps TO op_intake', v_cols);
END
$$;
