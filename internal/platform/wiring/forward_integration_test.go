//go:build integration

package wiring_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/ari"
	"sautihealth.com/sauti/internal/platform/ivr"
	"sautihealth.com/sauti/internal/platform/sim"
)

// outbound is ivr-controller's outbound side over a fake Asterisk.
type outbound struct {
	fake  *sim.FakeARI
	calls ivr.Outbound
}

func newOutbound(t *testing.T, r *engineRig) *outbound {
	t.Helper()
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	pw := hex.EncodeToString(b)
	fake := sim.NewFakeARI("sauti", pw)
	t.Cleanup(fake.Close)
	client, err := ari.New(ari.Config{BaseURL: fake.URL(), User: "sauti", Password: pw, App: "sauti-alerts", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	fw, err := ivr.NewForwardCalls(ivr.ForwardCallsConfig{ARI: client, Store: r.ivr, Dial: "PJSIP/{number}@sauti-trunk", Gazetteer: r.gaz})
	if err != nil {
		t.Fatal(err)
	}
	cc, err := ivr.NewCoordinatorCalls(ivr.CoordinatorCallsConfig{ARI: client, Store: r.ivr, Endpoint: "PJSIP/coordinators"})
	if err != nil {
		t.Fatal(err)
	}
	o := &outbound{fake: fake, calls: ivr.Outbound{ARI: client, Forwards: fw, Coordinator: cc}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = client.Events(ctx, func(e ari.Event) { o.calls.Handle(ctx, e) }) }()
	<-fake.Connected()
	return o
}

// answerAndPress answers the n-th originated call and presses digit, then
// waits for the hang-up.
func (o *outbound) answerAndPress(t *testing.T, n int, digit string) {
	t.Helper()
	q := o.fake.Originated()[n]
	ch := "out-" + strconv.Itoa(n+1)
	o.fake.Answered(ch, strings.Split(q.Get("appArgs"), ",")...)
	deadline := time.Now().Add(3 * time.Second)
	for len(o.fake.Plays(ch)) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	o.fake.Press(ch, digit)
	for !o.fake.HungUp(ch) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !o.fake.HungUp(ch) {
		t.Fatalf("call %s was not completed", ch)
	}
}

func amended(b *config.Bundle) { b.Dispatch.Mode = dispatch.ModeAmended }

// Scenario 7 (amended mode): nobody accepts; rings widen every 60 s to
// 2.5 km; at 5 minutes the nearest health centre with forwarding gets its
// SMS and a call, acknowledges with 1; the coordinator line rings; the
// caller gets the SMS naming the centre.
func TestScenario7ForwardAcknowledged(t *testing.T) {
	r := newEngineRig(t, amended)
	o := newOutbound(t, r)
	ctx := context.Background()
	id := r.incident(t, incident.SevYellow)
	r.mustProcess(t, id)
	r.age(t, id, 5*time.Minute+time.Second)
	snap := r.mustProcess(t, id)
	if len(snap.Forwards) != 1 || !dispatch.HasEscalation(snap.Escalations, dispatch.ReasonForwarded) {
		t.Fatalf("at 5 minutes: forwards %+v escalations %v", snap.Forwards, snap.Escalations)
	}
	centre := snap.Forwards[0].FacilityID
	if !slices.Equal(r.deliveries.facilities, []string{centre}) {
		t.Fatalf("facility SMS %v", r.deliveries.facilities)
	}
	if n, err := o.calls.Forwards.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("facility call %d %v", n, err)
	}
	o.answerAndPress(t, 0, "1")
	snap = r.mustProcess(t, id)
	if !snap.Forwards[0].Acked || !slices.Contains(snap.CallerNotified, dispatch.CallerForwarded) {
		t.Fatalf("after acknowledgement %+v", snap)
	}
	if !slices.ContainsFunc(r.deliveries.notices, func(n dispatch.Notice) bool { return n.Kind == dispatch.CallerForwarded }) {
		t.Fatalf("caller SMS %+v", r.deliveries.notices)
	}
	if n, err := o.calls.Coordinator.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("coordinator rung %d %v", n, err)
	}
	r.noDrift(t)
}

// Scenario 8: the first centre does not acknowledge within 60 s; the next
// nearest gets its SMS and call and acknowledges.
func TestScenario8NextCentre(t *testing.T) {
	r := newEngineRig(t, amended)
	o := newOutbound(t, r)
	ctx := context.Background()
	id := r.incident(t, incident.SevYellow)
	r.mustProcess(t, id)
	r.age(t, id, 5*time.Minute+time.Second)
	r.mustProcess(t, id)
	if _, err := o.calls.Forwards.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	r.age(t, id, 61*time.Second)
	snap := r.mustProcess(t, id)
	if len(snap.Forwards) != 2 || !snap.Forwards[0].TimedOut || len(r.deliveries.facilities) != 2 {
		t.Fatalf("after 60 s: forwards %+v facility SMS %v", snap.Forwards, r.deliveries.facilities)
	}
	if n, err := o.calls.Forwards.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("next centre called %d %v", n, err)
	}
	o.answerAndPress(t, 1, "1")
	snap = r.mustProcess(t, id)
	if !snap.Forwards[1].Acked || !slices.Contains(snap.CallerNotified, dispatch.CallerForwarded) {
		t.Fatalf("after the next centre acknowledged %+v", snap)
	}
}

// Scenario 11: a triage rule error makes the incident red and escalated
// (M1); the coordinator line now rings, and a coordinator acknowledges.
func TestScenario11RuleErrorRingsTheCoordinator(t *testing.T) {
	r := newEngineRig(t, nil)
	o := newOutbound(t, r)
	ctx := context.Background()
	id := r.incident(t, incident.SevRed)
	if _, err := r.intake.Append(ctx, incident.AppendParams{IncidentID: id, Type: incident.EvEscalated,
		Payload: []byte(`{"reason":"triage_rule_error"}`), ActorKind: incident.ActorSystem, IdemKey: "rule-error"}); err != nil {
		t.Fatal(err)
	}
	if n, err := o.calls.Coordinator.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("rang %d %v", n, err)
	}
	q := o.fake.Originated()
	if len(q) != 1 || q[0].Get("endpoint") != "PJSIP/coordinators" || !strings.Contains(q[0].Get("appArgs"), "triage_rule_error") {
		t.Fatalf("coordinator call %v", q)
	}
	o.answerAndPress(t, 0, "1")
	if n, _ := o.calls.Coordinator.RunOnce(ctx); n != 0 {
		t.Fatal("rang again after acknowledgement")
	}
}
