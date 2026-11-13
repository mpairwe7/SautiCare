//go:build integration

package wiring_test

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"strings"
	"testing"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/platform/devkeys"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
	"sautihealth.com/sauti/internal/platform/wiring"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

func options(t *testing.T, c *pgtest.TestCluster, production bool) wiring.IntakeOptions {
	t.Helper()
	cfg, err := config.Load(os.DirFS("../../../config"))
	if err != nil {
		t.Fatal(err)
	}
	doc, sig, err := wiring.ReadTriageBundle("../../..", cfg.Triage.RulesFile)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir() + "/k"
	if err := devkeys.Generate(dir, rand.Reader); err != nil {
		t.Fatal(err)
	}
	keys, err := devkeys.Open(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pg.Connect(context.Background(), c.DSN(pg.StoreOperational, "op_intake"), "test", 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return wiring.IntakeOptions{Config: cfg, Store: opstore.New(pool), Keys: keys, Rand: rand.Reader,
		TriageDoc: doc, TriageSig: sig, Production: production}
}

func loadRepoGazetteer(t *testing.T, c *pgtest.TestCluster) {
	t.Helper()
	raw, err := os.ReadFile("../../../config/gated/gazetteer.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f, err := location.ParseFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opstore.LoadGazetteer(context.Background(), c.Owner(t, pg.StoreOperational), f, raw); err != nil {
		t.Fatal(err)
	}
}

func TestBuildIntakeNeedsAGazetteer(t *testing.T) {
	c := pgtest.Cluster(t)
	if _, err := wiring.BuildIntake(context.Background(), options(t, c, false)); !errors.Is(err, opstore.ErrNoGazetteer) {
		t.Fatalf("err = %v, want ErrNoGazetteer", err)
	}
}

func TestBuildIntakeWiresTheRepositoryConfiguration(t *testing.T) {
	c := pgtest.Cluster(t)
	loadRepoGazetteer(t, c)
	in, err := wiring.BuildIntake(context.Background(), options(t, c, false))
	if err != nil {
		t.Fatal(err)
	}
	if in.Service == nil || in.Gazetteer.Version != "placeholder-2026-11-13b" || !strings.HasPrefix(in.Triage.LogicVersion(), "tool1-placeholder-0.1+") {
		t.Fatalf("built = %+v", in)
	}
}

// Placeholder Tool 1 wording, gazetteer or triage bundle never run live.
func TestBuildIntakeRefusesPlaceholdersInProduction(t *testing.T) {
	c := pgtest.Cluster(t)
	loadRepoGazetteer(t, c)
	_, err := wiring.BuildIntake(context.Background(), options(t, c, true))
	if err == nil {
		t.Fatal("production build accepted placeholder content")
	}
	for _, want := range []string{"protocol_status", "gazetteer", "triage"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("production refusal %q does not name %s", err, want)
		}
	}
}

func TestBuildIntakeRefusesATamperedBundle(t *testing.T) {
	c := pgtest.Cluster(t)
	loadRepoGazetteer(t, c)
	o := options(t, c, false)
	o.TriageDoc = []byte(strings.Replace(string(o.TriageDoc), `["poisoning"]`, `[]`, 1))
	if _, err := wiring.BuildIntake(context.Background(), o); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("tampered bundle: %v", err)
	}
}
