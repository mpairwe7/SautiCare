-- Dispatch store (migration 0015). Writes go through the SECURITY DEFINER
-- functions only; reads build the planner's snapshot.

-- name: IssueAlert :one
SELECT a.alert_id::uuid AS alert_id, a.sent_at::timestamptz AS sent_at, a.duplicate::boolean AS duplicate
FROM op.issue_alert(sqlc.arg(incident_id)::uuid, sqlc.arg(responder_id)::uuid, sqlc.arg(role)::text,
                    sqlc.arg(ring)::int, sqlc.arg(wave)::int, sqlc.narg(logic_version)::text) AS a(alert_id, sent_at, duplicate);

-- name: AcceptAlert :one
SELECT a.result::text AS result, a.incident_id::uuid AS incident_id, a.role::text AS role
FROM op.accept_alert(sqlc.arg(alert_id)::uuid, sqlc.arg(responder_id)::uuid) AS a(result, incident_id, role);

-- name: DeclineAlert :one
SELECT op.decline_alert(sqlc.arg(alert_id)::uuid, sqlc.arg(responder_id)::uuid, sqlc.narg(reason)::text)::text AS outcome;

-- name: CloseAlert :one
SELECT op.close_alert(sqlc.arg(alert_id)::uuid, sqlc.arg(outcome)::text)::boolean AS changed;

-- name: RecordForward :one
SELECT op.record_forward(sqlc.arg(incident_id)::uuid, sqlc.arg(facility_id)::text, sqlc.arg(gazetteer_version)::text,
                         sqlc.narg(logic_version)::text)::boolean AS changed;

-- name: AcknowledgeForward :one
SELECT op.acknowledge_forward(sqlc.arg(incident_id)::uuid, sqlc.arg(facility_id)::text)::boolean AS changed;

-- name: TimeOutForward :one
SELECT op.time_out_forward(sqlc.arg(incident_id)::uuid, sqlc.arg(facility_id)::text)::boolean AS changed;

-- name: NotifyCaller :one
SELECT op.notify_caller(sqlc.arg(incident_id)::uuid, sqlc.arg(kind)::text, sqlc.narg(facility_id)::text)::boolean AS changed;

-- name: DispatchEscalate :one
SELECT op.dispatch_escalate(sqlc.arg(incident_id)::uuid, sqlc.arg(reason)::text, sqlc.narg(role)::text,
                            sqlc.narg(logic_version)::text)::boolean AS changed;

-- name: BeginAttempt :one
SELECT a.attempt_id::uuid AS attempt_id, a.fresh::boolean AS fresh
FROM op.begin_attempt(sqlc.arg(alert_id)::uuid, sqlc.arg(purpose)::text, sqlc.arg(channel)::text) AS a(attempt_id, fresh);

-- name: FinishAttempt :one
SELECT op.finish_attempt(sqlc.arg(attempt_id)::uuid, sqlc.arg(status)::text, sqlc.narg(provider_ref)::text)::boolean AS changed;

-- name: ClaimDueDispatch :many
SELECT d.incident_id::uuid AS incident_id, d.gen::bigint AS gen
FROM op.claim_due_dispatch(sqlc.arg(max_rows)::int, sqlc.arg(lease)::interval) AS d(incident_id, gen);

-- name: ScheduleDispatch :exec
SELECT op.schedule_dispatch(sqlc.arg(incident_id)::uuid, sqlc.arg(gen)::bigint, sqlc.narg(due_at)::timestamptz);

-- name: DispatchIncident :one
SELECT id, state, severity, t2, zone_col, zone_row, ward, location_quality FROM op.incidents WHERE id = sqlc.arg(id)::uuid;

-- name: DispatchAlerts :many
SELECT id, responder_id, role, ring, wave, sent_at, outcome FROM op.alerts
WHERE incident_id = sqlc.arg(incident_id)::uuid ORDER BY sent_at, id;

-- name: DispatchAssignments :many
SELECT role, responder_id FROM op.assignments
WHERE incident_id = sqlc.arg(incident_id)::uuid AND released_at IS NULL;

-- name: DispatchForwards :many
SELECT facility_id, sent_at, acknowledged_at, timed_out_at FROM op.forwards
WHERE incident_id = sqlc.arg(incident_id)::uuid ORDER BY sent_at, facility_id;

-- name: DispatchNotices :many
SELECT kind FROM op.caller_notices WHERE incident_id = sqlc.arg(incident_id)::uuid ORDER BY created_at, kind;

