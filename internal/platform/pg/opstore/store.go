// Package opstore is the PostgreSQL adapter of the operational store. It
// implements incident.Store on top of the database-enforced single write
// path (op.open_incident, op.append_incident_event) and audit.Outbox on top
// of op.claim_outbox / op.mark_outbox_delivered.
package opstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"sautihealth.com/sauti/internal/audit"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg/opstore/opdb"
)

// Store implements incident.Store and audit.Outbox.
type Store struct {
	pool *pgxpool.Pool
	q    *opdb.Queries
}

// New returns a Store over pool.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool, q: opdb.New(pool)} }

func pgID(u uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte(u), Valid: true} }

func pgIDPtr(u *uuid.UUID) pgtype.UUID {
	if u == nil {
		return pgtype.UUID{}
	}
	return pgID(*u)
}

func idPtr(u pgtype.UUID) *uuid.UUID {
	if !u.Valid {
		return nil
	}
	v := uuid.UUID(u.Bytes)
	return &v
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func tsPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

// mapErr translates the write path's SQLSTATEs into incident errors.
func mapErr(err error) error {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return err
	}
	switch {
	case pe.Code == "SA404":
		return fmt.Errorf("%w: %s", incident.ErrNotFound, pe.Message)
	case pe.Code == "SA409":
		return fmt.Errorf("%w: %s", incident.ErrVersionConflict, pe.Message)
	case pe.Code == "SA422" && strings.HasPrefix(pe.Message, "severity lowered"):
		return fmt.Errorf("%w: %s", incident.ErrSeverityLowered, pe.Message)
	case pe.Code == "SA422":
		return fmt.Errorf("%w: %s", incident.ErrIllegalTransition, pe.Message)
	case pe.Code == "23505" && pe.ConstraintName == "incidents_reference_key":
		return incident.ErrReferenceTaken
	}
	return err
}

// OpenIncident implements incident.Store.
func (s *Store) OpenIncident(ctx context.Context, p incident.OpenParams) (incident.Opened, error) {
	params := opdb.OpenIncidentParams{
		Reference:         string(p.Reference),
		Channel:           string(p.Channel),
		SessionID:         pgID(p.SessionID),
		ProviderSessionID: p.ProviderSessionID,
		NetworkCode:       strPtr(p.NetworkCode),
		IdemKey:           p.IdemKey,
	}
	if p.Phone.Ciphertext != nil {
		params.PhoneCt, params.PhoneKeyID = p.Phone.Ciphertext, strPtr(p.Phone.KeyID)
	}
	if p.PhoneMAC.Sum != nil {
		params.PhoneMac, params.PhoneMacKeyID = p.PhoneMAC.Sum, strPtr(p.PhoneMAC.KeyID)
	}
	r, err := s.q.OpenIncident(ctx, params)
	if err != nil {
		return incident.Opened{}, mapErr(err)
	}
	ref, err := incident.ParseReference(r.Reference)
	if err != nil {
		return incident.Opened{}, err
	}
	return incident.Opened{
		IncidentID: uuid.UUID(r.IncidentID.Bytes),
		Reference:  ref,
		EventID:    uuid.UUID(r.EventID.Bytes),
		T1:         r.T1.Time.UTC(),
		Created:    r.Created,
	}, nil
}

// Append implements incident.Store.
func (s *Store) Append(ctx context.Context, p incident.AppendParams) (incident.Appended, error) {
	payload := p.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	r, err := s.q.AppendEvent(ctx, opdb.AppendEventParams{
		IncidentID:      pgID(p.IncidentID),
		ExpectedVersion: p.ExpectedVersion,
		Type:            string(p.Type),
		Payload:         payload,
		ActorKind:       p.ActorKind,
		StaffID:         pgIDPtr(p.StaffID),
		IdemKey:         p.IdemKey,
		LogicVersion:    strPtr(p.LogicVersion),
	})
	if err != nil {
		return incident.Appended{}, mapErr(err)
	}
	return incident.Appended{
		Event: incident.Event{
			ID: uuid.UUID(r.EventID.Bytes), IncidentID: p.IncidentID, Seq: r.Seq, Type: p.Type,
			RecordedAt: r.RecordedAt.Time.UTC(), ActorKind: p.ActorKind, StaffID: p.StaffID,
			Payload: payload, IdemKey: p.IdemKey, LogicVersion: p.LogicVersion,
		},
		NewState:  incident.State(r.NewState),
		Duplicate: r.Duplicate,
	}, nil
}

