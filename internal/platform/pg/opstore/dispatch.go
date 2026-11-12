package opstore

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg/opstore/opdb"
	"sautihealth.com/sauti/internal/responder"
)

// Dispatch store (migration 0015, ADR-0025). Every write is one SECURITY
// DEFINER function that records the fact and its incident event together.

// IssueAlert alerts one responder (op_dispatch).
func (s *Store) IssueAlert(ctx context.Context, p dispatch.IssueParams) (dispatch.Issued, error) {
	r, err := s.q.IssueAlert(ctx, opdb.IssueAlertParams{IncidentID: pgID(p.IncidentID), ResponderID: pgID(p.ResponderID),
		Role: string(p.Role), Ring: int32(p.Ring), Wave: int32(p.Wave), LogicVersion: strPtr(p.LogicVersion)}) //nolint:gosec // ring ≤ 99, wave ≤ 9999 (checked in SQL)
	if err != nil {
		return dispatch.Issued{}, mapErr(err)
	}
	return dispatch.Issued{AlertID: uuid.UUID(r.AlertID.Bytes), SentAt: r.SentAt.Time.UTC(), Duplicate: r.Duplicate}, nil
}

// AcceptAlert records a responder's acceptance; the first per role wins
// (op_intake, op_ivr).
func (s *Store) AcceptAlert(ctx context.Context, alert, responderID uuid.UUID) (dispatch.AcceptOutcome, error) {
	r, err := s.q.AcceptAlert(ctx, opdb.AcceptAlertParams{AlertID: pgID(alert), ResponderID: pgID(responderID)})
	if err != nil {
		return dispatch.AcceptOutcome{}, mapErr(err)
	}
	return dispatch.AcceptOutcome{Result: dispatch.AcceptResult(r.Result), IncidentID: uuid.UUID(r.IncidentID.Bytes),
		Role: responder.Role(r.Role)}, nil
}

// DeclineAlert records a decline (no reason until decision 6's list arrives)
// and returns the alert's outcome (op_intake, op_ivr).
func (s *Store) DeclineAlert(ctx context.Context, alert, responderID uuid.UUID) (string, error) {
	out, err := s.q.DeclineAlert(ctx, opdb.DeclineAlertParams{AlertID: pgID(alert), ResponderID: pgID(responderID)})
	return out, mapErr(err)
}

// CloseAlert settles a pending alert as no_response or stood_down; false if
// it was already settled (op_dispatch).
func (s *Store) CloseAlert(ctx context.Context, alert uuid.UUID, outcome string) (bool, error) {
	ok, err := s.q.CloseAlert(ctx, opdb.CloseAlertParams{AlertID: pgID(alert), Outcome: outcome})
	return ok, mapErr(err)
}

// RecordForward forwards the incident to a facility (op_dispatch).
func (s *Store) RecordForward(ctx context.Context, incidentID uuid.UUID, facility, gazetteerVersion, logicVersion string) (bool, error) {
	ok, err := s.q.RecordForward(ctx, opdb.RecordForwardParams{IncidentID: pgID(incidentID), FacilityID: facility,
		GazetteerVersion: gazetteerVersion, LogicVersion: strPtr(logicVersion)})
	return ok, mapErr(err)
}

// AcknowledgeForward records the facility's acknowledgement (op_ivr).
func (s *Store) AcknowledgeForward(ctx context.Context, incidentID uuid.UUID, facility string) (bool, error) {
	ok, err := s.q.AcknowledgeForward(ctx, opdb.AcknowledgeForwardParams{IncidentID: pgID(incidentID), FacilityID: facility})
	return ok, mapErr(err)
}

// TimeOutForward records that the facility did not acknowledge in time
// (op_dispatch).
func (s *Store) TimeOutForward(ctx context.Context, incidentID uuid.UUID, facility string) (bool, error) {
	ok, err := s.q.TimeOutForward(ctx, opdb.TimeOutForwardParams{IncidentID: pgID(incidentID), FacilityID: facility})
	return ok, mapErr(err)
}

