-- A classification needs a logic version (evaluation-hardening plan,
-- docs/superpowers/plans/2026-11-14-evaluation-hardening.md, Task 3.4;
-- evaluation audit V-29, ES-9; ADR-0043).
--
-- The store sends an empty logic version as NULL, and the format check in
-- op.append_event_core (0018) does not raise on NULL, so an incident.classified
-- event with no version was stored and the study could not attribute it to a
-- rule set. A trigger on the event log, not a check inside the append
-- function: no op.append_incident_event or op.append_event_core is replaced
-- here, and the trigger holds whatever a later migration makes of the write
-- path (as 0006's incident_events_answer_unchanged does). It runs before the
-- event insert, so a refused classification leaves no event, no outbox row
-- and no projection change. internal/incident/fold.go applies the same rule.
CREATE FUNCTION op.refuse_unversioned_classification() RETURNS trigger
LANGUAGE plpgsql
SET search_path = op, pg_temp
AS $$
BEGIN
    IF coalesce(NEW.logic_version, '') = '' THEN
        RAISE EXCEPTION 'illegal transition: a classification needs a logic version'
            USING ERRCODE = 'SA422';
    END IF;
    RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION op.refuse_unversioned_classification() FROM PUBLIC;

CREATE TRIGGER incident_events_classification_versioned BEFORE INSERT ON op.incident_events
    FOR EACH ROW WHEN (NEW.type = 'incident.classified') EXECUTE FUNCTION op.refuse_unversioned_classification();
