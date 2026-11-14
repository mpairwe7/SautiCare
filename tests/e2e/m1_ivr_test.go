//go:build integration

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/pii"
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
	newCtrl, client, fake := ivrParts(t, r)
	ctrl := newCtrl()
	go func() { _ = ctrl.Run(ctx, client) }()
	select {
	case <-fake.Connected():
	case <-time.After(5 * time.Second):
		t.Fatal("ivr controller did not connect to ARI")
	}
	return fake
}

// ivrParts builds ivr-controller's parts as op_ivr against the rig's
// database and a fake Asterisk: newCtrl makes a controller (call it twice
// for a restart), sharing one store and one client. wrap, if given, stands
// between the controller and the real intake service.
func ivrParts(t *testing.T, r *intakeRig, wrap ...func(ivr.Intake) ivr.Intake) (newCtrl func() *ivr.Controller, client *ari.Client, fake *sim.FakeARI) {
	t.Helper()
	ctx := context.Background()
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
	pwb := make([]byte, 16)
	if _, err := rand.Read(pwb); err != nil {
		t.Fatal(err)
	}
	ariPassword := hex.EncodeToString(pwb) // random per run: no password literal
	fake = sim.NewFakeARI("sauti", ariPassword)
	t.Cleanup(fake.Close)
	t.Cleanup(func() {
		if t.Failed() { // the log is redacted: no numbers
			t.Logf("ivr log:\n%s\nARI requests: %v", r.logs.String(), fake.Requests())
		}
	})
	client, err = ari.New(ari.Config{BaseURL: fake.URL(), User: "sauti", Password: ariPassword, App: "sauti-ivr"})
	if err != nil {
		t.Fatal(err)
	}
	var in ivr.Intake = built.Service
	for _, w := range wrap {
		in = w(in)
	}
	return func() *ivr.Controller {
		return ivr.New(client, in, slog.New(logging.NewHandler(r.logs, slog.LevelDebug)), ivr.Options{NoInput: time.Minute, MaxReprompts: 1})
	}, client, fake
}

// CodeRabbit (PR #13, C2): ivr-controller restarts mid-call. The new
// controller adopts the orphaned call: it escalates the call's incident,
// found from the call ID, before it hands the call to the fallback. The
// fallback's dial may then fail (busy, unanswered, a placeholder: dialplan
// work after the continue); the escalation already stands, so the
// coordinator line rings.
func TestAnAdoptedCallIsEscalatedWhateverTheFallbackDialDoes(t *testing.T) {
	r := newIntakeRig(t)
	newCtrl, _, fake := ivrParts(t, r)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	before := newCtrl()
	fake.StartCall("chan-9", "+256772123456") // live in the application; no stream yet, so the event is lost
	before.Handle(ctx, ivr.Event{Type: ivr.EventStasisStart, ChannelID: "chan-9", CallerNumber: "+256772123456"})
	waitFor(t, "the language prompt", func() bool { return len(fake.Plays("chan-9")) > 0 })
	inc := r.only(t)
	after := newCtrl() // the restarted process: no worker for chan-9
	if err := after.Adopt(ctx); err != nil {
		t.Fatal(err)
	}
	if got := fake.Continued("chan-9"); got != ivr.FallbackContext+",s,1" || fake.HungUp("chan-9") {
		t.Fatalf("continued %q hung up %v", got, fake.HungUp("chan-9"))
	}
	var n int
	if err := r.c.Owner(t, pg.StoreOperational).QueryRow(ctx, `SELECT count(*) FROM op.incident_events WHERE incident_id = $1
		AND type = 'incident.escalated' AND payload ->> 'reason' = 'voice_handed_over'`, inc.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("voice_handed_over escalations %d %v, want 1", n, err)
	}
}

// PR #14 review (round 4): intake fails after the call's session opened (its
// incident written, the reply lost to a timeout). The controller escalates
// under the call ID, and op_ivr's real store appends the escalation to that
// incident before the call goes to the fallback.
func TestAStartThatFailsAfterItsSessionOpenedIsEscalated(t *testing.T) {
	r := newIntakeRig(t)
	newCtrl, _, fake := ivrParts(t, r, func(in ivr.Intake) ivr.Intake { return replyLost{in} })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctrl := newCtrl()
	fake.StartCall("chan-7", "+256772123456")
	ctrl.Handle(ctx, ivr.Event{Type: ivr.EventStasisStart, ChannelID: "chan-7", CallerNumber: "+256772123456"})
	waitFor(t, "the hand-over", func() bool { return fake.Continued("chan-7") != "" })
	inc := r.only(t)
	var n int
	if err := r.c.Owner(t, pg.StoreOperational).QueryRow(ctx, `SELECT count(*) FROM op.incident_events WHERE incident_id = $1
		AND type = 'incident.escalated' AND payload ->> 'reason' = 'voice_handed_over'`, inc.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("voice_handed_over escalations %d %v, want 1", n, err)
	}
}

// replyLost runs the real StartIVR, then reports a timeout: the session and
// its incident are written, but the controller never learns them.
type replyLost struct{ ivr.Intake }

func (s replyLost) StartIVR(ctx context.Context, callID string, phone pii.Phone) (intake.IVRTurn, error) {
	if _, err := s.Intake.StartIVR(ctx, callID, phone); err != nil {
		return intake.IVRTurn{}, err
	}
	return intake.IVRTurn{}, context.DeadlineExceeded
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
