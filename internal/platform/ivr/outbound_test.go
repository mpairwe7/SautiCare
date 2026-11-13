package ivr

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/location"
)

type forwardStore struct {
	due     []dispatch.ForwardCall
	claimed map[string]bool
	settled []string
	acked   []string
}

func (s *forwardStore) DueForwardCalls(context.Context, int) ([]dispatch.ForwardCall, error) {
	var out []dispatch.ForwardCall
	for _, d := range s.due {
		if !s.claimed[d.FacilityID] {
			out = append(out, d)
		}
	}
	return out, nil
}
func (s *forwardStore) BeginForwardAttempt(_ context.Context, _ uuid.UUID, facility, channel string) (uuid.UUID, bool, error) {
	if channel != dispatch.ChannelVoice || s.claimed[facility] {
		return uuid.UUID{}, false, nil
	}
	s.claimed[facility] = true
	return uuid.NewV7(), true, nil
}
func (s *forwardStore) FinishForwardAttempt(_ context.Context, _ uuid.UUID, status, _ string) (bool, error) {
	s.settled = append(s.settled, status)
	return true, nil
}
func (s *forwardStore) MessageFacts(context.Context, uuid.UUID) (dispatch.MessageFacts, error) {
	return dispatch.MessageFacts{Reference: "1234567894", Severity: incident.SevRed, Category: "trauma"}, nil
}
func (s *forwardStore) AcknowledgeForward(_ context.Context, _ uuid.UUID, facility string) (bool, error) {
	s.acked = append(s.acked, facility)
	return true, nil
}

func gazetteer(t *testing.T) location.File {
	t.Helper()
	raw, err := os.ReadFile("../../../config/gated/gazetteer.yaml")
	if err != nil {
		t.Fatal(err)
	}
	g, err := location.ParseFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// Scenarios 7 and 8: the forwarded health centre's public number is called
// once; the answered call hears the incident and "press 1"; 1 acknowledges.
func TestForwardCalls(t *testing.T) {
	a := &alertARI{}
	inc := uuid.NewV7()
	st := &forwardStore{due: []dispatch.ForwardCall{{IncidentID: inc, FacilityID: "hc_test_1"}}, claimed: map[string]bool{}}
	fc, err := NewForwardCalls(ForwardCallsConfig{ARI: a, Store: st, Dial: "PJSIP/{number}@sauti-trunk", Gazetteer: gazetteer(t)})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := fc.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(a.originated) != 1 || a.originated[0] != "PJSIP/+256000000001@sauti-trunk" || a.args[0][0] != "forward" || len(st.settled) != 1 {
		t.Fatalf("originated %v args %v settled %v", a.originated, a.args, st.settled)
	}
	ch := "fwd-1"
	fc.Handle(context.Background(), Event{Type: EventStasisStart, ChannelID: ch, Args: a.args[0]})
	if len(a.plays) != 1 || !slices.Contains(a.plays[0], "sauti/en/forward-options") || !slices.Contains(a.plays[0], "sauti/digits/4") {
		t.Fatalf("plays %v", a.plays)
	}
	fc.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: ch, Digit: "1"})
	if slices.Contains(a.hungUp, ch) {
		t.Fatal("hung up before the acknowledgement was heard")
	}
	a.finish(t, fc, ch)
	if !slices.Equal(st.acked, []string{"hc_test_1"}) || !slices.Contains(a.hungUp, ch) {
		t.Fatalf("acknowledged %v hung up %v", st.acked, a.hungUp)
	}
}

type coordStore struct {
	calls  []dispatch.CoordinatorCall
	acked  map[uuid.UUID]int64
	limits []int
}

func (s *coordStore) ClaimCoordinatorCalls(_ context.Context, _ time.Duration, limit int) ([]dispatch.CoordinatorCall, error) {
	s.limits = append(s.limits, limit)
	n := min(limit, len(s.calls))
	out := s.calls[:n]
	s.calls = s.calls[n:]
	return out, nil
}
func (s *coordStore) AcknowledgeCoordinatorCall(_ context.Context, id uuid.UUID, seq int64) error {
	s.acked[id] = seq
	return nil
}

