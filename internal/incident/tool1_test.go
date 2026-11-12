package incident

import (
	"errors"
	"fmt"
	"testing"
	"uuid"
)

func answered(seq int64, screen int, field, value string) Event {
	return ev(seq, EvScreenAnswered, fmt.Sprintf(`{"screen":%d,"field":%q,"value":%q}`, screen, field, value))
}

func started() Event {
	return ev(1, EvSessionStarted, `{"reference":"1234567894","channel":"ussd_at"}`)
}

func TestApplyScreenAnsweredSetsTool1Fields(t *testing.T) {
	p, err := Fold([]Event{
		started(),
		answered(2, 1, FieldLanguage, "lg"),
		answered(3, 2, FieldDanger, "unknown"),
		answered(4, 3, FieldCategory, "poisoning"),
		answered(5, 4, FieldAgeBand, "age_18_59"),
		answered(6, 5, FieldSpecificDanger, "no"),
		answered(7, 6, FieldWard, "w01"),
		answered(8, 7, FieldLandmark, "w01_l1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{p.Language, p.Danger, p.Category, p.AgeBand, p.SpecificDanger, p.Ward, p.Landmark}
	want := []string{"lg", "unknown", "poisoning", "age_18_59", "no", "w01", "w01_l1"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("field %d = %q, want %q", i, got[i], want[i])
		}
	}
	if p.LastScreen != 7 || p.State != StateIntake {
		t.Fatalf("LastScreen=%d State=%s", p.LastScreen, p.State)
	}
}

// A screen answer carries only a coded value. A phone number, free text or
// any key outside the contract is refused, so no identifier can ever reach an
// event payload or the audit hash (hard constraints 5 and 8).
func TestApplyScreenAnsweredRejectsInvalidPayloads(t *testing.T) {
	p, err := Fold([]Event{started()})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{}`,
		`{"screen":2,"answer":"1"}`,
		`{"screen":9,"field":"danger","value":"yes"}`,
		`{"screen":0,"field":"language","value":"en"}`,
		`{"screen":"2","field":"danger","value":"yes"}`,
		`{"screen":2.5,"field":"danger","value":"yes"}`,
		`{"screen":2,"field":"danger","value":"yes","identifier_id":"{0195f3a2-7c1e-7d4b-9a10-2b3c4d5e6f70}"}`,
		`{"screen":3,"field":"danger","value":"yes"}`,
		`{"screen":2,"field":"danger","value":""}`,
		`{"screen":2,"field":"colour","value":"red"}`,
		`{"screen":8,"field":"callback","value":"0772123456"}`,
		`{"screen":7,"field":"landmark","value":"Kawempe market"}`,
		`{"screen":2,"field":"danger","value":"yes","phone":"0772123456"}`,
		`{"screen":8,"field":"callback","value":"provided"}`,
		`{"screen":7,"field":"landmark","value":"other","identifier_id":"not-a-uuid"}`,
		`{"screen":2,"field":"danger","value":"yes","identifier_id":"0195f3a2-7c1e-7d4b-9a10-2b3c4d5e6f70"}`,
	} {
		if _, err := Apply(p, ev(2, EvScreenAnswered, bad)); !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("payload %s: err = %v, want ErrIllegalTransition", bad, err)
		}
	}
}

func TestApplyScreenAnsweredAcceptsIdentifierReferences(t *testing.T) {
	id := uuid.NewV7()
	p, err := Fold([]Event{started()})
	if err != nil {
		t.Fatal(err)
	}
	for _, good := range []string{
		fmt.Sprintf(`{"screen":8,"field":"callback","value":"provided","identifier_id":%q}`, id),
		`{"screen":8,"field":"callback","value":"same_number"}`,
		fmt.Sprintf(`{"screen":7,"field":"landmark","value":"other","identifier_id":%q}`, id),
		`{"screen":7,"field":"landmark","value":"other"}`,
	} {
		if _, err := Apply(p, ev(2, EvScreenAnswered, good)); err != nil {
			t.Errorf("payload %s: %v", good, err)
		}
	}
}

func TestLastScreenIsMonotonic(t *testing.T) {
	p, err := Fold([]Event{started(), answered(2, 5, FieldSpecificDanger, "yes"), answered(3, 3, FieldCategory, "trauma")})
	if err != nil {
		t.Fatal(err)
	}
	if p.LastScreen != 5 || p.Category != "trauma" {
		t.Fatalf("LastScreen=%d Category=%q", p.LastScreen, p.Category)
	}
}

func TestDispatchReadySetsZone(t *testing.T) {
	base := []Event{started(), ev(2, EvClassified, `{"severity":"yellow"}`)}
	p, err := Fold(append(base, ev(3, EvDispatchReady, `{"zone":{"col":901,"row":70}, "location_quality":"fuzzy"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if p.Zone == nil || *p.Zone != (Zone{Col: 901, Row: 70}) || p.LocationQuality != "fuzzy" || p.State != StateDispatching {
		t.Fatalf("zone=%v quality=%q state=%s", p.Zone, p.LocationQuality, p.State)
	}
	q, _ := Fold(base)
	for _, bad := range []string{
		`{"zone":{"col":1,"row":2},"location_quality":"guess"}`,
		`{"zone":{"col":1},"location_quality":"ward"}`,
		`{"location_quality":"ward"}`,
		`{"zone":{"col":1.5,"row":2},"location_quality":"ward"}`,
		`{"zone":{"col":-0,"row":2},"location_quality":"ward"}`,
		`{"zone":{"col":"1","row":2},"location_quality":"ward"}`,
		`{"zone":{"col":1,"row":2,"lat":0.3},"location_quality":"ward"}`,
	} {
		if _, err := Apply(q, ev(3, EvDispatchReady, bad)); !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("dispatch_ready %s: err = %v", bad, err)
		}
	}
	south, err := Fold(append(base, ev(3, EvDispatchReady, `{"zone":{"col":933,"row":-56},"location_quality":"ward"}`)))
	if err != nil || *south.Zone != (Zone{Col: 933, Row: -56}) {
		t.Fatalf("south of the equator (EPSG:32636 has no false northing): %+v %v", south.Zone, err)
	}
	none, err := Fold(append(base, ev(3, EvDispatchReady, `{}`)))
	if err != nil || none.Zone != nil || none.LocationQuality != "" {
		t.Fatalf("dispatch_ready without location: %+v %v", none, err)
	}
}

func TestSessionResumedLeavesStateUnchanged(t *testing.T) {
	p, err := Fold([]Event{started(), ev(2, EvClassified, `{"severity":"red"}`), ev(3, EvSessionResumed, `{"channel":"ivr"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if p.State != StateTriaged || p.Version != 3 {
		t.Fatalf("state=%s version=%d", p.State, p.Version)
	}
	q, _ := Fold([]Event{started()})
	if _, err := Apply(q, ev(2, EvSessionResumed, `{"channel":"sms"}`)); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("unknown resume channel: %v", err)
	}
}

func TestDiffReportsTool1Fields(t *testing.T) {
	a := Projection{Category: "trauma", LastScreen: 3, Zone: &Zone{1, 2}, LocationQuality: "ward"}
	for name, b := range map[string]Projection{
		"category": {Category: "other", LastScreen: 3, Zone: &Zone{1, 2}, LocationQuality: "ward"},
		"last":     {Category: "trauma", LastScreen: 4, Zone: &Zone{1, 2}, LocationQuality: "ward"},
		"zone":     {Category: "trauma", LastScreen: 3, Zone: &Zone{1, 3}, LocationQuality: "ward"},
		"nozone":   {Category: "trauma", LastScreen: 3, LocationQuality: "ward"},
		"quality":  {Category: "trauma", LastScreen: 3, Zone: &Zone{1, 2}, LocationQuality: "fuzzy"},
	} {
		if Diff(a, b) == "" {
			t.Errorf("%s: Diff missed a difference", name)
		}
	}
	if d := Diff(a, Projection{Category: "trauma", LastScreen: 3, Zone: &Zone{1, 2}, LocationQuality: "ward"}); d != "" {
		t.Fatalf("equal projections differ: %s", d)
	}
}
