//go:build integration

package e2e

import (
	"context"
	"crypto/rand"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/ari"
	"sautihealth.com/sauti/internal/platform/ivr"
	"sautihealth.com/sauti/internal/platform/logging"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/platform/sim"
	"sautihealth.com/sauti/internal/platform/wiring"
)

// startIVR runs ivr-controller's components as op_ivr against the rig's
// database and a fake Asterisk.
func startIVR(t *testing.T, r *intakeRig) *sim.FakeARI {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cfg, err := config.Load(os.DirFS("../../config"))
	if err != nil {
		t.Fatal(err)
	}
	keys := r.keys
	pool, err := pg.Connect(ctx, r.c.DSN(pg.StoreOperational, "op_ivr"), "e2e-ivr", 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	doc, sig, err := wiring.ReadTriageBundle("../..", cfg.Triage.RulesFile)
	if err != nil {
		t.Fatal(err)
	}
	built, err := wiring.BuildIntake(ctx, wiring.IntakeOptions{Config: cfg, Store: opstore.New(pool), Keys: keys,
		Rand: rand.Reader, TriageDoc: doc, TriageSig: sig})
	if err != nil {
		t.Fatal(err)
	}
	fake := sim.NewFakeARI("sauti", "e2e-ari")
	t.Cleanup(fake.Close)
	t.Cleanup(func() {
		if t.Failed() { // the log is redacted: no numbers
			t.Logf("ivr log:\n%s\nARI requests: %v", r.logs.String(), fake.Requests())
		}
	})
	client, err := ari.New(ari.Config{BaseURL: fake.URL(), User: "sauti", Password: "e2e-ari", App: "sauti-ivr"})
	if err != nil {
		t.Fatal(err)
	}
	ctrl := ivr.New(client, built.Service, slog.New(logging.NewHandler(r.logs, slog.LevelDebug)), ivr.Options{NoInput: time.Minute, MaxReprompts: 1})
	go func() { _ = ctrl.Run(ctx, client) }()
	select {
	case <-fake.Connected():
	case <-time.After(5 * time.Second):
		t.Fatal("ivr controller did not connect to ARI")
	}
	return fake
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Scenario 5 (IVR part): the USSD session drops at screen 6; the caller rings
// the toll-free line within the window and finishes Tool 1 on the keypad.
// One incident, T1 of the first call, nothing recorded.
func TestScenario5FinishOnIVR(t *testing.T) {
	r := newIntakeRig(t)
	walk(t, r.ussd.Dial("+256772123456"), "", "1", "1", "1", "3", "2")
	first := r.only(t)
	fake := startIVR(t, r)

	fake.StartCall("chan-1", "+256772123456")
	waitFor(t, "the ward prompt", func() bool {
		p := fake.Plays("chan-1")
		return len(p) > 0 && slices.Contains(p[0], "sauti/en/ward")
	})
	for i, d := range []string{"1", "1", "1"} {
		fake.Press("chan-1", d)
		waitFor(t, "the next prompt", func() bool { return len(fake.Plays("chan-1")) > i+1 })
	}
	waitFor(t, "hang-up after the closing message", func() bool { return fake.HungUp("chan-1") })

	p := r.only(t)
	if p.LastScreen != 8 || p.T[2] == nil || !p.T[1].Equal(*first.T[1]) || p.State != incident.StateDispatching {
		t.Fatalf("projection = %+v", p)
	}
	closing := fake.Plays("chan-1")[len(fake.Plays("chan-1"))-1]
	if closing[0] != "sauti/en/complete" {
		t.Fatalf("closing prompt = %v", closing)
	}
	for _, req := range fake.Requests() {
		if strings.Contains(req, "record") || strings.Contains(req, "snoop") || strings.Contains(req, "bridges") {
			t.Fatalf("recording-related request %q", req)
		}
	}
	var sessions int
	if err := r.c.Owner(t, pg.StoreOperational).QueryRow(context.Background(),
		"SELECT count(*) FROM op.sessions WHERE incident_id = $1 AND channel = 'ivr'", p.ID).Scan(&sessions); err != nil || sessions != 1 {
		t.Fatalf("ivr sessions = %d, %v", sessions, err)
	}
	if strings.Contains(r.logs.String(), "772123456") {
		t.Fatal("a log line carries the caller's number")
	}
	r.noDrift(t)
}