// NotifyCaller records a caller notice, once per kind (op_dispatch).
func (s *Store) NotifyCaller(ctx context.Context, incidentID uuid.UUID, kind, facility string) (bool, error) {
	ok, err := s.q.NotifyCaller(ctx, opdb.NotifyCallerParams{IncidentID: pgID(incidentID), Kind: kind, FacilityID: strPtr(facility)})
	return ok, mapErr(err)
}

// DispatchEscalate alerts the coordinator, once per reason (op_dispatch).
func (s *Store) DispatchEscalate(ctx context.Context, incidentID uuid.UUID, reason string, role responder.Role, logicVersion string) (bool, error) {
	ok, err := s.q.DispatchEscalate(ctx, opdb.DispatchEscalateParams{IncidentID: pgID(incidentID), Reason: reason,
		Role: strPtr(string(role)), LogicVersion: strPtr(logicVersion)})
	return ok, mapErr(err)
}

// BeginAttempt claims a delivery attempt before the provider call; fresh is
// false when it was claimed before (never send it again).
func (s *Store) BeginAttempt(ctx context.Context, alert uuid.UUID, purpose, channel string) (uuid.UUID, bool, error) {
	r, err := s.q.BeginAttempt(ctx, opdb.BeginAttemptParams{AlertID: pgID(alert), Purpose: purpose, Channel: channel})
	if err != nil {
		return uuid.UUID{}, false, mapErr(err)
	}
	return uuid.UUID(r.AttemptID.Bytes), r.Fresh, nil
}

// FinishAttempt settles a claimed attempt as sent, failed or unknown.
func (s *Store) FinishAttempt(ctx context.Context, attempt uuid.UUID, status, providerRef string) (bool, error) {
	ok, err := s.q.FinishAttempt(ctx, opdb.FinishAttemptParams{AttemptID: pgID(attempt), Status: status, ProviderRef: strPtr(providerRef)})
	return ok, mapErr(err)
}

// ClaimDueDispatch leases up to limit due incidents, most urgent first.
func (s *Store) ClaimDueDispatch(ctx context.Context, limit int, lease time.Duration) ([]dispatch.Due, error) {
	rows, err := s.q.ClaimDueDispatch(ctx, opdb.ClaimDueDispatchParams{MaxRows: int32(min(limit, 1000)), //nolint:gosec // bounded above
		Lease: pgtype.Interval{Microseconds: lease.Microseconds(), Valid: true}})
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]dispatch.Due, 0, len(rows))
	for _, r := range rows {
		out = append(out, dispatch.Due{IncidentID: uuid.UUID(r.IncidentID.Bytes), Gen: r.Gen})
	}
	return out, nil
}

// ScheduleDispatch stores the planner's next wake (nil: none until a new
// fact) for the generation it planned on.
func (s *Store) ScheduleDispatch(ctx context.Context, incidentID uuid.UUID, gen int64, due *time.Time) error {
	return mapErr(s.q.ScheduleDispatch(ctx, opdb.ScheduleDispatchParams{IncidentID: pgID(incidentID), Gen: gen, DueAt: tsArg(due)}))
}

