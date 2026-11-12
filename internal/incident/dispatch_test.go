package incident

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	alertA = "0195f3a2-7c1e-7d4b-9a10-000000000001"
	respA  = "0195f3a2-7c1e-7d4b-9a10-000000000002"
)

func issued(role string) string {
	return `{"alert":"` + alertA + `","responder":"` + respA + `","role":"` + role + `","ring":1,"wave":1}`
}

// dispatching folds the fixture up to T2.
func dispatching(t *testing.T) Projection {
	t.Helper()
	p, err := Fold(FixtureLifecycle()[:4])
	if err != nil || p.State != StateDispatching {
		t.Fatalf("fixture: %v %v", p.State, err)
	}
	return p
}

// Dispatch facts carry exactly their identifiers and codes (Build Prompt §9
// Dispatch): never a name, number or free text. Go mirrors the SQL checks in
// migration 0015 exactly.
func TestDispatchEventPayloads(t *testing.T) {
	good := map[EventType][]string{
		EvAlertIssued:         {issued("vht"), issued("transporter")},
		EvAlertDeclined:       {`{"alert":"` + alertA + `","responder":"` + respA + `"}`, `{"alert":"` + alertA + `","responder":"` + respA + `","reason":"bike_or_fuel"}`},
		EvAlertNoResponse:     {`{"alert":"` + alertA + `"}`},
		EvAlertStoodDown:      {`{"alert":"` + alertA + `"}`},
		EvForwarded:           {`{"facility":"hc_test_1"}`},
		EvForwardAcknowledged: {`{"facility":"hc_test_1"}`},
		EvForwardTimedOut:     {`{"facility":"hc_test_1"}`},
		EvCallerNotified:      {`{"kind":"helper_on_the_way"}`, `{"kind":"forwarded_to_facility","facility":"hc_test_1"}`},
		EvEscalated:           {`{"reason":"no_eligible_responder"}`, `{"reason":"triage_rule_error","role":"vht"}`},
	}
	bad := map[EventType][]string{
		EvAlertIssued: {`{"role":"vht"}`, strings.Replace(issued("vht"), `"ring":1`, `"ring":0`, 1),
			strings.Replace(issued("vht"), `"wave":1`, `"wave":"1"`, 1), issued("doctor"),
			strings.Replace(issued("vht"), `}`, `,"phone":"+256772000123"}`, 1),
			strings.Replace(issued("vht"), alertA, "not-a-uuid", 1)},
		EvAlertDeclined:       {`{"alert":"` + alertA + `"}`, `{"alert":"` + alertA + `","responder":"` + respA + `","reason":"My bike broke"}`},
		EvAlertNoResponse:     {`{}`, `{"alert":"` + alertA + `","reason":"x"}`},
		EvAlertStoodDown:      {`{"alert":42}`},
		EvForwarded:           {`{}`, `{"facility":"Kawempe HC"}`},
		EvForwardAcknowledged: {`{"facility":""}`},
		EvForwardTimedOut:     {`{"facility":"hc_1","extra":1}`},
		EvCallerNotified:      {`{"kind":"sms"}`, `{"kind":"forwarded_to_facility"}`, `{"kind":"helper_on_the_way","facility":"hc_1"}`},
		EvEscalated:           {`{}`, `{"reason":"Caller sounded distressed"}`, `{"reason":"x","role":"doctor"}`},
	}
	for typ, payloads := range good {
		for _, pl := range payloads {
			if _, err := Apply(dispatching(t), ev(5, typ, pl)); err != nil {
				t.Errorf("%s %s: %v", typ, pl, err)
			}
		}
	}
	for typ, payloads := range bad {
		for _, pl := range payloads {
			if _, err := Apply(dispatching(t), ev(5, typ, pl)); !errors.Is(err, ErrIllegalTransition) {
				t.Errorf("%s %s accepted (%v)", typ, pl, err)
			}
		}
	}
}

// Each role searches on its own: the VHT may be on scene while the
// transporter role is still being alerted and accepts.
func TestAlertsContinueOnScene(t *testing.T) {
	p := dispatching(t)
	var err error
	for i, e := range []Event{
		ev(5, EvAlertIssued, issued("vht")),
		ev(6, EvAccepted, `{"alert":"`+alertA+`","responder":"`+respA+`","role":"vht"}`),
		ev(7, EvArrived, ""),
		ev(8, EvAlertIssued, issued("transporter")),
		ev(9, EvAccepted, `{"alert":"`+alertA+`","responder":"`+respA+`","role":"transporter"}`),
	} {
		if p, err = Apply(p, e); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
	}
	if p.State != StateOnScene || p.T[3] == nil || p.T[4] == nil || !p.T[4].Equal(t0.Add(6*time.Second)) {
		t.Fatalf("state %s T3 %v T4 %v", p.State, p.T[3], p.T[4])
	}
}

func TestAcceptedPayload(t *testing.T) {
	for _, pl := range []string{`{"role":"vht"}`, `{"alert":"` + alertA + `","responder":"` + respA + `","role":"boda"}`} {
		if _, err := Apply(dispatching(t), ev(5, EvAccepted, pl)); !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("%s accepted", pl)
		}
	}
}
