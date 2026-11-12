package audit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
	"uuid"
)

func newRelay(o Outbox, l Ledger) *Relay {
	return NewRelay(o, l, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, 100)
}

func TestRelayDeliversAllPending(t *testing.T) {
	o := &fakeOutbox{}
	for i := range 5 {
		o.pending = append(o.pending, outboxRow(i))
	}
	want := append([]OutboxRow(nil), o.pending...)
	l := newFakeLedger()
	n, err := newRelay(o, l).RunOnce(context.Background())
	if err != nil || n != 5 {
		t.Fatalf("RunOnce = %d, %v", n, err)
	}
	if len(o.pending) != 0 {
		t.Fatalf("%d rows left pending", len(o.pending))
	}
	for i, e := range l.entries {
		if e.EntryID != want[i].ID {
			t.Fatalf("entry %d is %v, want outbox order %v", i, e.EntryID, want[i].ID)
		}
	}
}

func TestRelayEmptyOutboxIsNoop(t *testing.T) {
	n, err := newRelay(&fakeOutbox{}, newFakeLedger()).RunOnce(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("RunOnce on empty outbox = %d, %v", n, err)
	}
}

// A crash between the ledger append and the outbox commit redelivers rows;
// the ledger must not grow twice.
func TestRelayRedeliveryIsIdempotent(t *testing.T) {
	o := &fakeOutbox{pending: []OutboxRow{outboxRow(1), outboxRow(2)}}
	l := newFakeLedger()
	req, err := ParseOutboxPayload(o.pending[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append(context.Background(), req); err != nil { // already delivered once
		t.Fatal(err)
	}
	if _, err := newRelay(o, l).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(l.entries) != 2 {
		t.Fatalf("ledger has %d entries, want 2", len(l.entries))
	}
}

// An unexpected key could be personal data; the relay stops instead of
// skipping or copying it (hard constraint 8).
func TestRelayAbortsOnPoisonRow(t *testing.T) {
	bad := outboxRow(2)
	bad.Payload = []byte(strings.Replace(string(bad.Payload), `"event_type"`, `"phone":"0772123456","event_type"`, 1))
	o := &fakeOutbox{pending: []OutboxRow{outboxRow(1), bad}}
	_, err := newRelay(o, newFakeLedger()).RunOnce(context.Background())
	if !errors.Is(err, ErrPoisonRow) {
		t.Fatalf("err = %v, want ErrPoisonRow", err)
	}
	if o.completes != 0 || o.aborts != 1 || len(o.pending) != 2 {
		t.Fatalf("completes=%d aborts=%d pending=%d", o.completes, o.aborts, len(o.pending))
	}
}

func TestParseOutboxPayloadRejectsMismatchedEntryID(t *testing.T) {
	r := outboxRow(1)
	r.ID = outboxRow(2).ID
	if _, err := ParseOutboxPayload(r); !errors.Is(err, ErrPoisonRow) {
		t.Fatalf("err = %v, want ErrPoisonRow", err)
	}
}

// Dispatch never waits on audit: while the ledger is down the outbox keeps
// the rows, and the relay catches up in order with no gap or duplicate.
func TestRelayCatchesUpAfterLedgerOutage(t *testing.T) {
	o := &fakeOutbox{}
	for i := range 7 {
		o.pending = append(o.pending, outboxRow(i))
	}
	want := append([]OutboxRow(nil), o.pending...)
	l := newFakeLedger()
	l.failNext = 3
	r := newRelay(o, l)
	for range 3 {
		if _, err := r.RunOnce(context.Background()); !errors.Is(err, errLedgerDown) {
			t.Fatalf("during outage err = %v", err)
		}
		if len(o.pending) != 7 {
			t.Fatalf("outbox lost rows during outage: %d", len(o.pending))
		}
	}
	if n, err := r.RunOnce(context.Background()); err != nil || n != 7 {
		t.Fatalf("after outage RunOnce = %d, %v", n, err)
	}
	if len(l.entries) != 7 {
		t.Fatalf("ledger entries = %d, want 7", len(l.entries))
	}
	for i, e := range l.entries {
		if e.Seq != int64(i+1) || e.EntryID != want[i].ID {
			t.Fatalf("entry %d: seq %d id %v", i, e.Seq, e.EntryID)
		}
	}
}

// CodeRabbit PR #2: a payload followed by extra bytes does not match the
// strict contract and must be treated as poison.
func TestParseOutboxPayloadRejectsTrailingData(t *testing.T) {
	r := outboxRow(1)
	r.Payload = append(append([]byte{}, r.Payload...), []byte(` {"x":1}`)...)
	if _, err := ParseOutboxPayload(r); !errors.Is(err, ErrPoisonRow) {
		t.Fatalf("trailing data: err = %v, want ErrPoisonRow", err)
	}
	ok := outboxRow(2)
	ok.Payload = append(append([]byte{}, ok.Payload...), []byte(" \n")...)
	if _, err := ParseOutboxPayload(ok); err != nil {
		t.Fatalf("trailing whitespace rejected: %v", err)
	}
}

type failingCompleteOutbox struct{ fakeOutbox }

type failingBatch struct {
	*fakeBatch
	aborted *int
}

func (o *failingCompleteOutbox) Claim(ctx context.Context, topic string, limit int) (OutboxBatch, error) {
	b, _ := o.fakeOutbox.Claim(ctx, topic, limit)
	return &failingBatch{fakeBatch: b.(*fakeBatch), aborted: &o.aborts}, nil
}
func (b *failingBatch) Complete(context.Context, []uuid.UUID) error {
	return errors.New("commit failed")
}
func (b *failingBatch) Abort(context.Context) { *b.aborted++ }

// CodeRabbit PR #2: a failed Complete must still release the claimed rows.
func TestRelayAbortsWhenCompleteFails(t *testing.T) {
	o := &failingCompleteOutbox{}
	o.pending = []OutboxRow{outboxRow(1)}
	if _, err := newRelay(o, newFakeLedger()).RunOnce(context.Background()); err == nil {
		t.Fatal("Complete failure not reported")
	}
	if o.aborts != 1 {
		t.Fatalf("aborts = %d, want 1", o.aborts)
	}
	empty := &failingCompleteOutbox{}
	if _, err := newRelay(empty, newFakeLedger()).RunOnce(context.Background()); err == nil || empty.aborts != 1 {
		t.Fatalf("empty batch: err=%v aborts=%d, want error and 1 abort", err, empty.aborts)
	}
}

// CodeRabbit PR #2: a zero poll interval would spin against the database
// (the error backoff doubles from it, so it would spin there too).
func TestRelayRunRejectsNonPositivePoll(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		if err := newRelay(&fakeOutbox{}, newFakeLedger()).Run(context.Background(), d); !errors.Is(err, ErrInvalidPoll) {
			t.Fatalf("Run(poll=%v) = %v, want ErrInvalidPoll", d, err)
		}
	}
}

// Rows are appended in outbox ID (uuidv7, time) order even if an adapter
// returns them unordered.
func TestRelayAppendsInOutboxIDOrder(t *testing.T) {
	rows := []OutboxRow{outboxRow(1), outboxRow(2), outboxRow(3)}
	o := &fakeOutbox{pending: []OutboxRow{rows[2], rows[0], rows[1]}}
	l := newFakeLedger()
	if _, err := newRelay(o, l).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i, e := range l.entries {
		if e.EntryID != rows[i].ID {
			t.Fatalf("entry %d = %v, want %v (outbox ID order)", i, e.EntryID, rows[i].ID)
		}
	}
}