-- Escalation keys (incident.EscalationKey): once per reason and role.
-- name: DispatchEscalations :many
SELECT DISTINCT ('escalate:' || (payload ->> 'reason') || ':' || coalesce(payload ->> 'role', '-'))::text AS escalation
FROM op.incident_events
WHERE incident_id = sqlc.arg(incident_id)::uuid AND type = 'incident.escalated' ORDER BY 1;

-- When the incident became red (classified red, or raised to red by a merge).
-- name: DispatchRedSince :one
SELECT min(e.recorded_at)::timestamptz AS red_since FROM op.incident_events e
WHERE e.incident_id = sqlc.arg(incident_id)::uuid AND e.payload ->> 'severity' = 'red'
  AND e.type IN ('incident.classified', 'incident.severity_raised');

-- Response history for ranking (decision 4): settled alerts of the last 90
-- days per responder; "dispatched" is an acceptance.
-- name: ResponderHistory :many
SELECT responder_id,
       count(*) FILTER (WHERE outcome <> 'pending')::int AS alerted,
       count(*) FILTER (WHERE outcome = 'accepted')::int AS accepted,
       max(responded_at) FILTER (WHERE outcome = 'accepted')::timestamptz AS last_dispatched
FROM op.alerts WHERE sent_at > clock_timestamp() - interval '90 days'
GROUP BY responder_id;

-- Responders on an open incident are not alerted for another.
-- name: BusyResponders :many
SELECT DISTINCT s.responder_id FROM op.assignments s JOIN op.incidents i ON i.id = s.incident_id
WHERE s.released_at IS NULL AND i.state NOT IN ('closed', 'merged');

-- name: DatabaseNow :one
SELECT clock_timestamp()::timestamptz AS now;

-- name: DispatchSuspended :one
SELECT op.dispatch_suspended()::boolean AS suspended;

-- name: SuspendDispatch :one
SELECT op.suspend_dispatch(sqlc.arg(reason)::text, sqlc.narg(staff_id)::uuid)::boolean AS changed;

-- name: ResumeDispatch :one
SELECT op.resume_dispatch(sqlc.arg(reason)::text, sqlc.narg(staff_id)::uuid)::boolean AS changed;

-- name: AutoSuspendDispatch :one
SELECT op.auto_suspend_dispatch(sqlc.arg(failures)::int, sqlc.arg(window_size)::int)::boolean AS changed;

-- name: ControlLog :many
SELECT recorded_at, action, reason, staff_id, failures, window_size FROM op.dispatch_control_log
ORDER BY recorded_at DESC, id DESC LIMIT sqlc.arg(max_rows)::int;

-- Settled red incidents for the stopping rule (Guide §14; review I2),
-- newest T2 first. A success is an acceptance (any role) within the red
-- window of T2 (decision 2: 3 minutes); a failure is the window passing
-- without one. Not judged: an incident closed inside the window without an
-- acceptance (a caller's or coordinator's decision, not a dispatch outcome),
-- one whose window is not over, merged incidents, and anything dispatched
-- before the latest resumption — so a coordinator's resume is never undone
-- by the evidence that caused the suspension.
-- name: RedDispatchOutcomes :many
WITH anchor AS (
    SELECT coalesce(max(l.recorded_at), '-infinity'::timestamptz) AS at
      FROM op.dispatch_control_log l WHERE l.action = 'resumed'),
red AS (
    SELECT i.id, i.t2, i.t4,
           (SELECT min(e.recorded_at) FROM op.incident_events e
             WHERE e.incident_id = i.id AND e.type = 'incident.closed') AS closed_at
      FROM op.incidents i, anchor
     WHERE i.severity = 'red' AND i.t2 IS NOT NULL AND i.state <> 'merged' AND i.t2 > anchor.at)
SELECT (red.t4 IS NOT NULL AND red.t4 <= red.t2 + sqlc.arg(red_window)::interval)::boolean AS succeeded
  FROM red
 WHERE (red.t4 IS NOT NULL AND red.t4 <= red.t2 + sqlc.arg(red_window)::interval)
    OR (clock_timestamp() >= red.t2 + sqlc.arg(red_window)::interval
        AND (red.closed_at IS NULL OR red.closed_at >= red.t2 + sqlc.arg(red_window)::interval))
 ORDER BY red.t2 DESC, red.id DESC
 LIMIT sqlc.arg(window_size)::int;

-- How late the most overdue dispatch wake is (0 when none is due): a growing
-- value means dispatch is not keeping up or not running (review I8).
-- name: OldestOverdueDispatch :one
SELECT coalesce(extract(epoch FROM clock_timestamp() - min(due_at)), 0)::float8 AS seconds
FROM op.dispatch_timers WHERE due_at <= clock_timestamp();
