//go:build integration

package pg_test

import (
	"context"
	"crypto/rand"
	"os"
	"sync"
	"testing"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/platform/pg"
)

func intakeService(t *testing.T, e env) *intake.Service {
	t.Helper()
	b, err := config.Load(os.DirFS("../../../config"))
	if err != nil {
		t.Fatal(err)
	}
	ids := crypto.Identifiers{Env: crypto.NewEnvelope(e.keys, rand.Reader)}
	return intake.NewService(e.svc, e.intake, ids, b.Screens, "en")
}

func firstCallback() intake.USSDRequest {
	p, _ := pii.ParsePhone("0772123456")
	return intake.USSDRequest{Provider: "at", SessionID: "ATUid_retry", ServiceCode: "*384*1#", NetworkCode: "64110", Phone: p}
}

// Africa's Talking may retry a callback: the same screen comes back and
// nothing is written twice (Build Prompt §7 idempotency).
func TestRetriedCallbackReturnsSameScreenNoSecondEvent(t *testing.T) {
	c, e := newEnv(t)
	svc := intakeService(t, e)
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

// Review focus 1: duplicate first callbacks arriving concurrently create one
// incident, and every response carries the same reference.
func TestConcurrentFirstCallbacksCreateOneIncident(t *testing.T) {
	c, e := newEnv(t)
	svc := intakeService(t, e)
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
