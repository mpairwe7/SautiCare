package ivr

import (
	"context"
	"errors"
	"os"
	"regexp"
	"slices"
	"testing"
	"testing/synctest"
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
	st := &coordStore{calls: []dispatch.CoordinatorCall{{IncidentID: inc, Seq: 7, Reasons: []string{"triage_rule_error"}, Reference: "1234567894", Calls: 1}},
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
		return dispatch.CoordinatorCall{IncidentID: uuid.NewV7(), Seq: int64(n), Reasons: []string{"dispatch_stalled"}, Reference: "1234567894", Calls: 1}
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

// CodeRabbit PR #11 (4560453615): on forwarding and coordinator calls only 1
// and 3 are keys; any other leaves the prompt playing.
func TestAnInvalidKeyDoesNotCutAnOutboundPrompt(t *testing.T) {
	a := &alertARI{}
	fs := &forwardStore{claimed: map[string]bool{}}
	fc, err := NewForwardCalls(ForwardCallsConfig{ARI: a, Store: fs, Dial: "PJSIP/{number}@sauti-trunk", Gazetteer: gazetteer(t)})
	if err != nil {
		t.Fatal(err)
	}
	fc.Handle(context.Background(), Event{Type: EventStasisStart, ChannelID: "fwd-1", Args: []string{"forward", uuid.NewV7().String(), "hc_test_1"}})
	cs := &coordStore{acked: map[uuid.UUID]int64{}}
	cc, err := NewCoordinatorCalls(CoordinatorCallsConfig{ARI: a, Store: cs, Endpoint: "PJSIP/coordinators"})
	if err != nil {
		t.Fatal(err)
	}
	cc.Handle(context.Background(), Event{Type: EventStasisStart, ChannelID: "esc-1",
		Args: []string{"escalation", uuid.NewV7().String(), "3", "dispatch_stalled", "1234567894"}})
	for _, k := range []string{"2", "9", "#"} {
		fc.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: "fwd-1", Digit: k})
		cc.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: "esc-1", Digit: k})
	}
	if len(a.stopped) != 0 || len(a.plays) != 2 || len(fs.acked) != 0 || len(cs.acked) != 0 {
		t.Fatalf("invalid keys: stopped %v plays %d", a.stopped, len(a.plays))
	}
	fc.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: "fwd-1", Digit: "3"})
	cc.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: "esc-1", Digit: "3"})
	if len(a.stopped) != 2 || len(a.plays) != 4 {
		t.Fatalf("3 repeats, cutting the prompt: stopped %v plays %d", a.stopped, len(a.plays))
	}
}

func escalation(n int64) dispatch.CoordinatorCall {
	return dispatch.CoordinatorCall{IncidentID: uuid.NewV7(), Seq: n, Reasons: []string{"dispatch_stalled"}, Reference: "1234567894", Calls: 1}
}

