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
SELECT role, responder_id, accepted_at FROM op.assignments
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

-- name: BeginNoticeAttempt :one
SELECT a.attempt_id::uuid AS attempt_id, a.fresh::boolean AS fresh
FROM op.begin_notice_attempt(sqlc.arg(incident_id)::uuid, sqlc.arg(kind)::text, sqlc.arg(channel)::text) AS a(attempt_id, fresh);

-- name: FinishNoticeAttempt :one
SELECT op.finish_notice_attempt(sqlc.arg(attempt_id)::uuid, sqlc.arg(status)::text, sqlc.narg(provider_ref)::text)::boolean AS changed;

-- name: RecordDeliveryReport :one
SELECT op.record_delivery_report(sqlc.arg(provider_ref)::text, sqlc.arg(status)::text, sqlc.narg(failure)::text,
                                 sqlc.arg(final)::boolean)::boolean AS known;

-- name: CallerContact :many
SELECT c.source::text AS source, c.row_id::uuid AS row_id, c.ct::bytea AS ct, c.key_id::text AS key_id
FROM op.caller_contact(sqlc.arg(incident_id)::uuid) AS c(source, row_id, ct, key_id);

-- name: ClaimAvailabilityReminders :many
SELECT c.responder_id::uuid AS responder_id, c.until::timestamptz AS until
FROM op.claim_availability_reminders(sqlc.arg(within)::interval, sqlc.arg(max_rows)::int) AS c(responder_id, until);

-- name: ResponderPhone :one
SELECT phone_ct, phone_key_id FROM op.responders WHERE id = sqlc.arg(id)::uuid;

-- name: MessageFacts :one
SELECT reference, severity, category, ward, landmark, language FROM op.incidents WHERE id = sqlc.arg(id)::uuid;

-- name: NoticeFacility :one
SELECT coalesce(facility_id, '')::text AS facility_id FROM op.caller_notices
WHERE incident_id = sqlc.arg(incident_id)::uuid AND kind = sqlc.arg(kind)::text;

-- The alert a responder's menu session is about (pinned at its first screen).
-- name: MenuAlert :many
SELECT m.alert_id::uuid AS id, m.incident_id::uuid AS incident_id, m.role::text AS role, m.outcome::text AS outcome,
       m.reference::text AS reference, coalesce(m.severity, '')::text AS severity, coalesce(m.category, '')::text AS category,
       coalesce(m.ward, '')::text AS ward, coalesce(m.landmark, '')::text AS landmark, coalesce(m.language, '')::text AS language,
       m.assigned::boolean AS assigned
FROM op.responder_menu_alert(sqlc.arg(session_key)::bytea, sqlc.arg(responder_id)::uuid)
     AS m(alert_id, incident_id, role, outcome, reference, severity, category, ward, landmark, language, assigned);

-- name: AvailableUntil :many
SELECT upper(period)::timestamptz AS until FROM op.availability
WHERE responder_id = sqlc.arg(responder_id)::uuid AND period @> clock_timestamp();

-- A responder's alerts, newest first (scenario 10: decline reasons shown).
-- name: ResponderAlertHistory :many
SELECT id, incident_id, role, sent_at, outcome, decline_reason, responded_at FROM op.alerts
WHERE responder_id = sqlc.arg(responder_id)::uuid ORDER BY sent_at DESC LIMIT sqlc.arg(max_rows)::int;

-- Alerts due a voice call (ivr-controller): pending, sent at least `after`
-- ago, no voice attempt yet, dispatch not suspended, the responder still
-- eligible (switched off or a gate lapsed: not rung).
-- name: DueVoiceAlerts :many
SELECT a.id, a.responder_id FROM op.alerts a
WHERE a.outcome = 'pending' AND a.sent_at <= clock_timestamp() - sqlc.arg(after)::interval AND NOT op.dispatch_suspended()
  AND op.responder_eligible_now(a.responder_id)
  AND NOT EXISTS (SELECT 1 FROM op.alert_attempts x WHERE x.alert_id = a.id AND x.channel = 'voice')
