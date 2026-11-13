package intake

import (
	"context"
	"encoding/json"
	"errors"
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

// appendLog records batches; fail refuses every batch, as a store whose
// transaction aborts stores none of it.
type appendLog struct {
	calls   []incident.AppendParams
	batches int
	fail    error
	failFor uuid.UUID // refuse only this incident's batches
}

func (a *appendLog) AppendBatch(_ context.Context, ps []incident.AppendParams) ([]incident.Appended, error) {
	if a.fail != nil || (len(ps) > 0 && ps[0].IncidentID == a.failFor) {
		return nil, errors.New("store unavailable")
	}
	a.batches++
	a.calls = append(a.calls, ps...)
	return make([]incident.Appended, len(ps)), nil
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
	n, err := NewSweeper(r, log, noCatchUp{}, 2*time.Minute).RunOnce(context.Background())
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
	if _, err := NewSweeper(r, log, noCatchUp{}, time.Minute).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := log.payloads(early); !slices.Contains(got, `incident.escalated {"reason":"abandoned_after_danger_yes"}`) {
		t.Fatalf("danger-yes abandonment not escalated: %v", got)
	}
	if got := log.payloads(dispatched); slices.ContainsFunc(got, func(s string) bool { return s[:18] == "incident.escalated" }) {
		t.Fatalf("escalated although dispatch is under way: %v", got)
	}
}

// I5: a call classified red (screen 5 yes) whose caller answered no or
// "don't know" at screen 2 and dropped before T2 is as urgent as a danger
// yes: it is escalated too, with its own reason. A danger yes keeps its
// reason; a call already dispatching is not escalated.
func TestSweeperEscalatesRedBeforeDispatch(t *testing.T) {
	no, unknown, both, dispatched, yellow := uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	r := &idleReader{idle: []Idle{
		{ID: no, Danger: "no", Category: "trauma", Severity: incident.SevRed},
		{ID: unknown, Danger: "unknown", Category: "trauma", Severity: incident.SevRed},
		{ID: both, Danger: "yes", Category: "trauma", Severity: incident.SevRed},
		{ID: dispatched, Danger: "no", Category: "trauma", Severity: incident.SevRed, Dispatched: true},
		{ID: yellow, Danger: "no", Category: "trauma", Severity: incident.SevYellow},
	}}
	log := &appendLog{}
	if _, err := NewSweeper(r, log, noCatchUp{}, time.Minute).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	red := `incident.escalated {"reason":"abandoned_red_before_dispatch"}`
	for _, id := range []uuid.UUID{no, unknown} {
		if got := log.payloads(id); !slices.Contains(got, red) {
			t.Errorf("red abandonment not escalated: %v", got)
		}
	}
	if got := log.payloads(both); !slices.Contains(got, `incident.escalated {"reason":"abandoned_after_danger_yes"}`) || slices.Contains(got, red) {
		t.Errorf("danger yes and red: %v", got)
	}
	for _, id := range []uuid.UUID{dispatched, yellow} {
		if got := log.payloads(id); slices.ContainsFunc(got, func(s string) bool { return s[:18] == "incident.escalated" }) {
			t.Errorf("escalated: %v", got)
		}
	}
}

// Keys are stable per incident, so a sweep repeated by a new leader or after
// a crash writes nothing twice.
func TestSweeperIsIdempotent(t *testing.T) {
	id := uuid.NewV7()
	r := &idleReader{idle: []Idle{{ID: id, Danger: "yes"}}}
	log := &appendLog{}
	sw := NewSweeper(r, log, noCatchUp{}, time.Minute)
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

// CodeRabbit PR #6: an incident's abandonment flags and its escalation are
// one batch. Were the flag stored and the escalation lost, the flag would
// keep the incident off later sweeps with the coordinator never alerted; a
// failed batch stores nothing, so the next sweep lists it again.
func TestSweeperCommitsAbandonmentAndEscalationTogether(t *testing.T) {
	id := uuid.NewV7()
	r := &idleReader{idle: []Idle{{ID: id, Danger: "yes"}}}
	failing := &appendLog{fail: errors.New("store unavailable")}
	if _, err := NewSweeper(r, failing, noCatchUp{}, time.Minute).RunOnce(context.Background()); err == nil || len(failing.calls) != 0 {
		t.Fatalf("failed batch: err=%v, stored %v", err, failing.calls)
	}
	log := &appendLog{}
	if _, err := NewSweeper(r, log, noCatchUp{}, time.Minute).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{`incident.flagged {"flag":"abandoned"}`, `incident.flagged {"flag":"abandoned_before_category"}`,
		`incident.escalated {"reason":"abandoned_after_danger_yes"}`}
	if got := log.payloads(id); log.batches != 1 || !slices.Equal(got, want) {
		t.Fatalf("%d batches: %v, want one batch %v", log.batches, got, want)
	}
}

func TestNewSweeperRejectsNonPositiveIdle(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewSweeper accepted a zero idle time")
		}
	}()
	NewSweeper(&idleReader{}, &appendLog{}, noCatchUp{}, 0)
}

