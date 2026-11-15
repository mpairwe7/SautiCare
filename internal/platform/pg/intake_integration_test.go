//go:build integration

package pg_test

import (
	"context"
	"crypto/rand"
	"os"
	"sync"
	"testing"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
	"sautihealth.com/sauti/internal/platform/wiring"
)

func intakeService(t *testing.T, c *pgtest.TestCluster, e env) *intake.Service {
	t.Helper()
	ctx := context.Background()
	b, err := config.Load(os.DirFS("../../../config"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../../config/gated/gazetteer.yaml")
	if err != nil {
		t.Fatal(err)
	}
	g, err := location.ParseFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opstore.LoadGazetteer(ctx, c.Owner(t, pg.StoreOperational), g, raw); err != nil {
		t.Fatal(err)
	}
	doc, sig, err := wiring.ReadTriageBundle("../../..", b.Triage.RulesFile)
	if err != nil {
		t.Fatal(err)
	}
	in, err := wiring.BuildIntake(ctx, wiring.IntakeOptions{Config: b, Store: e.intake, Keys: e.keys, Rand: rand.Reader,
		TriageDoc: doc, TriageSig: sig})
	if err != nil {
		t.Fatal(err)
	}
	return in.Service
}

func firstCallback() intake.USSDRequest {
	p, _ := pii.ParsePhone("0772123456")
	return intake.USSDRequest{Provider: "at", SessionID: "ATUid_retry", ServiceCode: "*384*1#", NetworkCode: "64110", Phone: p}
}

// Africa's Talking may retry a callback: the same screen comes back and
// nothing is written twice (Build Prompt §7 idempotency).
func TestRetriedCallbackReturnsSameScreenNoSecondEvent(t *testing.T) {
	c, e := newEnv(t)
	svc := intakeService(t, c, e)
	ctx := context.Background()
	a, err := svc.HandleUSSD(ctx, firstCallback())
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.HandleUSSD(ctx, firstCallback())
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("retry screen %+v != first %+v", b, a)
	}
	var incidents, events int
	owner := c.Owner(t, pg.StoreOperational)
	if err := owner.QueryRow(ctx, "SELECT (SELECT count(*) FROM op.incidents), (SELECT count(*) FROM op.incident_events)").
		Scan(&incidents, &events); err != nil {
		t.Fatal(err)
	}
	if incidents != 1 || events != 1 {
		t.Fatalf("incidents=%d events=%d, want 1 and 1", incidents, events)
	}
}

// A repeated Africa's Talking callback mid-flow (Scalable MVP Stack, §Edge
// cases, row 4): the stored step keyed on this session and input replays the
// same screen, and neither the event log nor the step store is written twice.
func TestRetriedAnswerReturnsSameScreenNoSecondWrite(t *testing.T) {
	c, e := newEnv(t)
	svc := intakeService(t, c, e)
	ctx := context.Background()
	owner := c.Owner(t, pg.StoreOperational)
	writes := func() (events, steps int) {
		t.Helper()
		if err := owner.QueryRow(ctx, "SELECT (SELECT count(*) FROM op.incident_events), (SELECT count(*) FROM op.ussd_steps)").
			Scan(&events, &steps); err != nil {
			t.Fatal(err)
		}
		return events, steps
	}
	req := firstCallback()
	if _, err := svc.HandleUSSD(ctx, req); err != nil {
		t.Fatal(err)
	}
	e0, s0 := writes()
	req.Text = "1" // screen 1: English
	a, err := svc.HandleUSSD(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	e1, s1 := writes()
	b, err := svc.HandleUSSD(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	e2, s2 := writes()
	if e1 != e0+1 || s1 != s0+1 {
		t.Fatalf("the answer wrote %d events and %d steps, want 1 and 1", e1-e0, s1-s0)
	}
	if a != b || e2 != e1 || s2 != s1 {
		t.Fatalf("retry: screen %q (first %q), %d more events, %d more steps", b.Text, a.Text, e2-e1, s2-s1)
	}
}

// Review focus 1: duplicate first callbacks arriving concurrently create one
// incident, and every response carries the same reference.
func TestConcurrentFirstCallbacksCreateOneIncident(t *testing.T) {
	c, e := newEnv(t)
	svc := intakeService(t, c, e)
	ctx := context.Background()
	var wg sync.WaitGroup
	screens := make([]intake.Screen, 10)
	errs := make([]error, 10)
	for i := range 10 {
		wg.Go(func() { screens[i], errs[i] = svc.HandleUSSD(ctx, firstCallback()) })
	}
	wg.Wait()
	for i := range 10 {
		if errs[i] != nil {
			t.Fatalf("callback %d: %v", i, errs[i])
		}
		if screens[i] != screens[0] {
			t.Fatalf("callback %d got %q, callback 0 got %q", i, screens[i].Text, screens[0].Text)
		}
	}
	var incidents int
	if err := c.Owner(t, pg.StoreOperational).QueryRow(ctx, "SELECT count(*) FROM op.incidents").Scan(&incidents); err != nil {
		t.Fatal(err)
	}
	if incidents != 1 {
		t.Fatalf("incidents = %d, want 1", incidents)
	}
}
