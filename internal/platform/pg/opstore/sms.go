package opstore

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgtype"

	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg/opstore/opdb"
	"sautihealth.com/sauti/internal/responder"
)

// CallerContact returns the caller's sealed number for SMS (op_dispatch):
// the screen 8 callback number when given, otherwise the number called
// from; false when the caller withheld it and gave none.
func (s *Store) CallerContact(ctx context.Context, incidentID uuid.UUID) (incident.SealedContact, bool, error) {
	rows, err := s.q.CallerContact(ctx, pgID(incidentID))
	if err != nil {
		return incident.SealedContact{}, false, fmt.Errorf("opstore: caller contact: %w", err)
	}
	if len(rows) == 0 {
		return incident.SealedContact{}, false, nil
	}
	r := rows[0]
	col := "phone"
	if r.Source == "incident_identifiers" {
		col = "ct"
	}
	return incident.SealedContact{Sealed: crypto.Sealed{KeyID: r.KeyID, Ciphertext: r.Ct},
		AAD: crypto.AAD{Store: "operational", Table: r.Source, Column: col, RowID: uuid.UUID(r.RowID.Bytes).String()}}, true, nil
}

// BeginNoticeAttempt claims a caller-notice attempt before the provider
// call; fresh only once, and only for a recorded notice.
func (s *Store) BeginNoticeAttempt(ctx context.Context, incidentID uuid.UUID, kind, channel string) (uuid.UUID, bool, error) {
	r, err := s.q.BeginNoticeAttempt(ctx, opdb.BeginNoticeAttemptParams{IncidentID: pgID(incidentID), Kind: kind, Channel: channel})
	if err != nil {
		return uuid.UUID{}, false, mapErr(err)
	}
	return uuid.UUID(r.AttemptID.Bytes), r.Fresh, nil
}

// FinishNoticeAttempt settles a claimed notice attempt.
func (s *Store) FinishNoticeAttempt(ctx context.Context, attempt uuid.UUID, status, providerRef string) (bool, error) {
	ok, err := s.q.FinishNoticeAttempt(ctx, opdb.FinishNoticeAttemptParams{AttemptID: pgID(attempt), Status: status, ProviderRef: strPtr(providerRef)})
	return ok, mapErr(err)
}

// DeliveryReport is a provider's report on one sent message.
type DeliveryReport struct {
	ProviderRef, Status, FailureReason string
	Final                              bool
}

// ErrUnknownDelivery reports a delivery report for no attempt Sauti made.
var ErrUnknownDelivery = errors.New("opstore: delivery report for an unknown message")

// RecordDeliveryReport stores a delivery report on the attempt that sent
// the message (op_intake: the provider's callback). A final report is never
// overwritten.
func (s *Store) RecordDeliveryReport(ctx context.Context, r DeliveryReport) error {
	known, err := s.q.RecordDeliveryReport(ctx, opdb.RecordDeliveryReportParams{ProviderRef: r.ProviderRef, Status: r.Status,
		Failure: strPtr(r.FailureReason), Final: r.Final})
	if err != nil {
		return mapErr(err)
	}
	if !known {
		return ErrUnknownDelivery
	}
	return nil
}

// ClaimAvailabilityReminders claims up to limit availability periods that
// end within `within` and were not reminded (op_dispatch); each period once.
func (s *Store) ClaimAvailabilityReminders(ctx context.Context, within time.Duration, limit int) ([]dispatch.Reminder, error) {
	rows, err := s.q.ClaimAvailabilityReminders(ctx, opdb.ClaimAvailabilityRemindersParams{
		Within: pgtype.Interval{Microseconds: within.Microseconds(), Valid: true}, MaxRows: int32(min(limit, 1000))}) //nolint:gosec // bounded above
	if err != nil {
		return nil, fmt.Errorf("opstore: reminders: %w", err)
	}
	out := make([]dispatch.Reminder, 0, len(rows))
	for _, r := range rows {
		out = append(out, dispatch.Reminder{ResponderID: uuid.UUID(r.ResponderID.Bytes), Until: r.Until.Time.UTC()})
	}
	return out, nil
}

