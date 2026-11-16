package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// called is a call reported by Caller A at t0, still looking for help.
func called(t0 time.Time, state string) board {
	return board{Incidents: []boardIncident{{ID: "0190f0c4-0000-7000-8000-000000000002", Reference: "284 262 7491", State: state,
		T: map[string]string{"T1": t0.Format(time.RFC3339Nano)}}}}
}

func asks(steps []arrivalStep) (n int) {
	for _, s := range steps {
		if s.ask {
			n++
		}
	}
	return n
}

// The caller is asked one interval after the call; an ask left unanswered
// lapses after the window and the next comes one interval after it; after a
// "no" the next comes one interval after the answer; a "yes" stops the
// asking and bounds the arrival between the last "no" and the "yes".
func TestTheCallerIsAskedUntilHelpArrives(t *testing.T) {
	t0 := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	b := called(t0, "assigned")
	id := b.Incidents[0].ID
	r := newArrivals()
	if got := r.tick(b, t0.Add(10*time.Minute)); len(got) != 0 {
		t.Fatalf("a call nobody reported on screen was asked: %+v", got)
	}
	r = newArrivals()
	r.reported("284 262 7491", "caller-a")
	at := func(d time.Duration) []arrivalStep { return r.tick(b, t0.Add(d)) }
	if n := asks(at(4*time.Minute + 59*time.Second)); n != 0 {
		t.Fatal("asked before 5 minutes")
	}
	steps := at(5 * time.Minute)
	if len(steps) != 1 || !steps[0].ask || steps[0].phone != "caller-a" || steps[0].ref != "284 262 7491" {
		t.Fatalf("at 5 minutes: %+v", steps)
	}
	r.opened(id, "tok1")
	if got := at(6 * time.Minute); len(got) != 0 {
		t.Fatalf("asked again while the first is open: %+v", got)
	}
	if got := at(7 * time.Minute); len(got) != 1 || !got[0].lapsed || got[0].token != "tok1" {
		t.Fatalf("the unanswered ask did not lapse after the window: %+v", got)
	}
	if n := asks(at(9*time.Minute + 59*time.Second)); n != 0 {
		t.Fatal("asked again before an interval after the lapsed ask")
	}
	if n := asks(at(10 * time.Minute)); n != 1 {
		t.Fatal("not asked again an interval after the lapsed ask")
	}
	if _, err := r.answer(id, "caller-b", answerNo, viaUSSD, t0.Add(10*time.Minute)); !errors.Is(err, errNoAsk) {
		t.Fatalf("another phone answered: %v", err)
	}
	no := t0.Add(10*time.Minute + 30*time.Second)
	if _, err := r.answer(id, "caller-a", answerNo, viaApp, no); err != nil {
		t.Fatal(err)
	}
	if n := asks(at(15 * time.Minute)); n != 0 {
		t.Fatal("asked again before an interval after the no")
	}
	if n := asks(at(15*time.Minute + 30*time.Second)); n != 1 {
		t.Fatal("not asked again an interval after the no")
	}
	if v := r.views(b); len(v) != 1 || v[0].Open == nil || !strings.Contains(v[0].Open.Text, "has a health worker arrived yet?\n1 Yes\n2 No") {
		t.Fatalf("the open ask's view: %+v", v)
	}
	yes := t0.Add(16 * time.Minute)
	if _, err := r.answer(id, "", answerYes, viaUSSD, yes); err != nil {
		t.Fatal(err)
	}
	if n := asks(at(time.Hour)); n != 0 {
		t.Fatal("asked again after the yes")
	}
	v := r.views(b)[0]
	if v.ConfirmedAt == nil || !v.ConfirmedAt.Equal(yes) || v.After == nil || !v.After.Equal(no) || v.Next != nil || v.Open != nil {
		t.Fatalf("after the yes: %+v", v)
	}
	want := []string{answerNone, answerNo, answerYes}
	for i, a := range v.Asks {
		if a.Answer != want[i] {
			t.Fatalf("answers %+v, want %v", v.Asks, want)
		}
	}
}