// noCatchUp is a store where nothing more can be recorded for an incident.
type noCatchUp struct{}

func (noCatchUp) CatchUp(context.Context, uuid.UUID) (bool, error) { return false, nil }

// catchUps records the catch-ups and reports some incidents dispatched.
type catchUps struct {
	dispatches map[uuid.UUID]bool
	asked      []uuid.UUID
}

func (c *catchUps) CatchUp(_ context.Context, id uuid.UUID) (bool, error) {
	c.asked = append(c.asked, id)
	return c.dispatches[id], nil
}

func escalations(log *appendLog, id uuid.UUID) []string {
	var out []string
	for _, p := range log.payloads(id) {
		if p[:18] == "incident.escalated" {
			out = append(out, p)
		}
	}
	return out
}

// F-04: a yellow call located to its ward whose dispatch never started (a
// failed screen-7 request, the caller gone) reaches a coordinator.
func TestSweeperEscalatesALocatedYellowCallWithoutT2(t *testing.T) {
	id := uuid.NewV7()
	r := &idleReader{idle: []Idle{{ID: id, Danger: "no", Category: "poisoning", Severity: incident.SevYellow, Ward: "w01"}}}
	log, c := &appendLog{}, &catchUps{}
	if _, err := NewSweeper(r, log, c, time.Minute).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := escalations(log, id); !slices.Equal(got, []string{`incident.escalated {"reason":"dispatch_not_started"}`}) || !slices.Equal(c.asked, []uuid.UUID{id}) {
		t.Fatalf("escalations %v, catch-ups %v", got, c.asked)
	}
}

// A recorded landmark is dispatched by the sweep, exactly as a redial would
// (catch-up records T2): dispatch is under way, so nothing is escalated.
func TestSweeperRecordsT2ByCatchUpWhenTheLandmarkIsKnown(t *testing.T) {
	id := uuid.NewV7()
	r := &idleReader{idle: []Idle{{ID: id, Danger: "no", Category: "poisoning", Severity: incident.SevYellow, Ward: "w01"}}}
	log := &appendLog{}
	if _, err := NewSweeper(r, log, &catchUps{dispatches: map[uuid.UUID]bool{id: true}}, time.Minute).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := escalations(log, id); len(got) != 0 || !slices.Contains(log.payloads(id), `incident.flagged {"flag":"abandoned"}`) {
		t.Fatalf("events %v", log.payloads(id))
	}
}

// Before a ward is given there is nowhere to send help: the call is only
// flagged, as today (the PI decides whether ward-level dispatch should
// change that, R-9).
func TestSweeperBeforeTheWardStillOnlyFlags(t *testing.T) {
	id := uuid.NewV7()
	r := &idleReader{idle: []Idle{{ID: id, Danger: "no", Category: "trauma", Severity: incident.SevGreen}}}
	log := &appendLog{}
	if _, err := NewSweeper(r, log, &catchUps{}, time.Minute).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := log.payloads(id); !slices.Equal(got, []string{`incident.flagged {"flag":"abandoned"}`}) {
		t.Fatalf("events %v", got)
	}
}

// F-10: one incident whose writes keep failing does not hold up the others
// behind it in the list; the failure is reported.
func TestOneFailingIncidentDoesNotStopTheSweep(t *testing.T) {
	bad, good := uuid.NewV7(), uuid.NewV7()
	r := &idleReader{idle: []Idle{{ID: bad, Danger: "yes"}, {ID: good, Danger: "yes"}}}
	log := &appendLog{failFor: bad}
	n, err := NewSweeper(r, log, noCatchUp{}, time.Minute).RunOnce(context.Background())
	if err == nil || n != 1 || len(escalations(log, good)) != 1 {
		t.Fatalf("n=%d err=%v events %v", n, err, log.calls)
	}
}

// "Somewhere else" is not a ward Sauti serves: an out-of-area incident left
// open (out_of_area_escalate_after_danger on) is never "dispatch not
// started"; it was escalated when the caller answered.
func TestSweeperDoesNotTakeSomewhereElseForAWard(t *testing.T) {
	id := uuid.NewV7()
	r := &idleReader{idle: []Idle{{ID: id, Danger: "yes", Category: "trauma", Ward: "elsewhere"}}}
	log := &appendLog{}
	if _, err := NewSweeper(r, log, noCatchUp{}, time.Minute).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := escalations(log, id); len(got) != 0 {
		t.Fatalf("escalations %v", got)
	}
}
