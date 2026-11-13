//go:build integration

package wiring_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/platform/ari"
	"sautihealth.com/sauti/internal/platform/ivr"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/sim"
	"sautihealth.com/sauti/internal/responder"
)

// incidentFrom opens a dispatched incident whose session carries the
// caller's number really sealed (bound to its session row) and its lookup
// MAC, as intake records them.
func (r *engineRig) incidentFrom(t *testing.T, sev incident.Severity, caller pii.Phone) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	session := uuid.NewV7()
	sealed, err := r.ids.SealPhone(ctx, caller, crypto.AAD{Store: "operational", Table: "sessions", Column: "phone", RowID: session.String()})
	if err != nil {
		t.Fatal(err)
	}
	mac, err := r.ids.PhoneMAC(ctx, caller)
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := incident.NewReference(rand.Reader)
	o, err := r.intake.OpenIncident(ctx, incident.OpenParams{Reference: ref, Channel: incident.ChannelUSSDAT, SessionID: session,
		ProviderSessionID: "ATUid_" + uuid.NewV4().String(), NetworkCode: "64110", IdemKey: "ussd:at:start", Phone: sealed, PhoneMAC: mac})
	if err != nil {
		t.Fatal(err)
	}
	stage, _ := r.gaz.Stage("w01_stage_a")
	for _, e := range []incident.AppendParams{
		{Type: incident.EvClassified, Payload: []byte(`{"severity":"` + string(sev) + `","category":"trauma"}`), LogicVersion: "tool1-test+sha256:000000000000"},
		{Type: incident.EvDispatchReady, Payload: fmt.Appendf(nil, `{"zone":{"col":%d,"row":%d},"location_quality":"landmark"}`, stage.Cell.Col, stage.Cell.Row)},
	} {
		e.IncidentID, e.ActorKind, e.IdemKey = o.IncidentID, incident.ActorSystem, "t:"+string(e.Type)
		if _, err := r.intake.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	return o.IncidentID
}