// An incident that closes or merges is no longer asked, and its open ask's
// session closes; a faster pace takes effect for the next ask.
func TestTheAskingFollowsTheIncidentAndThePace(t *testing.T) {
	t0 := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	r := newArrivals()
	r.reported("284 262 7491", "caller-a")
	if r.setPace(45*time.Second) || !r.setPace(30*time.Second) {
		t.Fatal("the paces offered are 5 minutes, 1 minute and 30 seconds")
	}
	b := called(t0, "dispatching")
	if n := asks(r.tick(b, t0.Add(30*time.Second))); n != 1 {
		t.Fatal("not asked at the 30-second pace")
	}
	r.opened(b.Incidents[0].ID, "tok")
	got := r.tick(called(t0, "closed"), t0.Add(31*time.Second))
	if len(got) != 1 || got[0].ask || got[0].token != "tok" {
		t.Fatalf("closing: %+v", got)
	}
	if n := asks(r.tick(called(t0, "closed"), t0.Add(time.Hour))); n != 0 {
		t.Fatal("a closed incident was asked")
	}
}

// A "no" tells each responder on the way; for a role nobody accepted it
// wakes dispatch, reminds whoever still holds an alert for it, and alerts a
// coordinator only when nobody is being asked and none has it yet.
func TestANoPressesTheSearchWithinTheProtocol(t *testing.T) {
	_, _, phones := shareRig(t)
	inc := both("assigned").Incidents[0]
	p := pressFor(inc, phones)
	if len(p.sms) != 2 || p.wake || len(p.escalate) != 0 || !p.told || p.harder {
		t.Fatalf("both on the way: %+v", p)
	}
	if got := texts(p.sms, vht1); len(got) != 1 || got[0] != "PENDING-SMS Sauti 284 262 7491: the caller says help has not arrived yet and is still waiting." {
		t.Fatalf("VHT 1: %q", got)
	}

	inc.State = "dispatching"
	inc.Alerts = []boardAlert{{Responder: "VHT 2", Role: "vht", Outcome: "pending"}, {Responder: "VHT 3", Role: "vht", Outcome: "declined"},
		{Responder: "Transporter 2", Role: "transporter", Outcome: "no_response"}}
	p = pressFor(inc, phones)
	if !p.wake || !p.harder || p.told || len(p.escalate) != 0 || len(p.sms) != 2 {
		t.Fatalf("alerts still out: %+v", p)
	}
	if got := texts(p.sms, "+256772000202"); len(got) != 1 || !strings.Contains(got[0], "the caller is still waiting and nobody has taken this. Dial *384*2# to accept.") {
		t.Fatalf("VHT 2's reminder: %q", got)
	}
	if got := texts(p.sms, "+256772000205"); len(got) != 1 {
		t.Fatalf("Transporter 2, whose lapsed alert can still be accepted, was not reminded: %v", p.sms)
	}

	inc.Alerts = []boardAlert{{Responder: "VHT 1", Role: "vht", Outcome: "accepted"}}
	p = pressFor(inc, phones)
	if len(p.escalate) != 1 || p.escalate[0] != "transporter" || !p.coord || !p.told || !p.wake {
		t.Fatalf("nobody asked for the transporter: %+v", p)
	}
	inc.Escalations = []boardEscalation{{Reason: "red_no_acceptance", Role: "transporter"}}
	if p = pressFor(inc, phones); len(p.escalate) != 0 || !p.coord {
		t.Fatalf("a coordinator already has the transporter: %+v", p)
	}
	for _, every := range arrivalPaces {
		s := callerSorry(pressed{told: true, harder: true, coord: true}, every)
		if n := utf8.RuneCountInString(s); n > 160 {
			t.Errorf("%d characters: %q", n, s)
		}
	}
	if s := callerSorry(p, 5*time.Minute); s != "PENDING-MD-PI Sorry. Sauti has told your helpers you are still waiting. Sauti is looking harder for help. A coordinator knows. We will ask again in 5 min." {
		t.Errorf("%q", s)
	}
	if n := utf8.RuneCountInString(arrivalQuestion("284 262 7491")); n > 160 {
		t.Errorf("the question is %d characters", n)
	}
}

// Before the first ask the board sends an empty list, not null: the page
// walks it (a null once stopped the activity feed from drawing).
func TestABoardCheckListsNoAsksAsEmpty(t *testing.T) {
	t0 := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	r := newArrivals()
	r.reported("284 262 7491", "caller-a")
	b := called(t0, "dispatching")
	r.tick(b, t0.Add(time.Second))
	raw, err := json.Marshal(r.views(b))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"asks":[]`) || !strings.Contains(string(raw), `"next":`) {
		t.Fatalf("%s", raw)
	}
}