// DispatchSnapshot reads one incident's dispatch facts in one consistent
// snapshot (op_dispatch, op_readonly).
func (s *Store) DispatchSnapshot(ctx context.Context, incidentID uuid.UUID) (dispatch.Snapshot, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return dispatch.Snapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	id := pgID(incidentID)
	inc, err := q.DispatchIncident(ctx, id)
	if err != nil {
		return dispatch.Snapshot{}, fmt.Errorf("opstore: dispatch snapshot: %w", notFound(err))
	}
	snap := dispatch.Snapshot{IncidentID: incidentID, State: incident.State(inc.State), Severity: incident.Severity(deref(inc.Severity)),
		DispatchedAt: tsOpt(inc.T2), Ward: deref(inc.Ward), LocationQuality: deref(inc.LocationQuality), Assigned: map[responder.Role]uuid.UUID{}}
	if inc.ZoneCol != nil && inc.ZoneRow != nil {
		snap.Zone = &incident.Zone{Col: int(*inc.ZoneCol), Row: int(*inc.ZoneRow)}
	}
	alerts, err := q.DispatchAlerts(ctx, id)
	if err != nil {
		return dispatch.Snapshot{}, err
	}
	for _, a := range alerts {
		snap.Alerts = append(snap.Alerts, dispatch.Alert{ID: uuid.UUID(a.ID.Bytes), ResponderID: uuid.UUID(a.ResponderID.Bytes),
			Role: responder.Role(a.Role), Ring: int(a.Ring), Wave: int(a.Wave), SentAt: a.SentAt.Time.UTC(), Outcome: a.Outcome})
	}
	assigned, err := q.DispatchAssignments(ctx, id)
	if err != nil {
		return dispatch.Snapshot{}, err
	}
	for _, a := range assigned {
		snap.Assigned[responder.Role(a.Role)] = uuid.UUID(a.ResponderID.Bytes)
	}
	forwards, err := q.DispatchForwards(ctx, id)
	if err != nil {
		return dispatch.Snapshot{}, err
	}
	for _, f := range forwards {
		snap.Forwards = append(snap.Forwards, dispatch.ForwardState{FacilityID: f.FacilityID, SentAt: f.SentAt.Time.UTC(),
			Acked: f.AcknowledgedAt.Valid, TimedOut: f.TimedOutAt.Valid})
	}
	if snap.CallerNotified, err = q.DispatchNotices(ctx, id); err != nil {
		return dispatch.Snapshot{}, err
	}
	if snap.Escalations, err = q.DispatchEscalations(ctx, id); err != nil {
		return dispatch.Snapshot{}, err
	}
	red, err := q.DispatchRedSince(ctx, id)
	if err != nil {
		return dispatch.Snapshot{}, err
	}
	snap.RedSince = tsOpt(red)
	return snap, tx.Commit(ctx)
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return incident.ErrNotFound
	}
	return err
}

// ResponderHistory returns each responder's settled alerts of the last 90
// days for ranking (op_dispatch).
func (s *Store) ResponderHistory(ctx context.Context) (map[uuid.UUID]dispatch.History, error) {
	rows, err := s.q.ResponderHistory(ctx)
	if err != nil {
		return nil, fmt.Errorf("opstore: responder history: %w", err)
	}
	out := make(map[uuid.UUID]dispatch.History, len(rows))
	for _, r := range rows {
		out[uuid.UUID(r.ResponderID.Bytes)] = dispatch.History{Alerted: int(r.Alerted), Accepted: int(r.Accepted),
			LastDispatched: tsOpt(r.LastDispatched)}
	}
	return out, nil
}

// BusyResponders returns the responders assigned to an open incident.
func (s *Store) BusyResponders(ctx context.Context) (map[uuid.UUID]bool, error) {
	ids, err := s.q.BusyResponders(ctx)
	if err != nil {
		return nil, fmt.Errorf("opstore: busy responders: %w", err)
	}
	out := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		out[uuid.UUID(id.Bytes)] = true
	}
	return out, nil
}

// Now is the database clock (rule 3.4: server-side time); the dispatch
// planner compares it with timestamps the database recorded.
func (s *Store) Now(ctx context.Context) (time.Time, error) {
	t, err := s.q.DatabaseNow(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("opstore: now: %w", err)
	}
	return t.Time.UTC(), nil
}

