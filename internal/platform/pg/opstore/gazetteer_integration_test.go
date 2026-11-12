//go:build integration

package opstore_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

const gazYAML = `
version: "it-1"
status: placeholder
wards:
  - {id: w01, name: {en: "Test Ward 1", lg: "Test Ward 1"}, centre: {lat: 0.3800, lon: 32.5600}}
  - {id: w02, name: {en: "Test Ward 2", lg: "Test Ward 2"}, centre: {lat: 0.3950, lon: 32.5650}, zones: [{col: 903, row: 87}]}
landmarks:
  - {id: w01_l1, ward: w01, name: {en: "Market", lg: "Akatale"}, point: {lat: 0.3812, lon: 32.5588}, menu: 1, aliases: ["Kawempe Market"]}
  - {id: w01_l2, ward: w01, name: {en: "Clinic", lg: "Ddwaliro"}, point: {lat: 0.3790, lon: 32.5612}, menu: 2}
  - {id: w01_x, ward: w01, name: {en: "Old Taxi Stage", lg: "Siteegi"}, point: {lat: 0.3700, lon: 32.5500}}
  - {id: w02_l1, ward: w02, name: {en: "Mosque", lg: "Muzikiti"}, point: {lat: 0.3960, lon: 32.5660}, menu: 1}
stages:
  - {id: w01_stage, ward: w01, name: {en: "Stage 1", lg: "Siteegi 1"}, point: {lat: 0.3805, lon: 32.5595}}
  - {id: w02_stage, ward: w02, name: {en: "Stage 2", lg: "Siteegi 2"}, point: {lat: 0.3955, lon: 32.5655}}
facilities:
  - {id: hc_1, name: {en: "Health Centre 1", lg: "Health Centre 1"}, point: {lat: 0.3790, lon: 32.5610}, forwarding_enabled: true, phone: "+256000000001"}
  - {id: hc_2, name: {en: "Clinic 2", lg: "Clinic 2"}, point: {lat: 0.3700, lon: 32.5700}}
`

func loadGaz(t *testing.T, c *pgtest.TestCluster, doc string) (location.File, bool, error) {
	t.Helper()
	f, err := location.ParseFile([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := opstore.LoadGazetteer(context.Background(), c.Owner(t, pg.StoreOperational), f, []byte(doc))
	return f, loaded, err
}

func TestGazetteerLoadIsIdempotentPerVersion(t *testing.T) {
	c := pgtest.Cluster(t)
	if _, loaded, err := loadGaz(t, c, gazYAML); err != nil || !loaded {
		t.Fatalf("first load: loaded=%v err=%v", loaded, err)
	}
	if _, loaded, err := loadGaz(t, c, gazYAML); err != nil || loaded {
		t.Fatalf("same file again: loaded=%v err=%v, want a no-op", loaded, err)
	}
	changed := strings.Replace(gazYAML, `"Clinic"`, `"Health Centre"`, 1)
	if _, _, err := loadGaz(t, c, changed); !errors.Is(err, opstore.ErrGazetteerVersionExists) {
		t.Fatalf("different content under the same version: err = %v", err)
	}
}

func TestGazetteerSnapshotRoundTrips(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	if _, err := s.Snapshot(context.Background()); !errors.Is(err, opstore.ErrNoGazetteer) {
		t.Fatalf("empty store: err = %v, want ErrNoGazetteer", err)
	}
	want, _, err := loadGaz(t, c, gazYAML)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot differs from the loaded file:\n got %+v\nwant %+v", got, want)
	}
}

func TestMatchFindsAliasAboveThreshold(t *testing.T) {
	c := pgtest.Cluster(t)
	if _, _, err := loadGaz(t, c, gazYAML); err != nil {
		t.Fatal(err)
	}
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	for text, want := range map[string]string{"kawempe mrket": "w01_l1", "taxi stage": "w01_x", "DDWALIRO": "w01_l2"} {
		m, err := s.Match(ctx, "it-1", "w01", location.Normalise(text))
		if err != nil {
			t.Fatal(err)
		}
		if !m.Found || m.LandmarkID != want || m.Similarity < 0.4 {
			t.Errorf("Match(%q) = %+v, want %s above 0.4", text, m, want)
		}
	}
}

// A free-text landmark is matched only within the caller's ward: a name from
// another ward must never pull the incident across the catchment.
func TestMatchIsScopedToWard(t *testing.T) {
	c := pgtest.Cluster(t)
	if _, _, err := loadGaz(t, c, gazYAML); err != nil {
		t.Fatal(err)
	}
	m, err := storeAs(t, c, "op_intake").Match(context.Background(), "it-1", "w02", location.Normalise("kawempe market"))
	if err != nil {
		t.Fatal(err)
	}
	if m.LandmarkID == "w01_l1" || m.Similarity >= 0.4 {
		t.Fatalf("Match in w02 = %+v", m)
	}
	none, err := storeAs(t, c, "op_intake").Match(context.Background(), "it-1", "w99", "market")
	if err != nil || none.Found {
		t.Fatalf("unknown ward: %+v %v", none, err)
	}
}

// The gazetteer is frozen reference data: even its owner cannot edit a
// loaded version in place (a correction is a new version plus a deviation).
func TestGazetteerIsFrozen(t *testing.T) {
	c := pgtest.Cluster(t)
	if _, _, err := loadGaz(t, c, gazYAML); err != nil {
		t.Fatal(err)
	}
	owner := c.Owner(t, pg.StoreOperational)
	for _, q := range []string{
		"UPDATE op.landmarks SET name_en = 'x' WHERE id = 'w01_l1'",
		"DELETE FROM op.landmark_aliases WHERE landmark_id = 'w01_l1'",
		"UPDATE op.wards SET centre_col = 1",
		"DELETE FROM op.gazetteer_versions",
		"UPDATE op.stages SET zone_col = 1",
		"DELETE FROM op.facilities",
	} {
		if _, err := owner.Exec(context.Background(), q); err == nil || !strings.Contains(err.Error(), "frozen") {
			t.Errorf("%s: err = %v, want frozen", q, err)
		}
	}
	rt, err := pg.Connect(context.Background(), c.DSN(pg.StoreOperational, "op_intake"), "test", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if _, err := rt.Exec(context.Background(), "UPDATE op.landmarks SET menu = 0"); err == nil {
		t.Error("runtime role updated the gazetteer")
	}
}

// pg_trgm's functions keep PUBLIC EXECUTE (a trusted extension's objects
// belong to the bootstrap superuser), so they live in schema trgm, where no
// runtime role has USAGE: op.match_landmark is the only way to them.
func TestRuntimeRolesCannotReachTrigramFunctions(t *testing.T) {
	c := pgtest.Cluster(t)
	owner := c.Owner(t, pg.StoreOperational)
	for _, role := range pg.StoreOperational.RuntimeRoles() {
		var usage bool
		if err := owner.QueryRow(context.Background(), "SELECT has_schema_privilege($1, 'trgm', 'USAGE')", role).Scan(&usage); err != nil {
			t.Fatal(err)
		}
		if usage {
			t.Errorf("%s has USAGE on schema trgm", role)
		}
	}
	rt, err := pg.Connect(context.Background(), c.DSN(pg.StoreOperational, "op_intake"), "test", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if _, err := rt.Exec(context.Background(), "SELECT trgm.similarity('a', 'b')"); err == nil {
		t.Fatal("op_intake called a pg_trgm function directly")
	}
}
