package dispatch

import (
	"math"
	"testing"

	"go.yaml.in/yaml/v4"
)

func outcomes(n, failures int) []bool {
	out := make([]bool, n)
	for i := range n {
		out[i] = i >= failures // the first `failures` failed
	}
	return out
}

// Guide §14: red dispatch failure above 15% across 30 consecutive red
// incidents suspends dispatch. 4 of 30 (13.3%) passes; 5 of 30 (16.7%)
// breaches; fewer than 30 settled incidents are not judged.
func TestStoppingRuleBoundaries(t *testing.T) {
	r := StoppingRule{Window: 30, MaxFailureRate: 0.15}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		n, failures int
		breached    bool
	}{
		{30, 0, false}, {30, 4, false}, {30, 5, true}, {30, 30, true},
		{29, 29, false}, // not yet 30 settled red incidents
	} {
		got, failures := r.Breached(outcomes(tc.n, tc.failures))
		if got != tc.breached || (tc.n == 30 && failures != tc.failures) {
			t.Errorf("%d of %d: breached=%v failures=%d, want %v", tc.failures, tc.n, got, failures, tc.breached)
		}
	}
	// Only the most recent Window outcomes count (newest first).
	if got, f := r.Breached(append(outcomes(30, 4), false, false, false)); got || f != 4 {
		t.Errorf("older failures outside the window counted: %v %d", got, f)
	}
	// 4.5 of 30 is exactly 15%: not above, so not breached (15 of 100 too).
	if got, _ := (StoppingRule{Window: 100, MaxFailureRate: 0.15}).Breached(outcomes(100, 15)); got {
		t.Error("exactly 15% breached; the rule is above 15%")
	}
	for _, bad := range []StoppingRule{{Window: 0, MaxFailureRate: 0.15}, {Window: 30, MaxFailureRate: 0}, {Window: 30, MaxFailureRate: 1}} {
		if bad.Validate() == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

// CodeRabbit PR #10 (4557205976): YAML decodes ".nan" as a float64, and a
// NaN rate compares false both ways, so Validate passed it and Breached
// never fired. A NaN (or infinite) rate is refused before the engine runs.
func TestStoppingRuleRefusesANaNRate(t *testing.T) {
	var cfg struct {
		Rate float64 `yaml:"max_failure_rate"`
	}
	if err := yaml.Unmarshal([]byte("max_failure_rate: .nan"), &cfg); err != nil || !math.IsNaN(cfg.Rate) {
		t.Fatalf("the decoder no longer yields NaN (%v, %v): revisit this test", cfg.Rate, err)
	}
	for _, rate := range []float64{cfg.Rate, math.Inf(1), math.Inf(-1)} {
		if err := (StoppingRule{Window: 30, MaxFailureRate: rate}).Validate(); err == nil {
			t.Errorf("rate %v accepted", rate)
		}
	}
}
