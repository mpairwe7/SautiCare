-- Dispatch suspension and the stopping rule (M2 Task 6; O6, user decision
-- 2026-11-12; Guide §14; ADR-0026).
--
-- op.dispatch_control is one row: suspended or active, why, by whom, when.
-- While suspended intake continues and records are kept, no responder is
-- alerted, and every incident is escalated to the coordinator. A coordinator
-- (console, with a staff identity) suspends and resumes; the dispatch leader
-- may only suspend, when the stopping rule is breached. Every change is a
-- row in the append-only op.dispatch_control_log — the dated register of
-- reportable events — and an audit-ledger entry without an incident; it
-- wakes every incident in dispatch so the change applies at once.

CREATE TABLE op.dispatch_control (
    id         boolean     PRIMARY KEY DEFAULT true CHECK (id),
    suspended  boolean     NOT NULL DEFAULT false,
    reason     text        NOT NULL DEFAULT 'initial' CHECK (reason ~ '^[a-z][a-z0-9_.-]{0,63}$'),
    staff_id   uuid,
    changed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
INSERT INTO op.dispatch_control DEFAULT VALUES;

CREATE TABLE op.dispatch_control_log (
    id           uuid        PRIMARY KEY,
    recorded_at  timestamptz NOT NULL,
    action       text        NOT NULL CHECK (action IN ('suspended', 'resumed', 'stopping_rule_breached')),
    reason       text        NOT NULL CHECK (reason ~ '^[a-z][a-z0-9_.-]{0,63}$'),
    staff_id     uuid,
    failures     int,
    window_size  int,
    payload_salt bytea       NOT NULL DEFAULT uuid_send(gen_random_uuid()) CHECK (octet_length(payload_salt) = 16),
    CHECK ((action = 'stopping_rule_breached') = (failures IS NOT NULL AND window_size IS NOT NULL)),
    CHECK (action <> 'resumed' OR staff_id IS NOT NULL)
);

CREATE FUNCTION op.control_log_append_only() RETURNS trigger
LANGUAGE plpgsql
SET search_path = op, pg_temp
AS $$
BEGIN
    RAISE EXCEPTION 'op.dispatch_control_log is append-only' USING ERRCODE = 'SA403';
END
$$;
CREATE TRIGGER dispatch_control_log_append_only BEFORE UPDATE OR DELETE ON op.dispatch_control_log
    FOR EACH ROW EXECUTE FUNCTION op.control_log_append_only();
CREATE TRIGGER dispatch_control_log_no_truncate BEFORE TRUNCATE ON op.dispatch_control_log
    FOR EACH STATEMENT EXECUTE FUNCTION op.control_log_append_only();

-- One register row and its audit entry (event dispatch.<action>, no
-- incident, the payload salted and hashed like incident events).
CREATE FUNCTION op.log_control(p_action text, p_reason text, p_staff uuid, p_failures int, p_window int) RETURNS void
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_id      uuid := uuidv7();
    v_at      timestamptz := clock_timestamp();
    v_salt    bytea;
    v_payload jsonb := jsonb_strip_nulls(jsonb_build_object('reason', p_reason, 'failures', p_failures, 'window', p_window));
    v_entry   uuid := uuidv7();
BEGIN
    INSERT INTO op.dispatch_control_log (id, recorded_at, action, reason, staff_id, failures, window_size)
    VALUES (v_id, v_at, p_action, p_reason, p_staff, p_failures, p_window)
    RETURNING payload_salt INTO v_salt;
    INSERT INTO op.outbox (id, topic, payload)
    VALUES (v_entry, 'audit', jsonb_build_object(
        'entry_id',       v_entry,
        'event_id',       v_id,
        'occurred_at',    v_at,
        'event_type',     'dispatch.' || p_action,
        'incident_id',    NULL,
        'staff_id',       p_staff,
        'logic_version',  NULL,
        'payload_sha256', encode(sha256(v_salt || convert_to(v_payload::text, 'UTF8')), 'hex')));
END
$$;

CREATE FUNCTION op.set_dispatch_suspended(p_suspend boolean, p_reason text, p_staff uuid) RETURNS boolean
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
DECLARE
    v_now boolean;
BEGIN
    SELECT c.suspended INTO v_now FROM op.dispatch_control c FOR UPDATE;
    IF v_now = p_suspend THEN
        RETURN false;
    END IF;
    UPDATE op.dispatch_control c
       SET suspended = p_suspend, reason = p_reason, staff_id = p_staff, changed_at = clock_timestamp();
    PERFORM op.log_control(CASE WHEN p_suspend THEN 'suspended' ELSE 'resumed' END, p_reason, p_staff, NULL, NULL);
    UPDATE op.dispatch_timers t SET due_at = clock_timestamp(), gen = t.gen + 1;
    RETURN true;
END
$$;

-- Console: a coordinator's decision, always with a staff identity.
CREATE FUNCTION op.suspend_dispatch(p_reason text, p_staff uuid) RETURNS boolean
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
BEGIN
    IF p_staff IS NULL THEN
        PERFORM op.reject('suspending dispatch needs the coordinator''s staff identity');
    END IF;
    RETURN op.set_dispatch_suspended(true, p_reason, p_staff);
END
$$;

CREATE FUNCTION op.resume_dispatch(p_reason text, p_staff uuid) RETURNS boolean
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
BEGIN
    IF p_staff IS NULL THEN
        PERFORM op.reject('resuming dispatch needs the coordinator''s staff identity');
    END IF;
    RETURN op.set_dispatch_suspended(false, p_reason, p_staff);
END
$$;

-- The dispatch leader: the stopping rule is breached. Registers the breach
-- with its numbers and suspends; nothing if already suspended.
CREATE FUNCTION op.auto_suspend_dispatch(p_failures int, p_window int) RETURNS boolean
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$
BEGIN
    PERFORM 1 FROM op.dispatch_control c WHERE NOT c.suspended FOR UPDATE;
    IF NOT FOUND THEN
        RETURN false;
    END IF;
    PERFORM op.log_control('stopping_rule_breached', 'stopping_rule', NULL, p_failures, p_window);
    RETURN op.set_dispatch_suspended(true, 'stopping_rule', NULL);
END
$$;

CREATE FUNCTION op.dispatch_suspended() RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = op, pg_temp
AS $$ SELECT c.suspended FROM op.dispatch_control c $$;

GRANT EXECUTE ON FUNCTION op.suspend_dispatch(text, uuid), op.resume_dispatch(text, uuid) TO op_console;
GRANT EXECUTE ON FUNCTION op.auto_suspend_dispatch(int, int) TO op_dispatch;
GRANT EXECUTE ON FUNCTION op.dispatch_suspended() TO op_dispatch, op_intake, op_ivr, op_console, op_readonly;
GRANT SELECT ON op.dispatch_control, op.dispatch_control_log TO op_console, op_readonly;
