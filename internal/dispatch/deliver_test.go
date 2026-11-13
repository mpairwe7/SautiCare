package dispatch

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/responder"
)

// attemptStore claims every attempt fresh and records how each settled and
// whether the settling context was still live.
type attemptStore struct {
	Store
	mu      sync.Mutex
	settled map[uuid.UUID]string
	deadCtx int // settlements attempted on a cancelled context
}

func (s *attemptStore) BeginAttempt(context.Context, uuid.UUID, string, string) (uuid.UUID, bool, error) {
	return uuid.NewV7(), true, nil
}
func (s *attemptStore) FinishAttempt(ctx context.Context, attempt uuid.UUID, status, _ string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		s.deadCtx++
		return false, ctx.Err()
	}
	s.settled[attempt] = status
	return true, nil
}

// slowDeliverer hangs for the incidents in hang until its context ends.
type slowDeliverer struct {
	hang map[uuid.UUID]bool
	mu   sync.Mutex
	sent []uuid.UUID
}

func (d *slowDeliverer) Deliver(ctx context.Context, x Delivery) (string, error) {
	if d.hang[x.IncidentID] {
		<-ctx.Done()
		return "", ctx.Err()
	}
	d.mu.Lock()
	d.sent = append(d.sent, x.IncidentID)
	d.mu.Unlock()
	return "ATXid_1", nil
}

func pendingSnap(n int) Snapshot {
	s := Snapshot{IncidentID: uuid.NewV7()}
	for range n {
		s.Alerts = append(s.Alerts, Alert{ID: uuid.NewV7(), ResponderID: uuid.NewV7(), Role: responder.RoleVHT, Outcome: OutcomePending})
	}
	return s
}

// Review I4 (rule 14): a provider that hangs on one incident's sends holds
// neither the other incidents' sends nor the pass: sends run concurrently,
// each bounded by the send timeout, and every attempt is settled.
func TestAHangingProviderDelaysNoOtherIncident(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		st := &attemptStore{settled: map[uuid.UUID]string{}}
		slow, a, b := pendingSnap(3), pendingSnap(3), pendingSnap(3)
		d := &slowDeliverer{hang: map[uuid.UUID]bool{slow.IncidentID: true}}
		e := &Engine{store: st, deliverer: d, sendTimeout: 8 * time.Second, workers: 4}
		start := time.Now()
		errs := e.deliverAll(t.Context(), []Snapshot{slow, a, b})
		if took := time.Since(start); took > 8*time.Second || len(errs) != 0 {
			t.Fatalf("delivery took %v (want at most one send timeout), errors %v", took, errs)
		}
		if len(d.sent) != 6 || len(st.settled) != 9 {
			t.Fatalf("sent %d, settled %d", len(d.sent), len(st.settled))
		}
		unknown := 0
		for _, s := range st.settled {
			if s == "unknown" {
				unknown++
			}
		}
		if unknown != 3 {
			t.Fatalf("the hung sends settled %d unknown, want 3", unknown)
		}
	})
}

// Review I4: a pass cancelled while a send is in flight still settles its
// attempt (on a context of its own), so no claim is left open for ever.
func TestAttemptsSettleAfterThePassIsCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		st := &attemptStore{settled: map[uuid.UUID]string{}}
		snap := pendingSnap(2)
		d := &slowDeliverer{hang: map[uuid.UUID]bool{snap.IncidentID: true}}
		e := &Engine{store: st, deliverer: d, sendTimeout: time.Minute, workers: 2}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		e.deliverAll(ctx, []Snapshot{snap})
		if st.deadCtx != 0 || len(st.settled) != 2 {
			t.Fatalf("settled %d, on a cancelled context %d", len(st.settled), st.deadCtx)
		}
	})
}
