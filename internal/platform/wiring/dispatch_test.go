package wiring

import (
	"context"
	"errors"
	"os"
	"regexp"
	"testing"
	"time"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/location"
)

// Every dispatch fact carries the ranking version and the fingerprint of
// the gated configuration it ran under (Build Prompt §10.4: stamp the
// version on each incident), in the format the audit ledger accepts.
func TestDispatchLogicVersion(t *testing.T) {
	b := config.Bundle{Dispatch: config.Dispatch{RankingVersion: "default-2026-11-12"},
		Fingerprint: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	got, err := DispatchLogicVersion(b)
	if err != nil || got != "default-2026-11-12+sha256:0123456789ab" {
		t.Fatalf("got %q, %v", got, err)
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}\+sha256:[0-9a-f]{12,64}$`).MatchString(got) {
		t.Fatalf("%q is not an approved identifier", got)
	}
	for _, bad := range []config.Bundle{
		{Dispatch: config.Dispatch{RankingVersion: "Default 1"}, Fingerprint: b.Fingerprint},
		{Dispatch: config.Dispatch{RankingVersion: "v1"}, Fingerprint: "md5:abc"},
	} {
		if _, err := DispatchLogicVersion(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

// The gated parameters become the planner's: 2.5 km in 500 m steps is ring
// 5; a radius that is not a whole number of steps is refused.
func TestDispatchParamsFromConfig(t *testing.T) {
	b, err := config.Load(os.DirFS("../../../config"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := DispatchParams(b.Dispatch)
	if err != nil {
		t.Fatal(err)
	}
	want := dispatch.Params{Mode: dispatch.ModeProtocol, RingInterval: time.Minute, MaxRing: 5, CascadeTimeout: 90 * time.Second,
		RedParallel: 3, ForwardAckTimeout: time.Minute, RedEscalationAfter: 3 * time.Minute}
	if p != want {
		t.Fatalf("params %+v, want %+v", p, want)
	}
	for name, mut := range map[string]func(*config.Dispatch){
		"radius not whole steps": func(d *config.Dispatch) { d.MaxRadiusMetres = 2400; d.RingStepMetres = 700 },
		"zero step":              func(d *config.Dispatch) { d.RingStepMetres = 0 },
		"unknown mode":           func(d *config.Dispatch) { d.Mode = "fast" },
		"no red escalation":      func(d *config.Dispatch) { d.RedEscalationAfter = config.Duration{} },
	} {
		d := b.Dispatch
		mut(&d)
		if _, err := DispatchParams(d); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Intake and the planner escalate a suspended incident with one reason, so
// the coordinator is alerted once.
func TestSuspensionReasonIsShared(t *testing.T) {
	if intake.ReasonDispatchSuspended != dispatch.ReasonSuspended {
		t.Fatalf("intake %q, dispatch %q", intake.ReasonDispatchSuspended, dispatch.ReasonSuspended)
	}
}

// snapshotStore is a dispatch store that only serves the gazetteer: it
// records the context it was called with.
type snapshotStore struct {
	dispatch.Store
	gaz      location.File
	deadline time.Time
	bounded  bool
}

func (s *snapshotStore) Snapshot(ctx context.Context) (location.File, error) {
	s.deadline, s.bounded = ctx.Deadline()
	return s.gaz, nil
}

func buildOptions(t *testing.T, gaz location.File, production bool) (DispatchOptions, *snapshotStore) {
	t.Helper()
	cfg, err := config.Load(os.DirFS("../../../config"))
	if err != nil {
		t.Fatal(err)
	}
	st := &snapshotStore{gaz: gaz}
	return DispatchOptions{Config: cfg, Store: st, Deliverer: nopDeliverer{}, Suspension: nopSuspension{},
		Production: production}, st
}

type nopDeliverer struct{}

func (nopDeliverer) Deliver(context.Context, dispatch.Delivery) (string, error) { return "", nil }

type nopSuspension struct{}

func (nopSuspension) DispatchSuspended(context.Context) (bool, error) { return false, nil }

// CodeRabbit PR #10 (4557205830): a placeholder gazetteer — synthetic
// stages and facilities whose numbers reach nobody — never runs dispatch in
// production, as it never runs intake (BuildIntake); dev and test keep it.
func TestBuildDispatchRefusesAPlaceholderGazetteerInProduction(t *testing.T) {
	placeholder := location.File{Version: "placeholder-2026-11-13", Status: location.StatusPlaceholder}
	approved := location.File{Version: "kawempe-2027-01", Status: location.StatusApproved}
	for _, tc := range []struct {
		gaz        location.File
		production bool
		refused    bool
	}{
		{placeholder, true, true},
		{placeholder, false, false},
		{approved, true, false},
	} {
		o, _ := buildOptions(t, tc.gaz, tc.production)
		_, err := BuildDispatch(context.Background(), o)
		if refused := errors.Is(err, location.ErrNotProductionReady); refused != tc.refused || (!tc.refused && err != nil) {
			t.Errorf("%s production=%v: %v", tc.gaz.Status, tc.production, err)
		}
	}
}

// CodeRabbit PR #10 (4557206033): the gazetteer read at startup has its own
// deadline, so a database that stops answering during connection setup
// cannot hold the worker before its health endpoints exist.
func TestBuildDispatchBoundsTheGazetteerRead(t *testing.T) {
	o, st := buildOptions(t, location.File{Version: "it-1", Status: location.StatusPlaceholder}, false)
	start := time.Now()
	if _, err := BuildDispatch(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if !st.bounded || st.deadline.Sub(start) > time.Minute {
		t.Fatalf("snapshot context: deadline %v (bounded %v)", st.deadline, st.bounded)
	}
}
