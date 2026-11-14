package intake

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
)

type stalledReader struct {
	ids     []uuid.UUID
	overdue []Overdue
	after   time.Duration
}

func (s *stalledReader) ListStalledDispatch(_ context.Context, after time.Duration) ([]uuid.UUID, error) {
	s.after = after
	return s.ids, nil
}

// ListOverdueDispatch pages by (due, id), as the store does.
func (s *stalledReader) ListOverdueDispatch(_ context.Context, after time.Duration, from Overdue, limit int) ([]Overdue, error) {
	s.after = after
	var out []Overdue
	for _, o := range s.overdue {
		if (o.DueAt.After(from.DueAt) || (o.DueAt.Equal(from.DueAt) && o.ID.Compare(from.ID) > 0)) && len(out) < limit {
			out = append(out, o)
		}
	}
	return out, nil
}

type watchdogLog struct {
	got     []incident.AppendParams
	failFor uuid.UUID
}

func (a *watchdogLog) Append(_ context.Context, p incident.AppendParams) (incident.Appended, error) {
	if p.IncidentID == a.failFor {
		return incident.Appended{}, errors.New("store unavailable")
	}
	a.got = append(a.got, p)
	return incident.Appended{}, nil
}

// Review I8: dispatch-worker may be down or stuck. intake-gw's leader
// (another process) escalates every incident dispatched more than the
// watchdog time ago with no alert and no dispatch escalation at all, once,
// with the dispatch escalation key, so a coordinator takes over.
func TestWatchdogEscalatesStalledDispatch(t *testing.T) {
	a, b := uuid.NewV7(), uuid.NewV7()
	r := &stalledReader{ids: []uuid.UUID{a, b}}
	log := &watchdogLog{}
	n, err := NewWatchdog(r, log, 2*time.Minute).RunOnce(context.Background())
	if err != nil || n != 2 || r.after != 2*time.Minute || len(log.got) != 2 {
		t.Fatalf("n=%d err=%v after=%v appended=%d", n, err, r.after, len(log.got))
	}
	for i, id := range []uuid.UUID{a, b} {
		p := log.got[i]
		var pl struct {
			Reason string `json:"reason"`
		}
		if p.IncidentID != id || p.Type != incident.EvEscalated || json.Unmarshal(p.Payload, &pl) != nil ||
			pl.Reason != ReasonDispatchStalled || p.IdemKey != incident.EscalationKey(ReasonDispatchStalled, "") {
			t.Fatalf("append %d = %+v (%s)", i, p, p.Payload)
		}
	}
}

// F-05: a dispatch timer overdue by the watchdog time on a dispatching
// incident (the worker died after the first alert, so T3 is set) is a stall:
// escalated once per overdue wake.
func TestWatchdogEscalatesOverdueTimers(t *testing.T) {
	id := uuid.NewV7()
	due := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	r := &stalledReader{overdue: []Overdue{{ID: id, DueAt: due}}}
	log := &watchdogLog{}
	n, err := NewWatchdog(r, log, 2*time.Minute).RunOnce(context.Background())
	if err != nil || n != 1 || len(log.got) != 1 || log.got[0].IncidentID != id ||
		log.got[0].IdemKey != incident.EscalationKey(ReasonDispatchStalled, strconv.FormatInt(due.Unix(), 10)) {
		t.Fatalf("n=%d err=%v got %+v", n, err, log.got)
	}
}

// F-10: one incident whose append keeps failing does not stop the watchdog
// escalating the others; the failure is reported.
func TestOneFailingIncidentDoesNotStopTheWatchdog(t *testing.T) {
	bad, good, late := uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	r := &stalledReader{ids: []uuid.UUID{bad, good}, overdue: []Overdue{{ID: late, DueAt: time.Now()}}}
	log := &watchdogLog{failFor: bad}
	n, err := NewWatchdog(r, log, 2*time.Minute).RunOnce(context.Background())
	if err == nil || n != 2 || len(log.got) != 2 || log.got[0].IncidentID != good || log.got[1].IncidentID != late {
		t.Fatalf("n=%d err=%v got %+v", n, err, log.got)
	}
}

// PR #14 review: the watchdog reads every overdue timer, past the first page,
// so a long dispatch-worker outage cannot hide incidents behind the oldest 500.
func TestWatchdogReadsEveryPageOfOverdueTimers(t *testing.T) {
	due := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	r := &stalledReader{}
	for i := range 1001 {
		r.overdue = append(r.overdue, Overdue{ID: uuid.NewV7(), DueAt: due.Add(time.Duration(i/3) * time.Second)})
	}
	log := &watchdogLog{}
	n, err := NewWatchdog(r, log, 2*time.Minute).RunOnce(context.Background())
	if err != nil || n != 1001 || len(log.got) != 1001 {
		t.Fatalf("n=%d err=%v escalated %d, want all 1001", n, err, len(log.got))
	}
}
