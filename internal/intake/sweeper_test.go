package intake

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
)

type idleReader struct {
	idle   []Idle
	window time.Duration
}

func (r *idleReader) ListIdle(_ context.Context, idleFor time.Duration) ([]Idle, error) {
	r.window = idleFor
	return r.idle, nil
}

type appendLog struct{ calls []incident.AppendParams }

func (a *appendLog) Append(_ context.Context, p incident.AppendParams) (incident.Appended, error) {
	a.calls = append(a.calls, p)
	return incident.Appended{}, nil
}

func (a *appendLog) payloads(id uuid.UUID) []string {
	var out []string
	for _, c := range a.calls {
		if c.IncidentID == id {
			out = append(out, string(c.Type)+" "+string(c.Payload))
		}
	}
	return out
}

func TestSweeperFlagsAbandonedBeforeCategory(t *testing.T) {
	id := uuid.NewV7()
	r, log := &idleReader{idle: []Idle{{ID: id, Danger: "no"}}}, &appendLog{}
	n, err := NewSweeper(r, log, 2*time.Minute).RunOnce(context.Background())
	if err != nil || n != 1 || r.window != 2*time.Minute {
		t.Fatalf("n=%d err=%v window=%v", n, err, r.window)
	}
	got := log.payloads(id)
	want := []string{`incident.flagged {"flag":"abandoned"}`, `incident.flagged {"flag":"abandoned_before_category"}`}
	if !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

// Guide §4: a session dropped after a yes at screen 2 and before dispatch is
// the most dangerous partial record: the coordinator is alerted at once.
func TestSweeperEscalatesDangerYesBeforeDispatch(t *testing.T) {
	early, dispatched := uuid.NewV7(), uuid.NewV7()
	r := &idleReader{idle: []Idle{
		{ID: early, Danger: "yes", Category: "trauma"},
		{ID: dispatched, Danger: "yes", Category: "trauma", Dispatched: true},
	}}
	log := &appendLog{}
	if _, err := NewSweeper(r, log, time.Minute).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := log.payloads(early); !slices.Contains(got, `incident.escalated {"reason":"abandoned_after_danger_yes"}`) {
		t.Fatalf("danger-yes abandonment not escalated: %v", got)
	}
	if got := log.payloads(dispatched); slices.ContainsFunc(got, func(s string) bool { return s[:18] == "incident.escalated" }) {
		t.Fatalf("escalated although dispatch is under way: %v", got)
	}
}

// Keys are stable per incident, so a sweep repeated by a new leader or after
// a crash writes nothing twice.
func TestSweeperIsIdempotent(t *testing.T) {
	id := uuid.NewV7()
	r := &idleReader{idle: []Idle{{ID: id, Danger: "yes"}}}
	log := &appendLog{}
	sw := NewSweeper(r, log, time.Minute)
	_, _ = sw.RunOnce(context.Background())
	_, _ = sw.RunOnce(context.Background())
	keys := map[string]int{}
	for _, c := range log.calls {
		keys[c.IdemKey]++
		var m map[string]string
		if json.Unmarshal(c.Payload, &m) != nil || c.ActorKind != incident.ActorSystem {
			t.Fatalf("bad call %+v", c)
		}
	}
	if len(keys) != 3 {
		t.Fatalf("distinct keys = %v, want abandoned, abandoned_before_category, escalate", keys)
	}
	for k, n := range keys {
		if n != 2 {
			t.Fatalf("key %s used %d times across two sweeps", k, n)
		}
	}
}

func TestNewSweeperRejectsNonPositiveIdle(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewSweeper accepted a zero idle time")
		}
	}()
	NewSweeper(&idleReader{}, &appendLog{}, 0)
}