// Events implements incident.Store: the incident's full history in order.
func (s *Store) Events(ctx context.Context, id uuid.UUID) ([]incident.Event, error) {
	rows, err := s.q.ListEvents(ctx, pgID(id))
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]incident.Event, 0, len(rows))
	for _, r := range rows {
		out = append(out, incident.Event{
			ID: uuid.UUID(r.ID.Bytes), IncidentID: uuid.UUID(r.IncidentID.Bytes), Seq: r.Seq,
			Type: incident.EventType(r.Type), RecordedAt: r.RecordedAt.Time.UTC(), ActorKind: r.ActorKind,
			StaffID: idPtr(r.StaffID), Payload: json.RawMessage(r.Payload), IdemKey: r.IdemKey,
			LogicVersion: deref(r.LogicVersion),
		})
	}
	return out, nil
}

// Projection reads the database projection of one incident.
func (s *Store) Projection(ctx context.Context, id uuid.UUID) (incident.Projection, error) {
	r, err := s.q.GetIncident(ctx, pgID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return incident.Projection{}, incident.ErrNotFound
	}
	if err != nil {
		return incident.Projection{}, mapErr(err)
	}
	ref, err := incident.ParseReference(r.Reference)
	if err != nil {
		return incident.Projection{}, err
	}
	p := incident.Projection{
		ID: uuid.UUID(r.ID.Bytes), Reference: ref, State: incident.State(r.State), Version: r.Version,
		Channel: r.Channel, Severity: incident.Severity(deref(r.Severity)), LogicVersion: deref(r.LogicVersion),
		Escalated: r.Escalated, Flags: r.Flags, MergedInto: idPtr(r.MergedInto),
		Language: deref(r.Language), Danger: deref(r.Danger), Category: deref(r.Category),
		AgeBand: deref(r.AgeBand), SpecificDanger: deref(r.SpecificDanger), Ward: deref(r.Ward),
		Landmark: deref(r.Landmark), LastScreen: int(r.LastScreen), LocationQuality: deref(r.LocationQuality),
	}
	if r.ZoneCol != nil && r.ZoneRow != nil {
		p.Zone = &incident.Zone{Col: int(*r.ZoneCol), Row: int(*r.ZoneRow)}
	}
	for i, t := range []pgtype.Timestamptz{r.T1, r.T2, r.T3, r.T4, r.T5, r.T6, r.T7} {
		p.T[i+1] = tsPtr(t)
	}
	if len(p.Flags) == 0 {
		p.Flags = nil
	}
	return p, nil
}

// ListIncidentIDs returns every incident ID (verification).
func (s *Store) ListIncidentIDs(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := s.q.ListIncidentIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		out[i] = uuid.UUID(r.Bytes)
	}
	return out, nil
}

// DefaultPartitionRows reports events that fell into the default partition.
func (s *Store) DefaultPartitionRows(ctx context.Context) (int64, error) {
	return s.q.DefaultPartitionRows(ctx)
}

// Claim implements audit.Outbox: it opens a transaction and locks up to
// limit pending rows; the batch holds the transaction until Complete/Abort.
func (s *Store) Claim(ctx context.Context, topic string, limit int) (audit.OutboxBatch, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	q := opdb.New(tx)
	rows, err := q.ClaimOutbox(ctx, opdb.ClaimOutboxParams{Topic: topic, MaxRows: int32(max(1, min(limit, 10000)))}) //nolint:gosec // clamped
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	b := &batch{tx: tx, q: q, rows: make([]audit.OutboxRow, len(rows))}
	for i, r := range rows {
		b.rows[i] = audit.OutboxRow{ID: uuid.UUID(r.ID.Bytes), Payload: json.RawMessage(r.Payload)}
	}
	return b, nil
}

type batch struct {
	tx   pgx.Tx
	q    *opdb.Queries
	rows []audit.OutboxRow
}

func (b *batch) Rows() []audit.OutboxRow { return b.rows }

// Complete marks delivered rows and commits, releasing the locks.
func (b *batch) Complete(ctx context.Context, delivered []uuid.UUID) error {
	if len(delivered) > 0 {
		ids := make([]pgtype.UUID, len(delivered))
		for i, d := range delivered {
			ids[i] = pgID(d)
		}
		if _, err := b.q.MarkOutboxDelivered(ctx, ids); err != nil {
			_ = b.tx.Rollback(ctx)
			return err
		}
	}
	return b.tx.Commit(ctx)
}

// Abort releases the locks without marking anything delivered.
func (b *batch) Abort(ctx context.Context) { _ = b.tx.Rollback(context.WithoutCancel(ctx)) }
