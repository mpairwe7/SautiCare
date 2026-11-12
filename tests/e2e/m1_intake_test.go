//go:build integration

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/platform/africastalking"
	"sautihealth.com/sauti/internal/platform/devkeys"
	"sautihealth.com/sauti/internal/platform/logging"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
	"sautihealth.com/sauti/internal/platform/sim"
	"sautihealth.com/sauti/internal/platform/wiring"
)

// syncBuffer collects the process log for the no-number scan.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type intakeRig struct {
	c    *pgtest.TestCluster
	ussd *sim.USSD
	ro   *opstore.Store
	logs *syncBuffer
}

// newIntakeRig runs intake-gw's components (wiring.BuildIntake behind the
// Africa's Talking adapter, with the redacting logger) against PostgreSQL 18
// and the repository's configuration, gazetteer and signed triage bundle.
func newIntakeRig(t *testing.T) *intakeRig {
	t.Helper()
	ctx := context.Background()
	c := pgtest.Cluster(t)
	cfg, err := config.Load(os.DirFS("../../config"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../config/gated/gazetteer.yaml")
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
	dir := t.TempDir() + "/keys"
	if err := devkeys.Generate(dir, rand.Reader); err != nil {
		t.Fatal(err)
	}
	keys, err := devkeys.Open(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pg.Connect(ctx, c.DSN(pg.StoreOperational, "op_intake"), "e2e", 8)
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
	logs := &syncBuffer{}
	tok := make([]byte, 16)
	_, _ = rand.Read(tok)
	token := hex.EncodeToString(tok)
	h, err := africastalking.NewUSSDHandler(africastalking.USSDConfig{Provider: "at", PathToken: token,
		ServiceCodes: []string{"*384*1#"}, ErrorText: cfg.Screens.InternalError["en"]},
		built.Service, slog.New(logging.NewHandler(logs, slog.LevelDebug)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	ro, err := pg.Connect(ctx, c.DSN(pg.StoreOperational, "op_readonly"), "e2e-ro", 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ro.Close)
	return &intakeRig{c: c, ussd: sim.NewUSSD(srv.URL, token, "*384*1#", "64110"), ro: opstore.New(ro), logs: logs}
}

func walk(t *testing.T, s *sim.USSDSession, inputs ...string) sim.Screen {
	t.Helper()
	var sc sim.Screen
	for _, in := range inputs {
		var err error
		if sc, err = s.Send(in); err != nil {
			t.Fatalf("send %q: %v", in, err)
		}
		if strings.Contains(sc.Text, "cannot take this request") {
			t.Fatalf("send %q: intake failed: %q", in, sc.Text)
		}
	}
	return sc
}

func (r *intakeRig) only(t *testing.T) incident.Projection {
	t.Helper()
	ids, err := r.ro.ListIncidentIDs(context.Background())
	if err != nil || len(ids) != 1 {
		t.Fatalf("incidents = %v, %v (want exactly one)", ids, err)
	}
	p, err := r.ro.Projection(context.Background(), ids[0])
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (r *intakeRig) noDrift(t *testing.T) {
	t.Helper()
	if _, drift, err := opstore.VerifyProjections(context.Background(), r.ro); err != nil || len(drift) != 0 {
		t.Fatalf("verify-projections: %v %v", drift, err)
	}
}

// Scenario 3: poisoning with screen 5 = no is classified yellow, not green,
// by the signed rule table; T2 is recorded after screen 7, before screen 8.
func TestScenario3PoisoningIsYellow(t *testing.T) {
	r := newIntakeRig(t)
	done := walk(t, r.ussd.Dial("+256772123456"), "", "1", "2", "4", "3", "2", "1", "2", "1")
	p := r.only(t)
	if !done.End || !strings.Contains(done.Text, p.Reference.Spoken()) {
		t.Fatalf("closing screen = %+v", done)
	}
	if p.Severity != incident.SevYellow || p.Category != "poisoning" || p.T[2] == nil || p.LastScreen != 8 ||
		!strings.HasPrefix(p.LogicVersion, "tool1-placeholder-0.1+sha256:") || p.LocationQuality != incident.QualityLandmark {
		t.Fatalf("projection = %+v", p)
	}
	r.noDrift(t)
}

// Scenario 5 (USSD part): the session drops at screen 6; a redial from the
// same number within the window resumes at screen 6 — one incident, T1 of
// the first call, two sessions.
func TestScenario5RedialResumesAtScreen6(t *testing.T) {
	r := newIntakeRig(t)
	walk(t, r.ussd.Dial("+256772123456"), "", "1", "1", "1", "3", "2")
	first := r.only(t)
	ward := walk(t, r.ussd.Dial("+256772123456"), "")
	if !strings.Contains(ward.Text, "Test Ward 1") || ward.End {
		t.Fatalf("redial screen = %+v, want the ward menu", ward)
	}
	p := r.only(t)
	if !p.T[1].Equal(*first.T[1]) || p.Severity != incident.SevRed {
		t.Fatalf("after redial: %+v", p)
	}
	var sessions int
	if err := r.c.Owner(t, pg.StoreOperational).QueryRow(context.Background(),
		"SELECT count(*) FROM op.sessions WHERE incident_id = $1", p.ID).Scan(&sessions); err != nil || sessions != 2 {
		t.Fatalf("sessions = %d, %v", sessions, err)
	}
	r.noDrift(t)
}

// Scenario 5 completed: the resumed session finishes Tool 1.
func TestScenario5ResumedSessionCompletes(t *testing.T) {
	r := newIntakeRig(t)
	walk(t, r.ussd.Dial("+256772123456"), "", "1", "1", "1", "3", "2")
	done := walk(t, r.ussd.Dial("+256772123456"), "", "1", "1", "1")
	p := r.only(t)
	if !done.End || p.LastScreen != 8 || p.T[2] == nil || p.State != incident.StateDispatching {
		t.Fatalf("screen %+v, projection %+v", done, p)
	}
	r.noDrift(t)
}

// Scenario 6: "Somewhere else" gives 999 or 112; the record is kept, flagged
// out_of_area and closed, and never dispatched.
func TestScenario6SomewhereElse(t *testing.T) {
	r := newIntakeRig(t)
	end := walk(t, r.ussd.Dial("+256772123456"), "", "1", "2", "1", "3", "2", "5")
	p := r.only(t)
	if !end.End || !strings.Contains(end.Text, "999") || !strings.Contains(end.Text, "112") {
		t.Fatalf("screen = %+v", end)
	}
	if p.State != incident.StateClosed || !slices.Contains(p.Flags, "out_of_area") || p.T[2] != nil {
		t.Fatalf("projection = %+v", p)
	}
	r.noDrift(t)
}

// Build Prompt §16: a caller's number never appears in any log line — not
// the session's number, not a typed callback number.
func TestNoCallerNumberInLogs(t *testing.T) {
	r := newIntakeRig(t)
	walk(t, r.ussd.Dial("+256772123456"), "", "1", "2", "1", "3", "2", "1", "6", "Mkt*near taxi", "0772 999 888")
	logs := r.logs.String()
	if !strings.Contains(logs, "ussd") {
		t.Fatalf("no request logs captured: %q", logs)
	}
	for _, leak := range []string{"772123456", "772999888", "772 999 888", "Mkt*near"} {
		if strings.Contains(logs, leak) {
			t.Fatalf("log carries %q:\n%s", leak, logs)
		}
	}
	p := r.only(t)
	if p.LastScreen != 8 || p.Landmark != incident.LandmarkOther {
		t.Fatalf("projection = %+v", p)
	}
}
