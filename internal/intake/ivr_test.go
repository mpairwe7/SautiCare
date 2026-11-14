package intake

import (
	"context"
	"slices"
	"testing"
	"time"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/pii"
)

type voiceCall struct {
	t    *testing.T
	r    *rig
	call IVRCall
	last Prompt
}

func (r *rig) callIVR(t *testing.T, callID, number string) *voiceCall {
	t.Helper()
	var p pii.Phone
	if number != "" {
		var err error
		if p, err = pii.ParsePhone(number); err != nil {
			t.Fatal(err)
		}
	}
	turn, err := r.svc.StartIVR(context.Background(), callID, p)
	if err != nil {
		t.Fatal(err)
	}
	return &voiceCall{t: t, r: r, call: turn.Call, last: turn.Prompt}
}

func (v *voiceCall) press(input string) Prompt {
	v.t.Helper()
	turn, err := v.r.svc.AnswerIVR(context.Background(), v.call, input)
	if err != nil {
		v.t.Fatalf("AnswerIVR(%s, %q): %v", v.call.Shown, input, err)
	}
	v.call, v.last = turn.Call, turn.Prompt
	return turn.Prompt
}

func (v *voiceCall) projection() incident.Projection {
	v.t.Helper()
	p, err := v.r.inc.Projection(context.Background(), v.call.IncidentID)
	if err != nil {
		v.t.Fatal(err)
	}
	return p
}

// The voice channel is functionally identical (Guide §4): the same eight
// screens, answered on the keypad, the same events and T2.
func TestIVRWalksEightScreens(t *testing.T) {
	r := newRig(t, nil, nil)
	v := r.callIVR(t, "call-1", "+256772123456")
	if v.last.Kind != KindLanguage {
		t.Fatalf("first prompt %s", v.last.Kind)
	}
	for _, d := range []string{"1", "2", "4", "3", "2", "1", "2"} {
		if pr := v.press(d); pr.End {
			t.Fatalf("ended at %q", d)
		}
	}
	done := v.press("1")
	p := v.projection()
	if !done.End || done.Kind != KindComplete || p.Channel != string(incident.ChannelIVR) || p.Severity != incident.SevYellow ||
		p.LastScreen != 8 || p.T[2] == nil || p.LocationQuality != incident.QualityLandmark {
		t.Fatalf("closing %+v, projection %+v", done, p)
	}
}

// "Another landmark" on the keypad has no free text: the incident is located
// at ward level and flagged for a location call-back (Build Prompt §10.2).
func TestIVRAnotherLandmarkNeedsCallback(t *testing.T) {
	r := newRig(t, nil, nil)
	v := r.callIVR(t, "call-1", "+256772123456")
	for _, d := range []string{"1", "2", "1", "3", "2", "1"} {
		v.press(d)
	}
	if pr := v.press("6"); pr.Kind != KindCallback {
		t.Fatalf("after another landmark: %s (no free text on voice)", pr.Kind)
	}
	p := v.projection()
	if p.Landmark != incident.LandmarkOther || p.LocationQuality != incident.QualityWard ||
		!slices.Contains(p.Flags, FlagNeedsLocationCallback) || p.T[2] == nil {
		t.Fatalf("projection = %+v", p)
	}
}

// A withheld number is treated as unknown: the IVR asks for a callback
// number, and dispatch proceeds on location (Build Prompt §10.2).
func TestIVRWithheldAsksCallback(t *testing.T) {
	r := newRig(t, nil, nil)
	v := r.callIVR(t, "call-1", "")
	for _, d := range []string{"1", "2", "1", "3", "2", "1", "1"} {
		v.press(d)
	}
	if v.last.Kind != KindCallback || len(v.last.Options) != 0 {
		t.Fatalf("withheld callback prompt = %+v", v.last)
	}
	if pr := v.press("1"); pr.Kind != KindCallback || pr.Notice == "" {
		t.Fatalf("'this number' without a number: %+v", pr)
	}
	if pr := v.press("0772999888"); !pr.End {
		t.Fatalf("typed number: %+v", pr)
	}
	if p := v.projection(); p.LastScreen != 8 || p.T[2] == nil {
		t.Fatalf("projection = %+v", p)
	}
}

