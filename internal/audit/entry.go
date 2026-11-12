// Package audit is the tamper-evident audit ledger (Build Prompt rule 3.8,
// ISP §5.5, spec §6.3, ADR-0005). Entries record what happened — event
// type, incident ID, staff ID, logic version and the SHA-256 of the event
// payload — and never a caller identifier. Entries form a SHA-256 hash chain;
// signed checkpoints anchor the chain outside the database.
package audit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
	"uuid"
)

// Entry is one ledger row.
type Entry struct {
	Seq           int64
	EntryID       uuid.UUID
	OccurredAt    time.Time
	EventType     string
	IncidentID    *uuid.UUID
	StaffID       *uuid.UUID
	LogicVersion  string
	PayloadSHA256 [32]byte
	PrevHash      [32]byte
	Hash          [32]byte
}

// AppendRequest is what a producer submits; the ledger assigns Seq and hashes.
type AppendRequest struct {
	EntryID       uuid.UUID
	OccurredAt    time.Time
	EventType     string
	IncidentID    *uuid.UUID
	StaffID       *uuid.UUID
	LogicVersion  string
	PayloadSHA256 [32]byte
}

// Checkpoint is a signed statement of the chain head at Seq.
type Checkpoint struct {
	Origin    string
	Seq       int64
	Head      [32]byte
	SignedAt  time.Time
	KeyID     string
	Signature []byte
}

// CheckpointFormat is the first line of every checkpoint body.
const CheckpointFormat = "sauti-audit-checkpoint/v1"

// Body is the exact byte string that is signed, modelled on the C2SP
// tlog-checkpoint note body: format, origin, sequence, base64 head, UTC time.
func (c Checkpoint) Body() []byte {
	return []byte(CheckpointFormat + "\n" +
		c.Origin + "\n" +
		strconv.FormatInt(c.Seq, 10) + "\n" +
		base64.StdEncoding.EncodeToString(c.Head[:]) + "\n" +
		c.SignedAt.UTC().Format(time.RFC3339Nano) + "\n")
}

// Ledger is the append side of the audit store.
type Ledger interface {
	Append(ctx context.Context, r AppendRequest) (Entry, error)
	Head(ctx context.Context) (int64, [32]byte, error)
	// HashAt returns the chain hash stored at seq, and false if there is none.
	HashAt(ctx context.Context, seq int64) ([32]byte, bool, error)
	SaveCheckpoint(ctx context.Context, c Checkpoint) error
}

// LedgerReader is the read side, used by verification.
type LedgerReader interface {
	Range(ctx context.Context, fromSeq int64, fn func(Entry) error) error
	Checkpoints(ctx context.Context) ([]Checkpoint, error)
}

// OutboxRow is one pending row of the operational store's outbox.
type OutboxRow struct {
	ID      uuid.UUID
	Payload json.RawMessage
}

// OutboxBatch is a set of claimed outbox rows, locked until Complete or Abort.
type OutboxBatch interface {
	Rows() []OutboxRow
	Complete(ctx context.Context, delivered []uuid.UUID) error
	Abort(ctx context.Context)
}

// Outbox is the operational store's transactional outbox (spec §6.3).
type Outbox interface {
	Claim(ctx context.Context, topic string, limit int) (OutboxBatch, error)
}

// String renders a short, PII-free description for logs.
func (e Entry) String() string {
	return fmt.Sprintf("audit.Entry{seq=%d type=%s}", e.Seq, e.EventType)
}