// DispatchSuspended reports whether dispatch is suspended (O6); it is the
// dispatch.Suspension the worker and intake read.
func (s *Store) DispatchSuspended(ctx context.Context) (bool, error) {
	on, err := s.q.DispatchSuspended(ctx)
	if err != nil {
		return false, fmt.Errorf("opstore: dispatch suspended: %w", err)
	}
	return on, nil
}

// SuspendDispatch suspends dispatch for a coordinator (op_console); false if
// already suspended.
func (s *Store) SuspendDispatch(ctx context.Context, reason string, staff uuid.UUID) (bool, error) {
	ok, err := s.q.SuspendDispatch(ctx, opdb.SuspendDispatchParams{Reason: reason, StaffID: staffArg(staff)})
	return ok, mapErr(err)
}

// ResumeDispatch resumes dispatch for a coordinator (op_console).
func (s *Store) ResumeDispatch(ctx context.Context, reason string, staff uuid.UUID) (bool, error) {
	ok, err := s.q.ResumeDispatch(ctx, opdb.ResumeDispatchParams{Reason: reason, StaffID: staffArg(staff)})
	return ok, mapErr(err)
}

// AutoSuspendDispatch registers a stopping-rule breach and suspends
// (op_dispatch); false if already suspended.
func (s *Store) AutoSuspendDispatch(ctx context.Context, failures, window int) (bool, error) {
	ok, err := s.q.AutoSuspendDispatch(ctx, opdb.AutoSuspendDispatchParams{Failures: int32(failures), WindowSize: int32(window)}) //nolint:gosec // window ≤ config bounds
	return ok, mapErr(err)
}

// RedDispatchOutcomes returns up to window settled red incidents' outcomes,
// newest first (true = accepted within redWindow of T2), for the stopping
// rule; only incidents dispatched since the latest resumption count.
func (s *Store) RedDispatchOutcomes(ctx context.Context, window int, redWindow time.Duration) ([]bool, error) {
	out, err := s.q.RedDispatchOutcomes(ctx, opdb.RedDispatchOutcomesParams{
		RedWindow:  pgtype.Interval{Microseconds: redWindow.Microseconds(), Valid: true},
		WindowSize: int32(min(window, 10000)), //nolint:gosec // bounded above
	})
	if err != nil {
		return nil, fmt.Errorf("opstore: red outcomes: %w", err)
	}
	return out, nil
}

// ControlLog returns the newest n rows of the dispatch control register.
func (s *Store) ControlLog(ctx context.Context, n int) ([]dispatch.ControlEntry, error) {
	rows, err := s.q.ControlLog(ctx, int32(min(n, 10000))) //nolint:gosec // bounded above
	if err != nil {
		return nil, fmt.Errorf("opstore: control log: %w", err)
	}
	out := make([]dispatch.ControlEntry, 0, len(rows))
	for _, r := range rows {
		e := dispatch.ControlEntry{At: r.RecordedAt.Time.UTC(), Action: r.Action, Reason: r.Reason}
		if r.StaffID.Valid {
			id := uuid.UUID(r.StaffID.Bytes)
			e.StaffID = &id
		}
		if r.Failures != nil && r.WindowSize != nil {
			e.Failures, e.Window = int(*r.Failures), int(*r.WindowSize)
		}
		out = append(out, e)
	}
	return out, nil
}

// staffArg is NULL for the zero UUID (so the database refuses it).
func staffArg(id uuid.UUID) pgtype.UUID {
	if id == (uuid.UUID{}) {
		return pgtype.UUID{}
	}
	return pgID(id)
}

// OldestOverdueDispatch is how late the most overdue dispatch wake is (0:
// none due), for dispatch-worker's metrics.
func (s *Store) OldestOverdueDispatch(ctx context.Context) (time.Duration, error) {
	secs, err := s.q.OldestOverdueDispatch(ctx)
	if err != nil {
		return 0, fmt.Errorf("opstore: overdue dispatch: %w", err)
	}
	return time.Duration(secs * float64(time.Second)), nil
}
