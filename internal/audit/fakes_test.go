package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
	"uuid"
)

// fakeOutbox holds pending rows; Complete removes delivered ones.
type fakeOutbox struct {
	mu        sync.Mutex
	pending   []OutboxRow
	completes int
	aborts    int
}

type fakeBatch struct {
	o    *fakeOutbox
	rows []OutboxRow
}

func (o *fakeOutbox) Claim(_ context.Context, _ string, limit int) (OutboxBatch, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := min(limit, len(o.pending))
	return &fakeBatch{o: o, rows: append([]OutboxRow(nil), o.pending[:n]...)}, nil
}

func (b *fakeBatch) Rows() []OutboxRow { return b.rows }
func (b *fakeBatch) Complete(_ context.Context, ids []uuid.UUID) error {
	b.o.mu.Lock()
	defer b.o.mu.Unlock()
	b.o.completes++
	done := map[uuid.UUID]bool{}
	for _, id := range ids {
		done[id] = true
	}
	var keep []OutboxRow
	for _, r := range b.o.pending {
		if !done[r.ID] {
			keep = append(keep, r)
		}
	}
	b.o.pending = keep
	return nil
}
func (b *fakeBatch) Abort(context.Context) {
	b.o.mu.Lock()
	b.o.aborts++
	b.o.mu.Unlock()
}

// fakeLedger dedupes on EntryID and computes the real chain.
type fakeLedger struct {
	mu          sync.Mutex
	entries     []Entry
	byID        map[uuid.UUID]Entry
	failNext    int
	checkpoints []Checkpoint
}

func newFakeLedger() *fakeLedger { return &fakeLedger{byID: map[uuid.UUID]Entry{}} }

var errLedgerDown = errors.New("ledger down")

func (l *fakeLedger) Append(_ context.Context, r AppendRequest) (Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failNext > 0 {
		l.failNext--
		return Entry{}, errLedgerDown
	}
	if e, ok := l.byID[r.EntryID]; ok {
		return e, nil
	}
	var prev [32]byte
	if n := len(l.entries); n > 0 {
		prev = l.entries[n-1].Hash
	}
	e := Entry{Seq: int64(len(l.entries) + 1), EntryID: r.EntryID, OccurredAt: r.OccurredAt.Truncate(time.Microsecond),
		EventType: r.EventType, IncidentID: r.IncidentID, StaffID: r.StaffID, LogicVersion: r.LogicVersion,
		PayloadSHA256: r.PayloadSHA256, PrevHash: prev}
	e.Hash = ChainHash(prev, e)
	l.entries = append(l.entries, e)
	l.byID[e.EntryID] = e
	return e, nil
}

func (l *fakeLedger) Head(context.Context) (int64, [32]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.entries) == 0 {
		return 0, [32]byte{}, nil
	}
	e := l.entries[len(l.entries)-1]
	return e.Seq, e.Hash, nil
}

func (l *fakeLedger) HashAt(_ context.Context, seq int64) ([32]byte, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if seq < 1 || seq > int64(len(l.entries)) {
		return [32]byte{}, false, nil
	}
	return l.entries[seq-1].Hash, true, nil
}

func (l *fakeLedger) SaveCheckpoint(_ context.Context, c Checkpoint) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.checkpoints = append(l.checkpoints, c)
	return nil
}

func (l *fakeLedger) Range(_ context.Context, from int64, fn func(Entry) error) error {
	for _, e := range l.entries {
		if e.Seq >= from {
			if err := fn(e); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l *fakeLedger) Checkpoints(context.Context) ([]Checkpoint, error) { return l.checkpoints, nil }

// outboxRow builds a row exactly as op.enqueue_audit would.
func outboxRow(i int) OutboxRow {
	id := uuid.NewV7()
	inc := uuid.NewV7()
	payload := map[string]any{
		"entry_id":       id.String(),
		"event_id":       uuid.NewV7().String(),
		"occurred_at":    time.Date(2026, 11, 12, 10, 0, i, 123456000, time.UTC).Format("2006-01-02T15:04:05.999999-07:00"),
		"event_type":     "screen.answered",
		"incident_id":    inc.String(),
		"staff_id":       nil,
		"logic_version":  nil,
		"payload_sha256": fmt.Sprintf("%064x", i),
	}
	b, _ := json.Marshal(payload)
	return OutboxRow{ID: id, Payload: b}
}
