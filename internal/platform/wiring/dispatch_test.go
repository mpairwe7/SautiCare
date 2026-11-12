package wiring

import (
	"os"
	"regexp"
	"testing"
	"time"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/dispatch"
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