// ResponderPhone returns a responder's sealed number bound to their row
// (op_dispatch: the SMS and voice channels).
func (s *Store) ResponderPhone(ctx context.Context, responderID uuid.UUID) (incident.SealedContact, error) {
	r, err := s.q.ResponderPhone(ctx, pgID(responderID))
	if err != nil {
		return incident.SealedContact{}, fmt.Errorf("opstore: responder phone: %w", notFound(err))
	}
	return incident.SealedContact{Sealed: crypto.Sealed{KeyID: r.PhoneKeyID, Ciphertext: r.PhoneCt},
		AAD: crypto.AAD{Store: "operational", Table: "responders", Column: "phone", RowID: responderID.String()}}, nil
}

// MessageFacts returns the coded facts a message may show about an incident.
func (s *Store) MessageFacts(ctx context.Context, incidentID uuid.UUID) (dispatch.MessageFacts, error) {
	r, err := s.q.MessageFacts(ctx, pgID(incidentID))
	if err != nil {
		return dispatch.MessageFacts{}, fmt.Errorf("opstore: message facts: %w", notFound(err))
	}
	return dispatch.MessageFacts{Reference: incident.Reference(r.Reference), Severity: incident.Severity(deref(r.Severity)),
		Category: deref(r.Category), Ward: deref(r.Ward), Landmark: deref(r.Landmark), Language: deref(r.Language)}, nil
}

// NoticeFacility returns the facility a caller notice names ("" if none).
func (s *Store) NoticeFacility(ctx context.Context, incidentID uuid.UUID, kind string) (string, error) {
	f, err := s.q.NoticeFacility(ctx, opdb.NoticeFacilityParams{IncidentID: pgID(incidentID), Kind: kind})
	if err != nil {
		return "", fmt.Errorf("opstore: notice facility: %w", notFound(err))
	}
	return f, nil
}

// DueVoiceAlerts lists alerts due a voice call (ivr-controller, op_ivr).
func (s *Store) DueVoiceAlerts(ctx context.Context, after time.Duration, limit int) ([]dispatch.VoiceAlert, error) {
	rows, err := s.q.DueVoiceAlerts(ctx, opdb.DueVoiceAlertsParams{After: pgtype.Interval{Microseconds: after.Microseconds(), Valid: true},
		MaxRows: int32(min(limit, 1000))}) //nolint:gosec // bounded above
	if err != nil {
		return nil, fmt.Errorf("opstore: due voice alerts: %w", err)
	}
	out := make([]dispatch.VoiceAlert, 0, len(rows))
	for _, r := range rows {
		out = append(out, dispatch.VoiceAlert{AlertID: uuid.UUID(r.ID.Bytes), ResponderID: uuid.UUID(r.ResponderID.Bytes)})
	}
	return out, nil
}

// AlertForAttempt returns the alert a voice attempt is about and its
// responder (ivr-controller: an answered alert call).
func (s *Store) AlertForAttempt(ctx context.Context, attempt uuid.UUID) (dispatch.ResponderAlert, uuid.UUID, error) {
	r, err := s.q.AlertForAttempt(ctx, pgID(attempt))
	if err != nil {
		return dispatch.ResponderAlert{}, uuid.UUID{}, fmt.Errorf("opstore: alert for attempt: %w", notFound(err))
	}
	return dispatch.ResponderAlert{AlertID: uuid.UUID(r.ID.Bytes), IncidentID: uuid.UUID(r.IncidentID.Bytes), Role: responder.Role(r.Role),
		Outcome: r.Outcome, Facts: dispatch.MessageFacts{Reference: incident.Reference(r.Reference), Severity: incident.Severity(deref(r.Severity)),
			Category: deref(r.Category), Ward: deref(r.Ward), Landmark: deref(r.Landmark), Language: deref(r.Language)}}, uuid.UUID(r.ResponderID.Bytes), nil
}

