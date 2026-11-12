//go:build integration

package wiring_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/respond"
	"sautihealth.com/sauti/internal/responder"
)

func (r *engineRig) menu(t *testing.T, askReasons bool) *respond.Service {
	t.Helper()
	s, err := respond.New(respond.Config{Store: r.intake, Keys: r.ids, Copy: r.cfg.Responder, Tool1: r.cfg.Tool1, Gazetteer: r.gaz,
		AskReasons: askReasons, ScreenLimit: r.cfg.Intake.DefaultScreenCharLimit})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// phoneOf is the synthetic number engineRig.responder gave its n-th responder.
func phoneOf(t *testing.T, n int) pii.Phone {
	t.Helper()
	p, err := pii.ParsePhone(fmt.Sprintf("+2567720003%02d", n))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func dialMenu(t *testing.T, s *respond.Service, phone pii.Phone, text string) intake.Screen {
	t.Helper()
	sc, err := s.HandleUSSD(context.Background(), intake.USSDRequest{Provider: "at", SessionID: "R", ServiceCode: "*384*2#", Phone: phone, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

// Scenario 10 through the responder's own menu: alerted, the transporter
// declines with reason 3 (bike or fuel problem); the reason is stored on the
// alert, linked to the responder, and the cascade moves on.
func TestScenario10DeclineThroughTheMenu(t *testing.T) {
	r := newEngineRig(t, nil)
	r.responder(t, responder.RoleTransporter)
	tr := phoneOf(t, r.phones)
	r.responder(t, responder.RoleTransporter)
	id := r.incident(t, incident.SevYellow)
	r.mustProcess(t, id)
	menu := r.menu(t, true)
	if sc := dialMenu(t, menu, tr, ""); sc.End || !strings.Contains(sc.Text, "1 Accept") {
		t.Fatalf("menu %q", sc.Text)
	}
	if sc := dialMenu(t, menu, tr, "2*3"); !sc.End {
		t.Fatalf("decline %q", sc.Text)
	}
	var reason string
	if err := r.c.Owner(t, pg.StoreOperational).QueryRow(context.Background(), `SELECT a.decline_reason FROM op.alerts a
		WHERE a.incident_id = $1 AND a.outcome = 'declined'`, id).Scan(&reason); err != nil || reason != "bike_or_fuel" {
		t.Fatalf("stored reason %q %v", reason, err)
	}
	snap := r.mustProcess(t, id)
	if n := len(alertsOf(snap, responder.RoleTransporter)); n != 2 {
		t.Fatalf("the cascade did not move on after the decline: %d transporter alerts", n)
	}
}

// Review focus 2 across the menu: two transporters alerted in parallel (red)
// accept at the same instant — exactly one is assigned, the other hears
// "already taken".
func TestConcurrentAcceptsThroughTheMenu(t *testing.T) {
	r := newEngineRig(t, nil)
	r.responder(t, responder.RoleTransporter)
	a := phoneOf(t, r.phones)
	r.responder(t, responder.RoleTransporter)
	b := phoneOf(t, r.phones)
	id := r.incident(t, incident.SevRed)
	r.mustProcess(t, id)
	menu := r.menu(t, false)
	var wg sync.WaitGroup
	screens := make([]intake.Screen, 2)
	for i, p := range []pii.Phone{a, b} {
		wg.Go(func() { screens[i] = dialMenu(t, menu, p, "1") })
	}
	wg.Wait()
	taken := 0
	for _, sc := range screens {
		if strings.Contains(sc.Text, "Already taken") {
			taken++
		}
	}
	snap, _ := r.store.DispatchSnapshot(context.Background(), id)
	if _, ok := snap.Assigned[responder.RoleTransporter]; taken != 1 || !ok {
		t.Fatalf("screens %+v assigned %v", screens, snap.Assigned)
	}
}
