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