// BeginForwardAttempt claims a forwarded facility's SMS (op_dispatch) or
// call (op_ivr) once; a call only while the forward awaits acknowledgement.
func (s *Store) BeginForwardAttempt(ctx context.Context, incidentID uuid.UUID, facility, channel string) (uuid.UUID, bool, error) {
	r, err := s.q.BeginForwardAttempt(ctx, opdb.BeginForwardAttemptParams{IncidentID: pgID(incidentID), FacilityID: facility, Channel: channel})
	if err != nil {
		return uuid.UUID{}, false, mapErr(err)
	}
	return uuid.UUID(r.AttemptID.Bytes), r.Fresh, nil
}

// FinishForwardAttempt settles a claimed forward attempt.
func (s *Store) FinishForwardAttempt(ctx context.Context, attempt uuid.UUID, status, providerRef string) (bool, error) {
	ok, err := s.q.FinishForwardAttempt(ctx, opdb.FinishForwardAttemptParams{AttemptID: pgID(attempt), Status: status, ProviderRef: strPtr(providerRef)})
	return ok, mapErr(err)
}

// DueForwardCalls lists forwarded facilities to call (ivr-controller).
func (s *Store) DueForwardCalls(ctx context.Context, limit int) ([]dispatch.ForwardCall, error) {
	rows, err := s.q.DueForwardCalls(ctx, int32(min(limit, 1000))) //nolint:gosec // bounded above
	if err != nil {
		return nil, fmt.Errorf("opstore: due forward calls: %w", err)
	}
	out := make([]dispatch.ForwardCall, 0, len(rows))
	for _, r := range rows {
		out = append(out, dispatch.ForwardCall{IncidentID: uuid.UUID(r.IncidentID.Bytes), FacilityID: r.FacilityID})
	}
	return out, nil
}

// ClaimCoordinatorCalls claims the coordinator calls due now (ivr-controller):
// each incident's unacknowledged escalation reasons, rung again after retry.
func (s *Store) ClaimCoordinatorCalls(ctx context.Context, retry time.Duration, limit int) ([]dispatch.CoordinatorCall, error) {
	rows, err := s.q.ClaimCoordinatorCalls(ctx, opdb.ClaimCoordinatorCallsParams{Retry: pgtype.Interval{Microseconds: retry.Microseconds(), Valid: true},
		MaxRows: int32(min(limit, 1000))}) //nolint:gosec // bounded above
	if err != nil {
		return nil, fmt.Errorf("opstore: coordinator calls: %w", err)
	}
	out := make([]dispatch.CoordinatorCall, 0, len(rows))
	for _, r := range rows {
		out = append(out, dispatch.CoordinatorCall{IncidentID: uuid.UUID(r.IncidentID.Bytes), Seq: r.Seq, Reasons: r.Reasons, Roles: r.Roles,
			Reference: incident.Reference(r.Reference), Calls: int(r.Calls), Severity: r.Severity})
	}
	return out, nil
}

// OldestUnacknowledgedEscalation reports the dead-man signal (intake-gw,
// op_intake): the age in seconds of the oldest escalation no coordinator has
// acknowledged on an open incident, and how many incidents hold one.
func (s *Store) OldestUnacknowledgedEscalation(ctx context.Context) (float64, int, error) {
	r, err := s.q.OldestUnacknowledgedEscalation(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("opstore: oldest unacknowledged escalation: %w", err)
	}
	return r.Seconds, int(r.Incidents), nil
}

// AcknowledgeCoordinatorCall records a coordinator's acknowledgement of the
// incident's escalations up to seq.
func (s *Store) AcknowledgeCoordinatorCall(ctx context.Context, incidentID uuid.UUID, seq int64) error {
	return mapErr(s.q.AcknowledgeCoordinatorCall(ctx, opdb.AcknowledgeCoordinatorCallParams{IncidentID: pgID(incidentID), Seq: seq}))
}
