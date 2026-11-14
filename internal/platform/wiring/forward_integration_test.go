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
	"sautihealth.com/sauti/internal/platform/pg"
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
	fake.SetPlaybackDuration(-1) // prompts play until the test finishes them
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
	select {
	case <-fake.Connected():
	case <-time.After(5 * time.Second):
		t.Fatal("the fake Asterisk never saw the event stream connect")
	}
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
	for len(o.fake.Plays(ch)) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if o.fake.HungUp(ch) {
		t.Fatalf("call %s hung up before its result was heard", ch)
	}
	o.fake.FinishPlayback(o.fake.Playing(ch))
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
	// The (reason, role) pairs travel as registry ranks and role codes:
	// triage_rule_error, no role, is "0-".
	if len(q) != 1 || q[0].Get("endpoint") != "PJSIP/coordinators" || strings.Split(q[0].Get("appArgs"), ",")[3] != "0-" {
		t.Fatalf("coordinator call %v", q)
	}
	o.answerAndPress(t, 0, "1")
	var calls int
	var acked bool
	if err := r.c.Owner(t, pg.StoreOperational).QueryRow(ctx, `SELECT calls, acknowledged_at IS NOT NULL FROM op.coordinator_calls
		WHERE incident_id = $1`, id).Scan(&calls, &acked); err != nil || calls != 0 || !acked {
		t.Fatalf("acknowledgement not recorded: calls %d acknowledged %v %v", calls, acked, err)
	}
	if left, _ := r.ivr.ClaimCoordinatorCalls(ctx, 0, 10); len(left) != 0 {
		t.Fatalf("still due after acknowledgement: %+v", left)
	}
}
