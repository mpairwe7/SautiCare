package dispatch

import (
	"math/rand/v2"
	"slices"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/responder"
)

var (
	t0     = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	centre = location.Cell{Col: 902, Row: 84}
)

func rider(stage string) responder.Responder {
	cert, ins := t0.Add(-time.Hour), t0.Add(24*time.Hour)
	return responder.Responder{ID: uuid.NewV7(), Role: responder.RoleTransporter, Stage: stage, Status: responder.StatusActive,
		CertifiedAt: &cert, InsuredUntil: &ins, KitOK: true,
		Available: &responder.Period{From: t0.Add(-time.Hour), Until: t0.Add(7 * time.Hour)}}
}

var stages = map[string]location.Cell{
	"here": centre, "east": {Col: 903, Row: 84}, "two": {Col: 904, Row: 86}, "far": {Col: 908, Row: 84},
}

// Build Prompt §10.4: eligible responders are certified, insured, kit OK,
// with availability open, registered at a stage inside the current ring.
func TestEligibleAppliesGatesAvailabilityAndRing(t *testing.T) {
	ok := rider("here")
	near := rider("east")
	two := rider("two")
	lapsed := rider("here")
	lapsed.KitOK = false
	off := rider("here")
	off.Available = &responder.Period{From: t0.Add(-9 * time.Hour), Until: t0.Add(-time.Hour)}
	unknownStage := rider("nowhere")
	vht := rider("here")
	vht.Role = responder.RoleVHT
	got := Eligible([]responder.Responder{ok, near, two, lapsed, off, unknownStage, vht}, stages, responder.RoleTransporter, centre, 1, t0)
	ids := []uuid.UUID{}
	for _, c := range got {
		ids = append(ids, c.Responder.ID)
	}
	if len(got) != 2 || !slices.Contains(ids, ok.ID) || !slices.Contains(ids, near.ID) {
		t.Fatalf("ring 1 eligible = %v", got)
	}
	if n := len(Eligible([]responder.Responder{ok, near, two}, stages, responder.RoleTransporter, centre, 2, t0)); n != 3 {
		t.Fatalf("ring 2 eligible = %d, want 3", n)
	}
}

func TestRankByZoneDistanceThenHistoryThenRecency(t *testing.T) {
	r, err := NewRanker(DefaultOrder, "default-2026-11-12")
	if err != nil {
		t.Fatal(err)
	}
	last := t0.Add(-time.Hour)
	older := t0.Add(-48 * time.Hour)
	far := Candidate{Responder: rider("two"), Distance: 2}
	goodHistory := Candidate{Responder: rider("east"), Distance: 1, History: History{Alerted: 10, Accepted: 9, LastDispatched: &last}}
	poorHistory := Candidate{Responder: rider("east"), Distance: 1, History: History{Alerted: 10, Accepted: 2}}
	sameHistoryRecent := Candidate{Responder: rider("east"), Distance: 1, History: History{Alerted: 10, Accepted: 9, LastDispatched: &last}}
	sameHistoryOld := Candidate{Responder: rider("east"), Distance: 1, History: History{Alerted: 10, Accepted: 9, LastDispatched: &older}}
	nearest := Candidate{Responder: rider("here"), Distance: 0, History: History{Alerted: 10, Accepted: 0}}
	got := r.Rank([]Candidate{far, poorHistory, sameHistoryRecent, nearest, goodHistory, sameHistoryOld})
	if got[0].Responder.ID != nearest.Responder.ID || got[len(got)-1].Responder.ID != far.Responder.ID {
		t.Fatalf("distance first: %v", got)
	}
	pos := func(c Candidate) int {
		return slices.IndexFunc(got, func(x Candidate) bool { return x.Responder.ID == c.Responder.ID })
	}
	if pos(sameHistoryOld) > pos(sameHistoryRecent) || pos(poorHistory) < pos(sameHistoryRecent) {
		t.Fatalf("history then least recently dispatched: %v", got)
	}
}

// The order is total: any permutation of the same candidates ranks the same.
func TestRankIsTotalAndStable(t *testing.T) {
	r, _ := NewRanker(DefaultOrder, "v")
	var cs []Candidate
	for i := range 20 {
		cs = append(cs, Candidate{Responder: rider("here"), Distance: i % 3, History: History{Alerted: i % 4, Accepted: i % 2}})
	}
	want := r.Rank(slices.Clone(cs))
	for range 20 {
		shuffled := slices.Clone(cs)
		rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		got := r.Rank(shuffled)
		for i := range got {
			if got[i].Responder.ID != want[i].Responder.ID {
				t.Fatal("rank depends on input order")
			}
		}
	}
}

// Decision 4 is open: the order comes from config, an unknown criterion is
// refused, and the version is stamped on every dispatch.
func TestNewRankerRejectsUnknownCriterion(t *testing.T) {
	if _, err := NewRanker([]string{"zone_distance", "seniority"}, "v"); err == nil {
		t.Fatal("unknown criterion accepted")
	}
	if _, err := NewRanker(nil, "v"); err == nil {
		t.Fatal("empty order accepted")
	}
	r, err := NewRanker([]string{"least_recently_dispatched", "zone_distance"}, "trial-2")
	if err != nil || r.Version() != "trial-2" {
		t.Fatalf("custom order: %v %v", r, err)
	}
}

// The incident log's alert roles and the registry's roles are one list.
func TestAlertRolesMatchRegistryRoles(t *testing.T) {
	var roles []string
	for _, r := range responder.Roles() {
		roles = append(roles, string(r))
	}
	if !slices.Equal(roles, incident.AlertRoles) {
		t.Fatalf("registry %v, incident log %v", roles, incident.AlertRoles)
	}
}
