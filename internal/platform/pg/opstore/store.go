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
	"log/slog"
	"strings"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"sautihealth.com/sauti/internal/audit"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore/opdb"
)

// Store implements incident.Store and audit.Outbox.
type Store struct {
	// pool bounds every call the adapter makes outside a transaction, and
	// the start of every transaction, to opTimeout (pg.BoundedDB).
	pool *pg.BoundedDB
	q    *opdb.Queries
}

// New returns a Store over pool. Every call it makes has its own deadline —
// the wait for a connection plus one statement (opTimeout) — even when the
// caller's context has none (CodeRabbit PR #3).
func New(pool *pgxpool.Pool) *Store {
	db := pg.Bounded(pool, opTimeout)
	return &Store{pool: db, q: opdb.New(db)}
}

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
	case pe.Code == "23514" && pe.ConstraintName == "incident_events_flag_is_string":
		// 0027: a flag event whose flag is not a JSON string.
		return fmt.Errorf("%w: flag event needs a string flag", incident.ErrIllegalTransition)
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
	if r.Duplicate {
		return s.original(ctx, p.IncidentID, r.Seq)
	}
	return incident.Appended{
		Event: incident.Event{
			ID: uuid.UUID(r.EventID.Bytes), IncidentID: p.IncidentID, Seq: r.Seq, Type: p.Type,
			RecordedAt: r.RecordedAt.Time.UTC(), ActorKind: p.ActorKind, StaffID: p.StaffID,
			Payload: payload, IdemKey: p.IdemKey, LogicVersion: p.LogicVersion,
		},
		NewState: incident.State(r.NewState),
	}, nil
}

// original answers a reused idempotency key: the event stored at seq (not
// the retry's fields, which may differ) and the state right after it (not
// the incident's current state, which later events may have moved on).
// Events are append-only, so reading them after the append is consistent.
func (s *Store) original(ctx context.Context, id uuid.UUID, seq int64) (incident.Appended, error) {
	events, err := s.Events(ctx, id)
	if err != nil {
		return incident.Appended{}, err
	}
	if seq < 1 || seq > int64(len(events)) {
		return incident.Appended{}, fmt.Errorf("opstore: duplicate at seq %d outside a history of %d events", seq, len(events))
	}
	p, err := incident.Fold(events[:seq])
	if err != nil {
		return incident.Appended{}, fmt.Errorf("opstore: fold history through seq %d: %w", seq, err)
	}
	return incident.Appended{Event: events[seq-1], NewState: p.State, Duplicate: true}, nil
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

// partitionTimeout bounds one partition-maintenance call, pool wait included.
const partitionTimeout = 30 * time.Second

// opTimeout bounds every other call to the pool: the wait for a connection
// plus one statement, which the server stops at 5 s (statement_timeout).
var opTimeout = 10 * time.Second

// EnsureEventPartitions creates every missing monthly partition of
// op.incident_events from the current month to monthsAhead months ahead and
// returns how many it created. Events of a month without a partition land in
// the default partition, which then blocks creating that month's partition,
// so the relay runs this well ahead of time (runbook:
// docs/runbooks/event-partition-maintenance.md).
func (s *Store) EnsureEventPartitions(ctx context.Context, monthsAhead int) (int, error) {
	// Partition maintenance has its own, longer bound (partitionTimeout).
	q := opdb.New(s.pool.WithTimeout(partitionTimeout))
	n, err := q.EnsureEventPartitions(ctx, int32(max(0, min(monthsAhead, 120)))) //nolint:gosec // clamped
	if err != nil {
		return 0, fmt.Errorf("opstore: ensure event partitions: %w", err)
	}
	return int(n), nil
}

// MaintainEventPartitions keeps monthsAhead monthly event partitions ahead
// of the clock: it calls EnsureEventPartitions at once and then every
// interval until ctx ends, logging how many partitions each run created. A
// failed run is logged and retried at the next interval; writes never wait
// on it (they fall back to the default partition). The outbox relay runs it
// while it holds leadership, so one process does it at a time.
func (s *Store) MaintainEventPartitions(ctx context.Context, monthsAhead int, every time.Duration, log *slog.Logger) error {
	if every <= 0 {
		return errors.New("opstore: partition maintenance interval must be positive")
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		n, err := s.EnsureEventPartitions(ctx, monthsAhead)
		switch {
		case err == nil:
			log.Info("event partitions ensured", "months_ahead", monthsAhead, "created", n)
		case ctx.Err() == nil:
			log.Warn("event partition maintenance failed; retrying at the next interval", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Claim implements audit.Outbox: it opens a transaction and locks up to
// limit pending rows; the batch holds the transaction until Complete/Abort.
// Only Begin is bounded (opTimeout): the held transaction is not, and the
// server ends it if it idles (idle_in_transaction_session_timeout).
func (s *Store) Claim(ctx context.Context, topic string, limit int) (audit.OutboxBatch, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	q := opdb.New(tx)
	rows, err := q.ClaimOutbox(ctx, opdb.ClaimOutboxParams{Topic: topic, MaxRows: int32(max(1, min(limit, 10000)))}) //nolint:gosec // clamped
	if err != nil {
		rollback(ctx, tx)
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
			rollback(ctx, b.tx)
			return err
		}
	}
	return b.tx.Commit(ctx)
}

// Abort releases the locks without marking anything delivered.
func (b *batch) Abort(ctx context.Context) { rollback(ctx, b.tx) }

// cleanupTimeout bounds a rollback on a cleanup path.
const cleanupTimeout = 5 * time.Second

// rollback ends tx on a fresh, short-lived context: it still runs after ctx
// is cancelled, and it cannot block for longer than cleanupTimeout if the
// connection stalls.
func rollback(ctx context.Context, tx pgx.Tx) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	_ = tx.Rollback(rctx)
}