// Scenario 11 and every escalation: the coordinator line rings with the
// incident number and the reason; 1 acknowledges the escalations up to it.
func TestCoordinatorCallsRingAndAcknowledge(t *testing.T) {
	a := &alertARI{}
	inc := uuid.NewV7()
	st := &coordStore{calls: []dispatch.CoordinatorCall{{IncidentID: inc, Seq: 7, Reason: "triage_rule_error", Reference: "1234567894", Calls: 1}},
		acked: map[uuid.UUID]int64{}}
	cc, err := NewCoordinatorCalls(CoordinatorCallsConfig{ARI: a, Store: st, Endpoint: "PJSIP/coordinators", Retry: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := cc.RunOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("rang %d %v", n, err)
	}
	if len(a.originated) != 1 || a.originated[0] != "PJSIP/coordinators" || a.args[0][0] != "escalation" {
		t.Fatalf("originated %v %v", a.originated, a.args)
	}
	ch := "esc-1"
	cc.Handle(context.Background(), Event{Type: EventStasisStart, ChannelID: ch, Args: a.args[0]})
	if len(a.plays) != 1 || !slices.Contains(a.plays[0], "sauti/en/escalation-reason-triage_rule_error") {
		t.Fatalf("plays %v", a.plays)
	}
	cc.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: ch, Digit: "1"})
	if slices.Contains(a.hungUp, ch) {
		t.Fatal("hung up before the acknowledgement was heard")
	}
	a.finish(t, cc, ch)
	if st.acked[inc] != 7 || !slices.Contains(a.hungUp, ch) {
		t.Fatalf("acknowledged %v hung up %v", st.acked, a.hungUp)
	}
	if got := escalationReasonSound("en", "something_new"); got != "sauti/en/escalation-reason-other" {
		t.Fatalf("unknown reason sound %s", got)
	}
}

// One Stasis application serves all outbound calls: each answered call goes
// to its kind.
func TestOutboundRoutesByKind(t *testing.T) {
	a := &alertARI{}
	st := &coordStore{acked: map[uuid.UUID]int64{}}
	cc, _ := NewCoordinatorCalls(CoordinatorCallsConfig{ARI: a, Store: st, Endpoint: "PJSIP/coordinators", Retry: time.Minute})
	o := Outbound{ARI: a, Coordinator: cc}
	o.Handle(context.Background(), Event{Type: EventStasisStart, ChannelID: "x", Args: []string{"escalation", uuid.NewV7().String(), "3", "dispatch_stalled", "1234567894"}})
	o.Handle(context.Background(), Event{Type: EventStasisStart, ChannelID: "y", Args: []string{"nonsense"}})
	if len(a.answered) != 1 || !slices.Contains(a.hungUp, "y") {
		t.Fatalf("answered %v hung up %v", a.answered, a.hungUp)
	}
}

// Review I5: the coordinator line is never rung twice at once. With one line,
// a second escalation waits while a call rings (until it is answered and ends,
// or the ring time passes unanswered); with two lines, two ring together.
func TestCoordinatorLineIsRungOneCallAtATime(t *testing.T) {
	a := &alertARI{}
	call := func(n byte) dispatch.CoordinatorCall {
		return dispatch.CoordinatorCall{IncidentID: uuid.NewV7(), Seq: int64(n), Reason: "dispatch_stalled", Reference: "1234567894", Calls: 1}
	}
	st := &coordStore{calls: []dispatch.CoordinatorCall{call(1), call(2), call(3)}, acked: map[uuid.UUID]int64{}}
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	cc, err := NewCoordinatorCalls(CoordinatorCallsConfig{ARI: a, Store: st, Endpoint: "PJSIP/coordinators", Ring: 30 * time.Second,
		Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if n, _ := cc.RunOnce(ctx); n != 1 || len(a.originated) != 1 {
		t.Fatalf("first ring %d %v", n, a.originated)
	}
	if n, _ := cc.RunOnce(ctx); n != 0 || len(st.limits) != 1 {
		t.Fatalf("rang again while the line rings: %d (claims %v)", n, st.limits)
	}
	// Answered: the line stays busy until the call ends, however long it lasts.
	ch := "out-1"
	cc.Handle(ctx, Event{Type: EventStasisStart, ChannelID: ch, Args: a.args[0]})
	now = now.Add(5 * time.Minute)
	if n, _ := cc.RunOnce(ctx); n != 0 {
		t.Fatal("rang during an answered call")
	}
	cc.Handle(ctx, Event{Type: EventStasisEnd, ChannelID: ch})
	if n, _ := cc.RunOnce(ctx); n != 1 {
		t.Fatal("line not freed when the call ended")
	}
	// Unanswered: free again once the ring time (and a margin) has passed.
	now = now.Add(20 * time.Second)
	if n, _ := cc.RunOnce(ctx); n != 0 {
		t.Fatal("rang again while the first call may still ring")
	}
	now = now.Add(time.Minute)
	if n, _ := cc.RunOnce(ctx); n != 1 || len(a.originated) != 3 {
		t.Fatalf("line not freed after an unanswered ring: %d %v", n, a.originated)
	}

	st2 := &coordStore{calls: []dispatch.CoordinatorCall{call(1), call(2), call(3)}, acked: map[uuid.UUID]int64{}}
	cc2, _ := NewCoordinatorCalls(CoordinatorCallsConfig{ARI: &alertARI{}, Store: st2, Endpoint: "PJSIP/coordinators", Lines: 2})
	if n, _ := cc2.RunOnce(ctx); n != 2 || st2.limits[0] != 2 {
		t.Fatalf("two lines: %d %v", n, st2.limits)
	}
}