// CodeRabbit PR #11 (4560453632): an answer event handled before RunOnce has
// stored the call it placed still marks the line answered, and RunOnce keeps
// that mark instead of a ringing reservation that would lapse while the call
// is under way.
func TestAnEarlyAnswerKeepsTheLine(t *testing.T) {
	a := &alertARI{}
	st := &coordStore{calls: []dispatch.CoordinatorCall{escalation(1), escalation(2)}, acked: map[uuid.UUID]int64{}}
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	cc, err := NewCoordinatorCalls(CoordinatorCallsConfig{ARI: a, Store: st, Endpoint: "PJSIP/coordinators", Ring: 30 * time.Second,
		Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a.onOriginate = func(ch string) {
		cc.Handle(ctx, Event{Type: EventStasisStart, ChannelID: ch, Args: a.args[len(a.args)-1]})
	}
	if n, _ := cc.RunOnce(ctx); n != 1 {
		t.Fatalf("first ring %d", n)
	}
	a.onOriginate = nil
	now = now.Add(5 * time.Minute) // long past the ring time
	if n, _ := cc.RunOnce(ctx); n != 0 || len(a.originated) != 1 {
		t.Fatalf("rang during a call answered before it was stored: %d %v", n, a.originated)
	}
	cc.Handle(ctx, Event{Type: EventStasisEnd, ChannelID: "out-1"})
	if n, _ := cc.RunOnce(ctx); n != 1 {
		t.Fatal("line not freed when the call ended")
	}
}

// CodeRabbit PR #11 (4560453637): a call's end can be lost while another
// process leads (its StasisEnd goes to that leader's websocket). When this
// process leads again, Run first asks Asterisk about every line it holds: a
// channel that no longer exists frees its line; one that still exists, or
// that Asterisk cannot be asked about, keeps it (never ring a line in use).
func TestANewTermFreesLinesWhoseCallsEnded(t *testing.T) {
	a := &alertARI{gone: map[string]bool{}}
	st := &coordStore{calls: []dispatch.CoordinatorCall{escalation(1), escalation(2), escalation(3)}, acked: map[uuid.UUID]int64{}}
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	cc, err := NewCoordinatorCalls(CoordinatorCallsConfig{ARI: a, Store: st, Endpoint: "PJSIP/coordinators", Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if n, _ := cc.RunOnce(ctx); n != 1 {
		t.Fatalf("first ring %d", n)
	}
	cc.Handle(ctx, Event{Type: EventStasisStart, ChannelID: "out-1", Args: a.args[0]})
	a.mu.Lock()
	a.gone["out-1"] = true // ended while another process led: no StasisEnd here
	a.mu.Unlock()
	if n, _ := cc.RunOnce(ctx); n != 0 {
		t.Fatal("rang during an answered call")
	}
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- cc.Run(rctx, time.Hour, nil) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		a.mu.Lock()
		rang := len(a.originated)
		a.mu.Unlock()
		if rang == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the new term did not free the line of a call that had ended")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	cc.Handle(ctx, Event{Type: EventStasisStart, ChannelID: "out-2", Args: a.args[1]})
	a.existsErr = errors.New("asterisk unreachable")
	cc.reconcile(ctx)
	if n, _ := cc.RunOnce(ctx); n != 0 {
		t.Fatal("a line was freed on an Asterisk that could not be asked")
	}
	a.existsErr = nil
	cc.reconcile(ctx)
	if n, _ := cc.RunOnce(ctx); n != 0 {
		t.Fatal("the line of a call still under way was freed")
	}
	a.gone["out-2"] = true
	cc.reconcile(ctx)
	if n, _ := cc.RunOnce(ctx); n != 1 {
		t.Fatal("line not freed once its call had ended")
	}
}

// CodeRabbit PR #11 (4560453641): an answered coordinator or forwarding call
// with no key pressed for the no-input time (default 2 minutes) is hung up,
// freeing the line; the escalation, unacknowledged, rings again at its
// interval. A key the call offers restarts the time (an invalid one does
// not); the end of the call stops it.
func TestAnAnsweredCallWithNoInputIsHungUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := &alertARI{}
		st := &coordStore{calls: []dispatch.CoordinatorCall{escalation(1), escalation(2)}, acked: map[uuid.UUID]int64{}}
		cc, err := NewCoordinatorCalls(CoordinatorCallsConfig{ARI: a, Store: st, Endpoint: "PJSIP/coordinators"})
		if err != nil {
			t.Fatal(err)
		}
		if cc.c.NoInput != 2*time.Minute {
			t.Fatalf("default no-input time %v", cc.c.NoInput)
		}
		ctx := context.Background()
		if n, _ := cc.RunOnce(ctx); n != 1 {
			t.Fatalf("first ring %d", n)
		}
		cc.Handle(ctx, Event{Type: EventStasisStart, ChannelID: "out-1", Args: a.args[0]})
		time.Sleep(90 * time.Second)
		cc.Handle(ctx, Event{Type: EventDTMF, ChannelID: "out-1", Digit: "3"}) // heard again: input
		time.Sleep(90 * time.Second)
		synctest.Wait()
		if a.hangups("out-1") != 0 {
			t.Fatal("hung up 90 s after a key")
		}
		if n, _ := cc.RunOnce(ctx); n != 0 {
			t.Fatal("rang during the answered call")
		}
		time.Sleep(31 * time.Second)
		synctest.Wait()
		if a.hangups("out-1") != 1 {
			t.Fatal("an answered call with no input was not hung up")
		}
		if n, _ := cc.RunOnce(ctx); n != 1 {
			t.Fatal("the line was not freed after the no-input hang-up")
		}
		// Acknowledged, heard, ended: the timer stops with the call.
		cc.Handle(ctx, Event{Type: EventStasisStart, ChannelID: "out-2", Args: a.args[1]})
		cc.Handle(ctx, Event{Type: EventDTMF, ChannelID: "out-2", Digit: "1"})
		a.finish(t, cc, "out-2")
		cc.Handle(ctx, Event{Type: EventStasisEnd, ChannelID: "out-2"})
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		if n := a.hangups("out-2"); n != 1 {
			t.Fatalf("an ended call was hung up %d times", n)
		}

		fs := &forwardStore{claimed: map[string]bool{}}
		fc, err := NewForwardCalls(ForwardCallsConfig{ARI: a, Store: fs, Dial: "PJSIP/{number}@sauti-trunk", Gazetteer: gazetteer(t),
			NoInput: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		fc.Handle(ctx, Event{Type: EventStasisStart, ChannelID: "fwd-1", Args: []string{"forward", uuid.NewV7().String(), "hc_test_1"}})
		time.Sleep(40 * time.Second)
		fc.Handle(ctx, Event{Type: EventDTMF, ChannelID: "fwd-1", Digit: "9"}) // not a key of this call: no input
		time.Sleep(21 * time.Second)
		synctest.Wait()
		if a.hangups("fwd-1") != 1 {
			t.Fatal("a forwarding call with no input was not hung up")
		}
	})
}

// F-06 (ADR-0029 rule 8): a call announces every reason it acknowledges,
// the most urgent first.
func TestCoordinatorCallSpeaksReasonsInPriorityOrder(t *testing.T) {
	a := &alertARI{}
	inc := uuid.NewV7()
	st := &coordStore{calls: []dispatch.CoordinatorCall{{IncidentID: inc, Seq: 9, Reasons: []string{"no_eligible_responder", "triage_rule_error"},
		Reference: "1234567894", Calls: 1}}, acked: map[uuid.UUID]int64{}}
	cc, err := NewCoordinatorCalls(CoordinatorCallsConfig{ARI: a, Store: st, Endpoint: "PJSIP/coordinators", Retry: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cc.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	cc.Handle(context.Background(), Event{Type: EventStasisStart, ChannelID: "esc-1", Args: a.args[0]})
	p := a.plays[0]
	rule, none := slices.Index(p, "sauti/en/escalation-reason-triage_rule_error"), slices.Index(p, "sauti/en/escalation-reason-no_eligible_responder")
	if rule < 0 || none < 0 || rule > none {
		t.Fatalf("plays %v: want the triage rule error first, then no eligible responder", p)
	}
	cc.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: "esc-1", Digit: "1"})
	if st.acked[inc] != 9 {
		t.Fatalf("acknowledged %v, want the claim's seq", st.acked)
	}
}

// Tier 2 (F-01; ADR-0029 rule 8): once an escalation has rung backup_after
// times unanswered, the backup line rings as well, from the next ring on
// (Calls counts the ring being placed; review R5). The first 1 on either
// acknowledges, and the other is hung up.
func TestBackupLineRingsAfterThreeUnansweredCalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var a *alertARI
		var cc *CoordinatorCalls
		var st *coordStore
		inc := uuid.NewV7()
		for _, tc := range []struct {
			calls int
			want  []string
		}{{3, []string{"PJSIP/coordinators"}}, {4, []string{"PJSIP/coordinators", "PJSIP/backup"}}} {
			a = &alertARI{}
			st = &coordStore{calls: []dispatch.CoordinatorCall{{IncidentID: inc, Seq: 7, Reasons: []string{"triage_rule_error"},
				Reference: "1234567894", Calls: tc.calls}}, acked: map[uuid.UUID]int64{}}
			var err error
			cc, err = NewCoordinatorCalls(CoordinatorCallsConfig{ARI: a, Store: st, Endpoint: "PJSIP/coordinators",
				BackupEndpoint: "PJSIP/backup", BackupAfterCalls: 3, Retry: 2 * time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cc.RunOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(a.originated, tc.want) {
				t.Fatalf("after %d calls rang %v, want %v", tc.calls, a.originated, tc.want)
			}
		}
		cc.Handle(t.Context(), Event{Type: EventStasisStart, ChannelID: "out-2", Args: a.args[1]})
		cc.Handle(t.Context(), Event{Type: EventDTMF, ChannelID: "out-2", Digit: "1"})
		if st.acked[inc] != 7 || a.hangups("out-1") != 1 || a.hangups("out-2") != 0 {
			t.Fatalf("acknowledged %v hung up %v", st.acked, a.hungUp)
		}
	})
}

// CodeRabbit (PR #13, C1): an acknowledgement covers every (reason, role)
// pair the claim returned, so the call speaks each pair, the role after its
// reason, before a coordinator can press 1.
func TestAcknowledgementCoversOnlyAnnouncedRoles(t *testing.T) {
	a := &alertARI{}
	inc := uuid.NewV7()
	st := &coordStore{calls: []dispatch.CoordinatorCall{{IncidentID: inc, Seq: 9,
		Reasons: []string{"no_eligible_responder", "no_eligible_responder", "triage_rule_error"},
		Roles:   []string{"vht", "transporter", ""}, Reference: "1234567894", Calls: 1}}, acked: map[uuid.UUID]int64{}}
	cc, err := NewCoordinatorCalls(CoordinatorCallsConfig{ARI: a, Store: st, Endpoint: "PJSIP/coordinators", Retry: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cc.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	cc.Handle(context.Background(), Event{Type: EventStasisStart, ChannelID: "esc-1", Args: a.args[0]})
	b := "sauti/en/"
	want := []string{b + "escalation-reason-triage_rule_error", b + "escalation-reason-no_eligible_responder", b + "role-vht",
		b + "escalation-reason-no_eligible_responder", b + "role-transporter", b + "escalation-options"}
	if got := a.plays[0][len(a.plays[0])-len(want):]; !slices.Equal(got, want) {
		t.Fatalf("announced %v, want %v", got, want)
	}
	cc.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: "esc-1", Digit: "1"})
	if st.acked[inc] != 9 {
		t.Fatalf("acknowledged %v", st.acked)
	}
}

// Every reason with every role still fits the 128 characters an ARI
// argument may hold: a call that cannot be placed would never ring.
func TestEveryReasonAndRoleFitsInTheCallArguments(t *testing.T) {
	var reasons, roles []string
	for _, r := range incident.EscalationReasons {
		for _, role := range []string{"", "vht", "transporter"} {
			reasons, roles = append(reasons, r), append(roles, role)
		}
	}
	reasons, roles = append(reasons, "not_in_the_registry"), append(roles, "")
	arg := reasonPairs(reasons, roles)
	if len(arg) > 128 || !regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`).MatchString(arg) {
		t.Fatalf("%d characters: %q", len(arg), arg)
	}
	if got := len(pairsOf(arg)); got != len(incident.EscalationReasons)*3+1 {
		t.Fatalf("%d pairs read back", got)
	}
}

// Review R5: an answered backup call does not hold one of the coordinator
// line's calls (SAUTI_COORDINATOR_LINES): the line stays free for the next
// escalation.
func TestAnAnsweredBackupCallKeepsTheLineFree(t *testing.T) {
	a := &alertARI{}
	inc := uuid.NewV7()
	st := &coordStore{calls: []dispatch.CoordinatorCall{{IncidentID: inc, Seq: 7, Reasons: []string{"triage_rule_error"},
		Reference: "1234567894", Calls: 4}, {IncidentID: uuid.NewV7(), Seq: 1, Reasons: []string{"dispatch_stalled"}, Reference: "1234567894", Calls: 1}},
		acked: map[uuid.UUID]int64{}}
	cc, err := NewCoordinatorCalls(CoordinatorCallsConfig{ARI: a, Store: st, Endpoint: "PJSIP/coordinators", BackupEndpoint: "PJSIP/backup",
		BackupAfterCalls: 3, Lines: 2, Retry: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	st.calls, st.limits = st.calls[:1], nil
	queued := []dispatch.CoordinatorCall{{IncidentID: uuid.NewV7(), Seq: 1, Reasons: []string{"dispatch_stalled"}, Reference: "1234567894", Calls: 1}}
	if _, err := cc.RunOnce(context.Background()); err != nil || len(a.originated) != 2 {
		t.Fatalf("rang %v %v", a.originated, err)
	}
	cc.Handle(context.Background(), Event{Type: EventStasisStart, ChannelID: "out-2", Args: a.args[1]})
	st.calls = queued
	if _, err := cc.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(st.limits, []int{2, 1}) {
		t.Fatalf("free lines asked for %v, want [2 1]: the answered backup call took a line", st.limits)
	}
}

func tierRig(t *testing.T, a *alertARI, lines int, calls ...dispatch.CoordinatorCall) (*CoordinatorCalls, *coordStore) {
	t.Helper()
	st := &coordStore{calls: calls, acked: map[uuid.UUID]int64{}}
	cc, err := NewCoordinatorCalls(CoordinatorCallsConfig{ARI: a, Store: st, Endpoint: "PJSIP/coordinators", BackupEndpoint: "PJSIP/backup",
		BackupAfterCalls: 3, Lines: lines, Retry: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return cc, st
}

func tierCall(inc uuid.UUID, seq int64) dispatch.CoordinatorCall {
	return dispatch.CoordinatorCall{IncidentID: inc, Seq: seq, Reasons: []string{"triage_rule_error"}, Reference: "1234567894", Calls: 4}
}

// PR #14 review: a primary call answered and acknowledged before Originate
// returns (StasisStart can come first) ends the round: the backup is not
// rung for an escalation already acknowledged.
func TestAnEarlyAcknowledgementStopsTheOtherCall(t *testing.T) {
	a := &alertARI{}
	inc := uuid.NewV7()
	cc, st := tierRig(t, a, 1, tierCall(inc, 7))
	a.onOriginate = func(ch string) {
		if ch != "out-1" {
			return
		}
		a.mu.Lock()
		args := a.args[0]
		a.mu.Unlock()
		cc.Handle(context.Background(), Event{Type: EventStasisStart, ChannelID: ch, Args: args})
		cc.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: ch, Digit: "1"})
	}
	if _, err := cc.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st.acked[inc] != 7 || !slices.Equal(a.originated, []string{"PJSIP/coordinators"}) || a.hangups("out-1") != 0 {
		t.Fatalf("acknowledged %v, rang %v, hung up %v (the acknowledging call keeps its closing prompt)", st.acked, a.originated, a.hungUp)
	}
}

// PR #14 review: the acknowledgement of an older round (seq 7, on its still
// live backup call) hangs up only that round's calls; the newer round's
// (seq 9) keep ringing, as its escalation is not acknowledged.
func TestAnOldRoundsAcknowledgementLeavesTheNewRoundRinging(t *testing.T) {
	a := &alertARI{}
	inc := uuid.NewV7()
	cc, st := tierRig(t, a, 2, tierCall(inc, 7))
	if _, err := cc.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	st.calls = []dispatch.CoordinatorCall{tierCall(inc, 9)}
	if _, err := cc.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(a.originated) != 4 {
		t.Fatalf("rang %v", a.originated)
	}
	cc.Handle(context.Background(), Event{Type: EventStasisStart, ChannelID: "out-2", Args: a.args[1]})
	cc.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: "out-2", Digit: "1"})
	if st.acked[inc] != 7 || a.hangups("out-1") != 1 || a.hangups("out-3") != 0 || a.hangups("out-4") != 0 {
		t.Fatalf("acknowledged %v hung up %v", st.acked, a.hungUp)
	}
}

// PR #14 review: an answered sibling Asterisk will not hang up still holds
// its line (and keeps its no-input time) until it ends.
func TestASiblingThatCannotBeHungUpKeepsItsLine(t *testing.T) {
	a := &alertARI{failHangup: map[string]bool{"out-1": true}}
	inc := uuid.NewV7()
	cc, _ := tierRig(t, a, 1, tierCall(inc, 7))
	if _, err := cc.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	cc.Handle(context.Background(), Event{Type: EventStasisStart, ChannelID: "out-1", Args: a.args[0]})
	cc.Handle(context.Background(), Event{Type: EventStasisStart, ChannelID: "out-2", Args: a.args[1]})
	cc.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: "out-2", Digit: "1"})
	cc.mu.Lock()
	_, busy := cc.busy["out-1"]
	_, tracked := cc.calls["out-1"]
	cc.mu.Unlock()
	if !busy || !tracked {
		t.Fatalf("the sibling that is still live was released: busy %v tracked %v", busy, tracked)
	}
}
