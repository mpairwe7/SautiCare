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
SELECT id, state, severity, t2, zone_col, zone_row, ward FROM op.incidents WHERE id = sqlc.arg(id)::uuid;

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

-- name: DispatchEscalations :many
SELECT DISTINCT (payload ->> 'reason')::text AS reason FROM op.incident_events
WHERE incident_id = sqlc.arg(incident_id)::uuid AND type = 'incident.escalated' ORDER BY 1;

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
