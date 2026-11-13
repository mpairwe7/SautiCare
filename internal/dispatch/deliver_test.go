package dispatch

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
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

// pipeStore claims two incidents, most urgent first: ready plans at once (a
// closed incident with a stand-down to send), hung's plan hangs until its
// pass times out. It records when the ready incident's wake is stored.
type pipeStore struct {
	*attemptStore
	ready, hung uuid.UUID
	claimed     bool
	scheduled   map[uuid.UUID]time.Time
	escalated   []uuid.UUID
}

func (s *pipeStore) ClaimDueDispatch(context.Context, int, time.Duration) ([]Due, error) {
	if s.claimed {
		return nil, nil
	}
	s.claimed = true
	return []Due{{IncidentID: s.ready, Gen: 1}, {IncidentID: s.hung, Gen: 1}}, nil
}
func (s *pipeStore) DispatchSnapshot(ctx context.Context, id uuid.UUID) (Snapshot, error) {
	if id == s.hung {
		<-ctx.Done()
		return Snapshot{}, ctx.Err()
	}
	return Snapshot{IncidentID: id, State: incident.StateClosed, Alerts: []Alert{{ID: uuid.NewV7(), ResponderID: uuid.NewV7(),
		Role: responder.RoleVHT, Outcome: OutcomeStoodDown}}}, nil
}
func (s *pipeStore) Now(context.Context) (time.Time, error)          { return time.Now(), nil }
func (s *pipeStore) DispatchSuspended(context.Context) (bool, error) { return false, nil }
func (s *pipeStore) ScheduleDispatch(_ context.Context, id uuid.UUID, _ int64, _ *time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scheduled[id] = time.Now()
	return nil
}
func (s *pipeStore) DispatchEscalate(_ context.Context, id uuid.UUID, _ string, _ responder.Role, _ string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.escalated = append(s.escalated, id)
	return true, nil
}

// timedDeliverer records when each incident's message went out.
type timedDeliverer struct {
	mu     sync.Mutex
	sentAt map[uuid.UUID]time.Time
}

func (d *timedDeliverer) Deliver(_ context.Context, x Delivery) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sentAt[x.IncidentID] = time.Now()
	return "ATXid_1", nil
}

// CodeRabbit PR #11 (4560453605): an incident's sends start as soon as it is
// planned, on the batch's shared workers, while later incidents still plan:
// a later plan that hangs until its pass timeout delays neither the first
// incident's message nor its next wake. The hung incident is escalated.
func TestAPlannedIncidentSendsWhileLaterOnesPlan(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		st := &pipeStore{attemptStore: &attemptStore{settled: map[uuid.UUID]string{}}, ready: uuid.NewV7(), hung: uuid.NewV7(),
			scheduled: map[uuid.UUID]time.Time{}}
		d := &timedDeliverer{sentAt: map[uuid.UUID]time.Time{}}
		e := &Engine{store: st, deliverer: d, suspension: st, passTimeout: 20 * time.Second, sendTimeout: 5 * time.Second, workers: 4}
		start := time.Now()
		var failed []uuid.UUID
		if n, err := e.RunOnce(t.Context(), 10, time.Minute, func(id uuid.UUID, _ error) { failed = append(failed, id) }); err != nil || n != 2 {
			t.Fatalf("pass: %d %v", n, err)
		}
		sent, ok := d.sentAt[st.ready]
		if !ok || sent.Sub(start) >= e.passTimeout {
			t.Fatalf("the planned incident's message went out after %v (sent %v), want before the later plan's %v timeout",
				sent.Sub(start), ok, e.passTimeout)
		}
		if at, ok := st.scheduled[st.ready]; !ok || at.Sub(start) >= e.passTimeout {
			t.Fatalf("the planned incident's wake stored after %v (%v)", at.Sub(start), ok)
		}
		if _, ok := st.scheduled[st.hung]; ok || len(failed) != 1 || failed[0] != st.hung || len(st.escalated) != 1 {
			t.Fatalf("the hung incident: scheduled %v, failed %v, escalated %v", st.scheduled, failed, st.escalated)
		}
		if len(st.settled) != 1 {
			t.Fatalf("settled %v", st.settled)
		}
	})
}