// Scenario 5 (IVR part): a USSD session drops at screen 6; the caller rings
// the voice line within 10 minutes and finishes there: one incident.
func TestIVRResumesUSSDIncident(t *testing.T) {
	r := newRig(t, nil, nil)
	c := r.dial(t, "S1", "0772123456")
	c.send("")
	for _, in := range []string{"1", "2", "1", "3", "2"} {
		c.send(in)
	}
	r.inc.now = r.inc.now.Add(4 * time.Minute)
	v := r.callIVR(t, "call-9", "+256772123456")
	if v.call.IncidentID != c.incidentID() || v.last.Kind != KindWard {
		t.Fatalf("voice call: incident %s (USSD %s), prompt %s", v.call.IncidentID, c.incidentID(), v.last.Kind)
	}
	v.press("1")
	v.press("1")
	if done := v.press("1"); !done.End {
		t.Fatalf("closing = %+v", done)
	}
	if p := v.projection(); p.LastScreen != 8 || p.T[2] == nil {
		t.Fatalf("projection = %+v", p)
	}
}

// A repeated DTMF event (a re-delivered or double-pressed key on the same
// prompt) never records the answer twice.
func TestIVRRepeatedInputIsIdempotent(t *testing.T) {
	r := newRig(t, nil, nil)
	v := r.callIVR(t, "call-1", "+256772123456")
	before := v.call
	v.press("1")
	n := len(r.inc.allEvents())
	if _, err := r.svc.AnswerIVR(context.Background(), before, "1"); err != nil {
		t.Fatal(err)
	}
	if len(r.inc.allEvents()) != n {
		t.Fatalf("events %d → %d on a repeated answer", n, len(r.inc.allEvents()))
	}
}

// A voice hand-over records one escalation for the call, however often the
// controller asks (a retry after a failed Continue), so the coordinator line
// rings even if the dialplan's transfer fails. The call carries the
// reference the coordinator is shown.
func TestHandOverEscalatesOncePerCall(t *testing.T) {
	r := newRig(t, nil, nil)
	v := r.callIVR(t, "call-1", "+256772123456")
	v.press("1")
	if _, err := incident.ParseReference(string(v.call.Reference)); err != nil {
		t.Fatalf("call reference %q: %v", v.call.Reference, err)
	}
	for range 2 {
		if err := r.svc.HandOver(context.Background(), v.call, incident.ReasonVoiceHandedOver); err != nil {
			t.Fatal(err)
		}
	}
	var reasons []string
	for _, e := range r.inc.allEvents() {
		if e.Type == incident.EvEscalated {
			reasons = append(reasons, string(e.Payload))
		}
	}
	if !slices.Equal(reasons, []string{`{"reason":"voice_handed_over"}`}) {
		t.Fatalf("escalations %v", reasons)
	}
	if !v.projection().Escalated {
		t.Fatal("incident not escalated")
	}
	// A call with no incident (intake never opened one) has nothing to escalate.
	if err := r.svc.HandOver(context.Background(), IVRCall{CallID: "call-2"}, incident.ReasonVoiceHandedOver); err != nil {
		t.Fatal(err)
	}
}

// Task 1.2 amendment: a call adopted after a restart carries only its call
// ID (the channel). HandOver finds its incident from the call's voice
// session; a call with no session has nothing to escalate.
func TestHandOverFindsTheIncidentByCallID(t *testing.T) {
	r := newRig(t, nil, nil)
	v := r.callIVR(t, "ch9", "+256772123456")
	if err := r.svc.HandOver(context.Background(), IVRCall{CallID: "ch9"}, incident.ReasonVoiceHandedOver); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.HandOver(context.Background(), IVRCall{CallID: "nobody"}, incident.ReasonVoiceHandedOver); err != nil {
		t.Fatal(err)
	}
	var on []string
	for _, e := range r.inc.allEvents() {
		if e.Type == incident.EvEscalated {
			on = append(on, e.IncidentID.String())
		}
	}
	if !slices.Equal(on, []string{v.call.IncidentID.String()}) {
		t.Fatalf("escalations on %v, want one on %s", on, v.call.IncidentID)
	}
}
