package intake

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/pii"
)

// C1 across channels: a USSD request lost T2 after screen 7; the caller
// rings the voice line. StartIVR records T2 before the first prompt, and the
// call goes on to the callback screen and "help is coming".
func TestIVRStartRecordsALostT2(t *testing.T) {
	mem := newMemIncidents()
	r := newRigWith(t, &faultyIncidents{memIncidents: mem, failType: incident.EvDispatchReady}, mem, nil, nil, 7)
	c := r.dial(t, "S1", "0772123456")
	c.send("")
	for _, in := range []string{"1", "1", "1", "3", "2", "1"} {
		c.send(in)
	}
	if _, err := c.try("1"); !errors.Is(err, errInjected) {
		t.Fatalf("screen 7: want the injected failure, got %v", err)
	}
	v := r.callIVR(t, "call-1", "+256772123456")
	if p := v.projection(); v.call.IncidentID != c.incidentID() || p.T[2] == nil || v.last.Kind != KindCallback {
		t.Fatalf("voice call: prompt %s, T2 %v", v.last.Kind, p.T[2])
	}
	if done := v.press("1"); done.Kind != KindComplete {
		t.Fatalf("closing prompt %s", done.Kind)
	}
}

// Every write of a voice walk, failed once before or after it takes effect,
// then recovered by the caller ringing again: the incident ends classified
// and dispatched, and the closing prompt is never reached without T2.
func TestIVREveryWriteFailureConverges(t *testing.T) {
	answers := map[Kind]string{KindLanguage: "1", KindDanger: "2", KindCategory: "1", KindAgeBand: "3",
		KindSpecificDanger: "2", KindWard: "1", KindLandmark: "6", KindCallback: "1"}
	voiceWalk := func(t *testing.T, r *rig) incident.Projection {
		t.Helper()
		ctx := context.Background()
		phone, err := pii.ParsePhone("+256772123456")
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		for calls < 10 {
			calls++
			turn, err := r.svc.StartIVR(ctx, "call-"+strconv.Itoa(calls), phone)
			for err == nil && !turn.Prompt.End {
				turn, err = r.svc.AnswerIVR(ctx, turn.Call, answers[turn.Call.Shown])
			}
			if err != nil {
				if !errors.Is(err, errInjected) {
					t.Fatalf("voice call %d: %v", calls, err)
				}
				continue
			}
			p, err := r.inc.Projection(ctx, turn.Call.IncidentID)
			if err != nil {
				t.Fatal(err)
			}
			if turn.Prompt.Kind == KindComplete && p.T[2] == nil {
				t.Fatal("closing prompt without T2")
			}
			return p
		}
		t.Fatal("no voice call finished")
		return incident.Projection{}
	}
	clean := &faultyIncidents{memIncidents: newMemIncidents()}
	voiceWalk(t, newRigWith(t, clean, clean.memIncidents, nil, nil, 7))
	for n := 1; n <= clean.calls; n++ {
		for _, after := range []bool{false, true} {
			t.Run(fmt.Sprintf("write%d/after=%v", n, after), func(t *testing.T) {
				f := &faultyIncidents{memIncidents: newMemIncidents(), failAt: n, after: after}
				p := voiceWalk(t, newRigWith(t, f, f.memIncidents, nil, nil, 7))
				if p.Severity == "" || p.T[2] == nil || p.LastScreen != 8 || p.LocationQuality != incident.QualityWard {
					t.Fatalf("finished incident: %+v", p)
				}
			})
		}
	}
}
