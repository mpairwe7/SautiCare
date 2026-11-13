package incident

import (
	"strings"
	"testing"
	"time"
)

func TestDiffEqualProjections(t *testing.T) {
	p, err := Fold(FixtureLifecycle())
	if err != nil {
		t.Fatal(err)
	}
	if d := Diff(p, p); d != "" {
		t.Fatalf("Diff of identical projections = %q", d)
	}
}

// Timestamps compare by instant, not by location, because the database
// returns UTC while a fixture may carry another zone.
func TestDiffComparesInstantsNotZones(t *testing.T) {
	p, _ := Fold(FixtureLifecycle())
	q := p
	t1 := p.T[1].In(time.FixedZone("EAT", 3*3600))
	q.T[1] = &t1
	if d := Diff(p, q); d != "" {
		t.Fatalf("zone change reported as difference: %s", d)
	}
}

func TestDiffReportsEachField(t *testing.T) {
	p, _ := Fold(FixtureLifecycle())
	q := p
	q.State = StateOnScene
	q.Version = 99
	q.T[5] = nil
	q.Flags = []string{"x"}
	d := Diff(p, q)
	for _, want := range []string{"state", "version", "T5", "flags"} {
		if !strings.Contains(d, want) {
			t.Errorf("Diff %q does not mention %s", d, want)
		}
	}
}
