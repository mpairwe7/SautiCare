package audit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
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
