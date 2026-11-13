package intake

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"sautihealth.com/sauti/internal/incident"
)

type suspensionFlag struct{ on atomic.Bool }

func (f *suspensionFlag) DispatchSuspended(context.Context) (bool, error) { return f.on.Load(), nil }

// O6: while dispatch is suspended, intake continues and the record is kept,
// the caller's closing message is the (pending-approval) suspended message
// with the incident number — never "help is coming" — and the incident is
// escalated to the coordinator once, with the reason the planner uses.
func TestSuspendedDispatchChangesTheClosingMessageAndEscalates(t *testing.T) {
	r := newRig(t, nil, nil)
	r.suspension.on.Store(true)
	c := r.dial(t, "S1", "0772123456")
	c.send("")
	for _, in := range []string{"1", "2", "4", "3", "2", "1", "2"} {
		if sc := c.send(in); sc.End {
			t.Fatalf("ended early at %q: %+v", in, sc)
		}
	}
	done := c.send("1")
	p := c.projection()
	if !done.End || strings.Contains(done.Text, "HELP IS COMING") || !strings.Contains(done.Text, "coordinator") ||
		!strings.Contains(done.Text, p.Reference.Spoken()) {
		t.Fatalf("closing screen while suspended = %+v", done)
	}
	if p.T[2] == nil || !p.Escalated {
		t.Fatalf("record kept and escalated? %+v", p)
	}
	c.send("1") // a retried final callback
	n := 0
	for _, e := range r.inc.allEvents() {
		var pl struct {
			Reason string `json:"reason"`
		}
		if e.Type == incident.EvEscalated && json.Unmarshal(e.Payload, &pl) == nil && pl.Reason == ReasonDispatchSuspended {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("dispatch_suspended escalations = %d, want 1", n)
	}

	r.suspension.on.Store(false)
	c2 := r.dial(t, "S2", "0772123457")
	c2.send("")
	for _, in := range []string{"1", "2", "4", "3", "2", "1", "2"} {
		c2.send(in)
	}
	if done := c2.send("1"); !strings.Contains(done.Text, "HELP IS COMING") {
		t.Fatalf("closing screen after resumption = %+v", done)
	}
}

// Review I6: a second caller about the same emergency, merged at T2 while
// dispatch is suspended, hears the suspended message with their own number —
// not an error — and the merged call itself is not escalated (its survivor
// is).
func TestMergedCallerWhileSuspendedHearsTheMessage(t *testing.T) {
	r := newRig(t, nil, nil)
	r.suspension.on.Store(true)
	walk := func(session, phone string) (Screen, *caller) {
		c := r.dial(t, session, phone)
		c.send("")
		for _, in := range []string{"1", "2", "4", "3", "2", "1", "2"} {
			c.send(in)
		}
		return c.send("1"), c
	}
	walk("S1", "0772123456")
	done, c2 := walk("S2", "0772123457")
	p := c2.projection()
	if p.State != incident.StateMerged {
		t.Fatalf("second call state %s, want merged", p.State)
	}
	if !done.End || !strings.Contains(done.Text, "coordinator") || !strings.Contains(done.Text, p.Reference.Spoken()) {
		t.Fatalf("merged caller's closing screen = %+v", done)
	}
	for _, e := range r.inc.allEvents() {
		if e.IncidentID == p.ID && e.Type == incident.EvEscalated {
			t.Fatal("the merged call itself was escalated")
		}
	}
}
