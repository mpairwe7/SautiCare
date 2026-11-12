// Package auditstore is the PostgreSQL adapter for the audit ledger
// (internal/audit). It talks only to the audit store, with the
// audit_appender role (append, head, checkpoint) or audit_reader (verify).
package auditstore

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"sautihealth.com/sauti/internal/audit"
	"sautihealth.com/sauti/internal/platform/pg/auditstore/auditdb"
)

const pageSize = 1000

// appendTimeout bounds one Append, pool acquisition included: the statement
// timeout does not cover waiting for a connection.
var appendTimeout = 10 * time.Second

// Store implements audit.Ledger and audit.LedgerReader.
type Store struct {
	q *auditdb.Queries
}

// New returns a Store over pool.
func New(pool *pgxpool.Pool) *Store { return &Store{q: auditdb.New(pool)} }

func pgUUID(u *uuid.UUID) pgtype.UUID {
	if u == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: [16]byte(*u), Valid: true}
}

func fromPGUUID(u pgtype.UUID) *uuid.UUID {
	if !u.Valid {
		return nil
	}
	v := uuid.UUID(u.Bytes)
	return &v
}

func hash32(b []byte, what string) ([32]byte, error) {
	var out [32]byte
	if len(b) != 32 {
		return out, fmt.Errorf("auditstore: %s has %d bytes, want 32", what, len(b))
	}
	copy(out[:], b)
	return out, nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

type row struct {
	Seq           int64
	EntryID       pgtype.UUID
	OccurredAt    pgtype.Timestamptz
	EventType     string
	IncidentID    pgtype.UUID
	StaffID       pgtype.UUID
	LogicVersion  *string
	PayloadSha256 []byte
	PrevHash      []byte
	Hash          []byte
}

func toEntry(r row) (audit.Entry, error) {
	e := audit.Entry{
		Seq:        r.Seq,
		EntryID:    uuid.UUID(r.EntryID.Bytes),
		OccurredAt: r.OccurredAt.Time.UTC(),
		EventType:  r.EventType,
		IncidentID: fromPGUUID(r.IncidentID),
		StaffID:    fromPGUUID(r.StaffID),
	}
	if r.LogicVersion != nil {
		e.LogicVersion = *r.LogicVersion
	}
	var err error
	if e.PayloadSHA256, err = hash32(r.PayloadSha256, "payload_sha256"); err != nil {
		return e, err
	}
	if e.PrevHash, err = hash32(r.PrevHash, "prev_hash"); err != nil {
		return e, err
	}
	if e.Hash, err = hash32(r.Hash, "hash"); err != nil {
		return e, err
	}
	return e, nil
}

// Append implements audit.Ledger. It is idempotent on EntryID. Each call
// has its own deadline (appendTimeout), so one stalled append cannot stop
// relay progress even when the caller's context has none.
func (s *Store) Append(ctx context.Context, r audit.AppendRequest) (audit.Entry, error) {
	ctx, cancel := context.WithTimeout(ctx, appendTimeout)
	defer cancel()
	id := r.EntryID
	got, err := s.q.AppendEntry(ctx, auditdb.AppendEntryParams{
		EntryID:       pgUUID(&id),
		OccurredAt:    pgtype.Timestamptz{Time: r.OccurredAt, Valid: true},
		EventType:     r.EventType,
		IncidentID:    pgUUID(r.IncidentID),
		StaffID:       pgUUID(r.StaffID),
		LogicVersion:  strPtr(r.LogicVersion),
		PayloadSha256: r.PayloadSHA256[:],
	})
	if err != nil {
		return audit.Entry{}, fmt.Errorf("auditstore: append: %w", err)
	}
	return toEntry(row(got))
}

// Head implements audit.Ledger. An empty ledger has head (0, zero hash).
func (s *Store) Head(ctx context.Context) (int64, [32]byte, error) {
	rows, err := s.q.Head(ctx)
	if err != nil {
		return 0, [32]byte{}, fmt.Errorf("auditstore: head: %w", err)
	}
	if len(rows) == 0 {
		return 0, [32]byte{}, nil
	}
	h, err := hash32(rows[0].Hash, "head")
	return rows[0].Seq, h, err
}

// HashAt implements audit.Ledger: the chain hash at seq, false if absent.
func (s *Store) HashAt(ctx context.Context, seq int64) ([32]byte, bool, error) {
	b, err := s.q.HashAt(ctx, seq)
	if err != nil {
		return [32]byte{}, false, fmt.Errorf("auditstore: hash at %d: %w", seq, err)
	}
	if b == nil {
		return [32]byte{}, false, nil
	}
	h, err := hash32(b, "hash_at")
	return h, err == nil, err
}

// SaveCheckpoint implements audit.Ledger; the signed body is stored verbatim.
func (s *Store) SaveCheckpoint(ctx context.Context, c audit.Checkpoint) error {
	if c.Signature == nil {
		return errors.New("auditstore: refusing to store an unsigned checkpoint")
	}
	_, err := s.q.SaveCheckpoint(ctx, auditdb.SaveCheckpointParams{
		Origin:    c.Origin,
		Seq:       c.Seq,
		Head:      c.Head[:],
		SignedAt:  pgtype.Timestamptz{Time: c.SignedAt, Valid: true},
		KeyID:     c.KeyID,
		Body:      c.Body(),
		Signature: c.Signature,
	})
	if err != nil {
		return fmt.Errorf("auditstore: save checkpoint: %w", err)
	}
	return nil
}

// Range implements audit.LedgerReader, calling fn for every entry with
// seq >= fromSeq in order.
func (s *Store) Range(ctx context.Context, fromSeq int64, fn func(audit.Entry) error) error {
	next := fromSeq
	for {
		rows, err := s.q.RangeEntries(ctx, auditdb.RangeEntriesParams{FromSeq: next, PageSize: pageSize})
		if err != nil {
			return fmt.Errorf("auditstore: range: %w", err)
		}
		for _, r := range rows {
			e, err := toEntry(row(r))
			if err != nil {
				return err
			}
			if err := fn(e); err != nil {
				return err
			}
			next = e.Seq + 1
		}
		if len(rows) < pageSize {
			return nil
		}
	}
}

// Checkpoints implements audit.LedgerReader.
func (s *Store) Checkpoints(ctx context.Context) ([]audit.Checkpoint, error) {
	rows, err := s.q.ListCheckpoints(ctx)
	if err != nil {
		return nil, fmt.Errorf("auditstore: checkpoints: %w", err)
	}
	out := make([]audit.Checkpoint, 0, len(rows))
	for _, r := range rows {
		h, err := hash32(r.Head, "checkpoint head")
		if err != nil {
			return nil, err
		}
		out = append(out, audit.Checkpoint{
			Origin: r.Origin, Seq: r.Seq, Head: h, SignedAt: r.SignedAt.Time.UTC(),
			KeyID: r.KeyID, Signature: r.Signature,
		})
	}
	return out, nil
}