ORDER BY a.sent_at LIMIT sqlc.arg(max_rows)::int;

-- The alert an answered voice call is about (by its attempt).
-- name: AlertForAttempt :one
SELECT a.id, a.incident_id, a.responder_id, a.role, a.outcome, i.reference, i.severity, i.category, i.ward, i.landmark, i.language
FROM op.alert_attempts x JOIN op.alerts a ON a.id = x.alert_id JOIN op.incidents i ON i.id = a.incident_id
WHERE x.id = sqlc.arg(attempt_id)::uuid AND x.channel = 'voice';

-- name: BeginForwardAttempt :one
SELECT a.attempt_id::uuid AS attempt_id, a.fresh::boolean AS fresh
FROM op.begin_forward_attempt(sqlc.arg(incident_id)::uuid, sqlc.arg(facility_id)::text, sqlc.arg(channel)::text) AS a(attempt_id, fresh);

-- name: FinishForwardAttempt :one
SELECT op.finish_forward_attempt(sqlc.arg(attempt_id)::uuid, sqlc.arg(status)::text, sqlc.narg(provider_ref)::text)::boolean AS changed;

-- Forwards awaiting acknowledgement and not yet called (ivr-controller), of
-- incidents still open (a closed or merged one takes no acknowledgement).
-- name: DueForwardCalls :many
SELECT f.incident_id, f.facility_id FROM op.forwards f JOIN op.incidents i ON i.id = f.incident_id
WHERE f.acknowledged_at IS NULL AND f.timed_out_at IS NULL AND i.state NOT IN ('closed', 'merged')
  AND NOT EXISTS (SELECT 1 FROM op.forward_attempts x WHERE x.incident_id = f.incident_id AND x.facility_id = f.facility_id AND x.channel = 'voice')
ORDER BY f.sent_at LIMIT sqlc.arg(max_rows)::int;

-- name: ClaimCoordinatorCalls :many
SELECT c.incident_id::uuid AS incident_id, c.seq::bigint AS seq, coalesce(c.reason, '')::text AS reason,
       c.reference::text AS reference, c.calls::int AS calls
FROM op.claim_coordinator_calls(sqlc.arg(retry)::interval, sqlc.arg(max_rows)::int) AS c(incident_id, seq, reason, reference, calls);

-- name: AcknowledgeCoordinatorCall :exec
SELECT op.acknowledge_coordinator_call(sqlc.arg(incident_id)::uuid, sqlc.arg(seq)::bigint);

-- name: BridgeTargetsForResponder :many
SELECT t.incident_id::uuid AS incident_id, t.reference::text AS reference, t.role::text AS role, t.responder_id::uuid AS responder_id
FROM op.bridge_targets_for_responder(sqlc.arg(responder_id)::uuid) AS t(incident_id, reference, role, responder_id);

-- name: BridgeTargetsForCaller :many
SELECT t.incident_id::uuid AS incident_id, t.reference::text AS reference, t.role::text AS role, t.responder_id::uuid AS responder_id
FROM op.bridge_targets_for_caller(sqlc.arg(macs)::bytea[]) AS t(incident_id, reference, role, responder_id);

-- name: BeginBridgeCall :one
SELECT op.begin_bridge_call(sqlc.arg(incident_id)::uuid, sqlc.arg(initiated_by)::text)::uuid AS id;

-- name: BridgeCallAnswered :exec
SELECT op.bridge_call_answered(sqlc.arg(id)::uuid);

-- name: FinishBridgeCall :one
SELECT op.finish_bridge_call(sqlc.arg(id)::uuid, sqlc.arg(result)::text)::boolean AS changed;

-- name: DispatchSurge :one
SELECT s.waiting::int AS waiting, s.available::int AS available, s.surge::boolean AS surge
FROM op.dispatch_surge() AS s(waiting, available, surge);
