//go:build integration

package wiring_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/ari"
	"sautihealth.com/sauti/internal/platform/ivr"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/sim"
	"sautihealth.com/sauti/internal/responder"
)

// Scenario 9: USSD push is disabled (O2), so the SMS goes at once; nobody
// answers it; 20 s later the responder's phone rings (voice), and they
// accept by pressing 1. Every attempt is logged per channel.
func TestScenario9VoiceAt20Seconds(t *testing.T) {
	r := newEngineRig(t, nil)
	vht := r.responder(t, responder.RoleVHT)
	id := r.incident(t, incident.SevGreen)
	for i, a := range [][2]string{{"ward", "w01"}, {"landmark", "w01_market"}} {
		if _, err := r.intake.Append(context.Background(), incident.AppendParams{IncidentID: id, Type: incident.EvScreenAnswered,
			Payload: fmt.Appendf(nil, `{"screen":%d,"field":%q,"value":%q}`, 6+i, a[0], a[1]), ActorKind: incident.ActorSystem,
			IdemKey: "t:answer:" + a[0]}); err != nil {
			t.Fatal(err)
		}
	}
	snap := r.mustProcess(t, id)
	alert := alertsOf(snap, responder.RoleVHT)[0]
	if r.deliveries.count(alert.ID, dispatch.PurposeAlert) != 1 {
		t.Fatal("no SMS at T0")
	}
	pwb := make([]byte, 16)
	_, _ = rand.Read(pwb)
	pw := hex.EncodeToString(pwb)
	fake := sim.NewFakeARI("sauti", pw)
	defer fake.Close()
	fake.SetPlaybackDuration(-1) // prompts play until the test finishes them
	client, err := ari.New(ari.Config{BaseURL: fake.URL(), User: "sauti", Password: pw, App: "sauti-alerts", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	calls, err := ivr.NewAlertCalls(ivr.AlertCallsConfig{ARI: client, Store: r.ivr, Opener: r.ids.Env, Dial: "PJSIP/{number}@sauti-trunk",
		After: 20 * time.Second, Copy: r.cfg.Responder, Gazetteer: r.gaz})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = client.Events(ctx, func(e ari.Event) { calls.Handle(ctx, e) }) }()
	<-fake.Connected()

	if n, err := calls.RunOnce(ctx); err != nil || n != 0 {
		t.Fatalf("voice before 20 s: %d %v", n, err)
	}
	if _, err := r.c.Owner(t, pg.StoreOperational).Exec(ctx, "UPDATE op.alerts SET sent_at = sent_at - interval '21 seconds' WHERE id = $1", alert.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := calls.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("voice at 20 s: %d %v", n, err)
	}
	o := fake.Originated()
	if len(o) != 1 || o[0].Get("endpoint") != "PJSIP/+256772000301@sauti-trunk" {
		t.Fatalf("dialled %v", o)
	}
	ch := "out-1"
	fake.Answered(ch, "alert", o[0].Get("appArgs")[len("alert,"):])
	deadline := time.Now().Add(3 * time.Second)
	for len(fake.Plays(ch)) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	fake.Press(ch, "1")
	for len(fake.Plays(ch)) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	// The result says where to go (C1) and is heard before the hang-up (I2).
	plays := fake.Plays(ch)
	if len(plays) < 2 || !slices.Contains(plays[1], "sauti/en/alert-accepted") || !slices.ContainsFunc(plays[1], func(s string) bool {
		return strings.HasPrefix(s, "sauti/en/ward-")
	}) || !slices.Contains(plays[1], "sauti/en/landmark-w01_market") {
		t.Fatalf("accept result %v", plays)
	}
	time.Sleep(50 * time.Millisecond)
	if fake.HungUp(ch) {
		t.Fatal("hung up before the result was heard")
	}
	fake.FinishPlayback(fake.Playing(ch))
	for !fake.HungUp(ch) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !fake.HungUp(ch) {
		t.Fatal("not hung up after the result was heard")
	}
	s, err := r.store.DispatchSnapshot(ctx, id)
	if err != nil || s.Assigned[responder.RoleVHT] != vht {
		t.Fatalf("accepted by voice? assigned %v %v", s.Assigned, err)
	}
	rows, err := r.c.Owner(t, pg.StoreOperational).Query(ctx, "SELECT channel, status FROM op.alert_attempts WHERE alert_id = $1 ORDER BY claimed_at", alert.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var ch, st string
		_ = rows.Scan(&ch, &st)
		got = append(got, ch+":"+st)
	}
	if len(got) != 2 || got[0] != "sms:sent" || got[1] != "voice:sent" {
		t.Fatalf("attempts per channel %v", got)
	}
}