// Scenario 1 to acceptance, through the channels (Build Prompt §16): a red
// trauma call — the top 3 of each role alerted in parallel by SMS; the first
// VHT to accept by the menu wins, a later accept hears "already taken", the
// rest are stood down by SMS; the caller is told help is on the way. Then
// the VHT calls the bridge number and reaches the caller through Sauti's
// number (§10.5): the caller's number is dialled on the trunk only — never
// shown to the VHT, never in a log line — and the call is logged.
func TestScenario1RedTraumaThroughTheChannels(t *testing.T) {
	r := newEngineRig(t, nil)
	ctx := context.Background()
	var vhts []pii.Phone
	for range 4 {
		r.responder(t, responder.RoleVHT)
		vhts = append(vhts, phoneOf(t, r.phones))
	}
	r.responder(t, responder.RoleTransporter)
	tr := phoneOf(t, r.phones)
	caller, _ := pii.ParsePhone("+256772000777")
	id := r.incidentFrom(t, incident.SevRed, caller)

	snap := r.mustProcess(t, id)
	vhtAlerts := alertsOf(snap, responder.RoleVHT)
	if len(vhtAlerts) != 3 || len(alertsOf(snap, responder.RoleTransporter)) != 1 {
		t.Fatalf("red parallel alerts: %d VHT, %d transporter", len(vhtAlerts), len(alertsOf(snap, responder.RoleTransporter)))
	}
	for _, a := range vhtAlerts {
		if r.deliveries.count(a.ID, dispatch.PurposeAlert) != 1 {
			t.Fatalf("alert %v not sent by SMS once", a.ID)
		}
	}
	// The alerted VHTs' phones: the first to accept wins.
	menu := r.menu(t, false)
	var alerted []pii.Phone
	for _, p := range vhts {
		if sc := dialMenu(t, menu, p, ""); strings.Contains(sc.Text, "1 Accept") {
			alerted = append(alerted, p)
		}
	}
	if len(alerted) != 3 {
		t.Fatalf("%d VHTs see the alert in their menu", len(alerted))
	}
	winner := alerted[1]
	if sc := dialMenu(t, menu, winner, "1"); !strings.Contains(sc.Text, "Accepted") {
		t.Fatalf("first accept %q", sc.Text)
	}
	if sc := dialMenu(t, menu, alerted[0], "1"); !strings.Contains(sc.Text, "Already taken") {
		t.Fatalf("late accept %q", sc.Text)
	}
	if sc := dialMenu(t, menu, tr, "1"); !strings.Contains(sc.Text, "Accepted") {
		t.Fatalf("transporter accept %q", sc.Text)
	}
	snap = r.mustProcess(t, id)
	stood := 0
	for _, a := range alertsOf(snap, responder.RoleVHT) {
		if a.Outcome == dispatch.OutcomeStoodDown {
			stood++
			if r.deliveries.count(a.ID, dispatch.PurposeStandDown) != 1 {
				t.Fatalf("stand-down of %v not sent once", a.ID)
			}
		}
	}
	if stood != 2 || len(snap.Assigned) != 2 {
		t.Fatalf("stood down %d, assigned %v", stood, snap.Assigned)
	}
	if !slices.ContainsFunc(r.deliveries.notices, func(n dispatch.Notice) bool { return n.Kind == dispatch.CallerHelperOnTheWay }) {
		t.Fatalf("caller not told help is on the way: %+v", r.deliveries.notices)
	}

	// The masked bridge.
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	pw := hex.EncodeToString(b)
	fake := sim.NewFakeARI("sauti", pw)
	defer fake.Close()
	fake.SetPlaybackDuration(-1)
	client, err := ari.New(ari.Config{BaseURL: fake.URL(), User: "sauti", Password: pw, App: "sauti-alerts", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	const sauti = "+256800100100"
	br, err := ivr.NewBridge(ivr.BridgeConfig{ARI: client, Store: r.ivr, Keys: r.ids, Opener: r.ids.Env, Dial: "PJSIP/{number}@sauti-trunk",
		CallerID: sauti, Log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	if err != nil {
		t.Fatal(err)
	}
	out := ivr.Outbound{ARI: client, Bridge: br}
	ectx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = client.Events(ectx, func(e ari.Event) { out.Handle(ectx, e) }) }()
	select {
	case <-fake.Connected():
	case <-time.After(5 * time.Second):
		t.Fatal("the fake Asterisk never saw the event stream connect")
	}
	fake.CallIn("in-1", winner.E164(), "bridge")
	deadline := time.Now().Add(5 * time.Second)
	for len(fake.Originated()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	o := fake.Originated()
	if len(o) != 1 || o[0].Get("endpoint") != "PJSIP/"+caller.E164()+"@sauti-trunk" || o[0].Get("callerId") != sauti {
		t.Fatalf("the caller's leg: %v", o)
	}
	args := strings.Split(o[0].Get("appArgs"), ",")
	fake.Answered("out-1", args...)
	bridgeID := "bridge-" + args[1]
	for len(fake.Bridged(bridgeID)) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := fake.Bridged(bridgeID); !slices.Equal(got, []string{"in-1", "out-1"}) {
		t.Fatalf("bridged %v", got)
	}
	if got := fake.CallerID("in-1"); got != sauti {
		t.Fatalf("the VHT's leg presents %q, want Sauti's number before it joins the bridge", got)
	}
	fake.Emit(map[string]any{"type": "StasisEnd", "channel": map[string]any{"id": "in-1"}})
	for !fake.HungUp("out-1") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !fake.HungUp("out-1") || !fake.BridgeDestroyed(bridgeID) {
		t.Fatal("the caller's leg outlived the VHT's")
	}
	var by, result string
	if err := r.c.Owner(t, pg.StoreOperational).QueryRow(ctx, "SELECT initiated_by, result FROM op.bridge_calls WHERE incident_id = $1", id).
		Scan(&by, &result); err != nil || by != dispatch.BridgeByVHT || result != dispatch.BridgeConnected {
		t.Fatalf("bridge call logged as %q %q %v", by, result, err)
	}
	for _, n := range []string{caller.E164(), winner.E164(), "0772000777", "772000777", strings.Replace(caller.E164(), "+", "%2B", 1)} {
		if strings.Contains(logs.String(), n) {
			t.Fatalf("a number reached the bridge's logs: %s", logs.String())
		}
	}
	r.noDrift(t)
}

// Scenario 2 through the channels: a yellow obstetric call cascades one VHT
// at a time — the first lets it time out (90 s), the second declines by the
// menu, the third accepts by the menu — while the transporter accepts at
// once; every alert is sent once and the caller is told help is on the way.
func TestScenario2YellowObstetricThroughTheChannels(t *testing.T) {
	r := newEngineRig(t, nil)
	byID := map[uuid.UUID]pii.Phone{}
	for range 3 {
		byID[r.responder(t, responder.RoleVHT)] = phoneOf(t, r.phones)
	}
	r.responder(t, responder.RoleTransporter)
	tr := phoneOf(t, r.phones)
	caller, _ := pii.ParsePhone("+256772000778")
	id := r.incidentFrom(t, incident.SevYellow, caller)
	menu := r.menu(t, false)

	snap := r.mustProcess(t, id)
	if sc := dialMenu(t, menu, tr, "1"); !strings.Contains(sc.Text, "Accepted") {
		t.Fatalf("transporter accepts at once: %q", sc.Text)
	}
	if len(alertsOf(snap, responder.RoleVHT)) != 1 {
		t.Fatalf("yellow alerts one VHT at a time: %+v", snap.Alerts)
	}
	r.age(t, id, 90*time.Second) // the first VHT does not answer
	snap = r.mustProcess(t, id)
	second := alertsOf(snap, responder.RoleVHT)
	if len(second) != 2 || second[0].Outcome != dispatch.OutcomeNoResponse {
		t.Fatalf("after 90 s: %+v", second)
	}
	if sc := dialMenu(t, menu, byID[second[1].ResponderID], "2"); !sc.End || !strings.Contains(sc.Text, "Declined") {
		t.Fatalf("second VHT declines: %q", sc.Text)
	}
	snap = r.mustProcess(t, id)
	third := alertsOf(snap, responder.RoleVHT)
	if len(third) != 3 || third[1].Outcome != dispatch.OutcomeDeclined {
		t.Fatalf("after the decline: %+v", third)
	}
	if sc := dialMenu(t, menu, byID[third[2].ResponderID], "1"); !strings.Contains(sc.Text, "Accepted") {
		t.Fatalf("third VHT accepts: %q", sc.Text)
	}
	snap = r.mustProcess(t, id)
	if snap.State != incident.StateAssigned || len(snap.Assigned) != 2 {
		t.Fatalf("both roles assigned: %+v", snap)
	}
	for _, a := range snap.Alerts {
		if n := r.deliveries.count(a.ID, dispatch.PurposeAlert); n != 1 {
			t.Fatalf("alert %v sent %d times", a.ID, n)
		}
	}
	if !slices.ContainsFunc(r.deliveries.notices, func(n dispatch.Notice) bool { return n.Kind == dispatch.CallerHelperOnTheWay }) {
		t.Fatal("caller not told help is on the way")
	}
	r.noDrift(t)
}
