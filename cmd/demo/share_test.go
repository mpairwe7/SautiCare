package main

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/pii"
)

// shareRig is the gazetteer, its map, and the demo's phones as startStack
// builds them, without a database.
func shareRig(t *testing.T) (location.File, mapView, []phone) {
	t.Helper()
	raw, err := os.ReadFile("../../config/gated/gazetteer.yaml")
	if err != nil {
		t.Fatal(err)
	}
	gaz, err := location.ParseFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	geo, err := newMapView(raw, gaz)
	if err != nil {
		t.Fatal(err)
	}
	phones := append([]phone{}, callers...)
	for _, d := range seeds {
		phones = append(phones, phone{Handle: handleOf(d.Label), Label: d.Label, Kind: "responder", Role: string(d.Role), Ward: d.Ward,
			Stage: d.Stage, Facility: vhtFacility[d.Label], e164: d.Phone})
	}
	for i := range phones {
		if phones[i].Number, err = pii.ParsePhone(phones[i].e164); err != nil {
			t.Fatal(err)
		}
	}
	return gaz, geo, phones
}

// both is a red call at Test School 1 that VHT 1 and Transporter 1 accepted.
func both(state string) board {
	return board{Incidents: []boardIncident{{ID: "0190f0c4-0000-7000-8000-000000000001", Reference: "284 262 7491", State: state,
		Ward: "Test Ward 1", WardID: "w01", Landmark: "Test School 1", LandmarkID: "w01_school",
		Alerts: []boardAlert{{Responder: "VHT 1", Role: "vht", Outcome: "accepted"}, {Responder: "VHT 2", Role: "vht", Outcome: "stood_down"},
			{Responder: "Transporter 1", Role: "transporter", Outcome: "accepted"}}}}}
}

func texts(out []outgoing, to string) []string {
	var s []string
	for _, m := range out {
		if m.to == to {
			s = append(s, m.text)
		}
	}
	return s
}

const (
	vht1 = "+256772000201"
	tr1  = "+256772000204"
)

// A VHT on a basic phone: with both roles accepted and nothing shared, the
// VHT is told after the grace who is coming and to dial the menu; once they
// share, the transporter gets where they are, how far from the patient, the
// facility and the number; a later share tells the transporter again.
func TestTheTransporterIsSentWhereTheVHTIs(t *testing.T) {
	gaz, geo, phones := shareRig(t)
	c := newCoordination()
	t0 := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	b := both("assigned")
	if out := c.due(b, t0, phones, gaz, geo); len(out) != 0 {
		t.Fatalf("before the grace: %v", out)
	}
	out := c.due(b, t0.Add(shareGrace), phones, gaz, geo)
	if got := texts(out, vht1); len(got) != 1 || got[0] != "PENDING-SMS Sauti 284 262 7491: Transporter 1 is coming to help. Call +256772000204. Dial *384*3# to send them where you are." {
		t.Fatalf("the VHT's prompt: %q", got)
	}
	if len(texts(out, tr1)) != 0 {
		t.Fatalf("the transporter was told before the VHT shared: %v", out)
	}
	church := gaz.Landmarks[3] // w01_church
	c.put(b.Incidents[0].ID, vhtShare{vht: "vht-1", facility: "hc_test_1", landmark: church.ID, point: church.Point, number: phones[2].Number, via: viaUSSD})
	out = c.due(b, t0.Add(10*time.Second), phones, gaz, geo)
	want := "PENDING-SMS Sauti 284 262 7491: VHT 1 is near Test Church 1, Test Ward 1, 0.3 km from the patient. Works at Test Health Centre 1. Call +256772000201."
	if got := texts(out, tr1); len(got) != 1 || got[0] != want {
		t.Fatalf("the transporter's SMS: %q, want %q", got, want)
	}
	if len(texts(out, vht1)) != 0 {
		t.Fatalf("the VHT was told twice: %v", out)
	}
	if out := c.due(b, t0.Add(11*time.Second), phones, gaz, geo); len(out) != 0 {
		t.Fatalf("nothing new, yet %v", out)
	}
	school := gaz.Landmarks[2] // w01_school, the patient's place
	c.put(b.Incidents[0].ID, vhtShare{vht: "vht-1", facility: "hc_test_1", landmark: school.ID, point: school.Point, number: phones[2].Number, via: viaUSSD})
	out = c.due(b, t0.Add(12*time.Second), phones, gaz, geo)
	if got := texts(out, tr1); len(got) != 1 || !strings.Contains(got[0], "VHT 1 is now near Test School 1, Test Ward 1, 0.0 km from the patient.") {
		t.Fatalf("after a move: %q", got)
	}
	views := c.views(b, phones, gaz, geo)
	if len(views) != 1 || views[0].VHT != "VHT 1" || views[0].Facility != "Test Health Centre 1" || !views[0].Told || views[0].KM == nil || *views[0].KM != 0 {
		t.Fatalf("views %+v", views)
	}
	if views[0].Number.String() != "+256•••••••••" {
		t.Fatalf("a view's number shows as %q", views[0].Number.String())
	}
}

// A VHT on a smartphone shares before the transporter accepts: once they do,
// the transporter gets the details and the VHT who is coming, at once.
func TestASharedVHTIsToldWhoIsComing(t *testing.T) {
	gaz, geo, phones := shareRig(t)
	c := newCoordination()
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	b := both("assigned")
	b.Incidents[0].Alerts[2].Outcome = "pending"
	fix := location.Point{Lat: 0.3781, Lon: 32.5551}
	c.put(b.Incidents[0].ID, vhtShare{vht: "vht-1", facility: "hc_test_2", landmark: nearestLandmark(gaz, fix).ID, point: fix, number: phones[2].Number, via: viaApp})
	if out := c.due(b, now, phones, gaz, geo); len(out) != 0 {
		t.Fatalf("with no transporter yet: %v", out)
	}
	b.Incidents[0].Alerts[2].Outcome = "accepted"
	out := c.due(b, now.Add(time.Second), phones, gaz, geo)
	if got := texts(out, tr1); len(got) != 1 || !strings.Contains(got[0], "VHT 1 is near Test Clinic 1, Test Ward 1") || !strings.Contains(got[0], "Works at Test Health Centre 2.") {
		t.Fatalf("the transporter's SMS: %q", got)
	}
	if got := texts(out, vht1); len(got) != 1 || got[0] != "PENDING-SMS Sauti 284 262 7491: Transporter 1 is coming to help. Call +256772000204. They have your place and number." {
		t.Fatalf("the VHT's SMS: %q", got)
	}
}

// A closed incident, or one with a single role accepted, sends nothing.
func TestNothingIsSentOffTheJob(t *testing.T) {
	gaz, geo, phones := shareRig(t)
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	for _, b := range []board{both("closed"), both("merged"), func() board { b := both("assigned"); b.Incidents[0].Alerts[2].Outcome = "declined"; return b }()} {
		c := newCoordination()
		c.put(b.Incidents[0].ID, vhtShare{vht: "vht-1", facility: "hc_test_1", landmark: "w01_church", number: phones[2].Number})
		if out := c.due(b, now.Add(time.Minute), phones, gaz, geo); len(out) != 0 {
			t.Fatalf("%s: %v", b.Incidents[0].State, out)
		}
	}
}

// Every SMS and every menu screen fits one GSM segment and the operator's
// 160-character screen (intake.yaml), for every place and facility.
func TestShareMessagesAndScreensFit(t *testing.T) {
	gaz, geo, phones := shareRig(t)
	inc := both("assigned").Incidents[0]
	for _, l := range gaz.Landmarks {
		for _, f := range gaz.Facilities {
			s := transporterSMS(inc, "VHT 1", vhtShare{facility: f.ID, landmark: l.ID, point: l.Point, number: phones[2].Number}, true, gaz, geo)
			if n := utf8.RuneCountInString(s); n > 160 {
				t.Errorf("%d characters: %q", n, s)
			}
		}
	}
	m := &menuSession{a: &app{gaz: gaz, geo: geo}, inc: inc}
	screens := []string{m.facilityScreen().Text, m.wardScreen().Text, m.destScreen().Text}
	for _, w := range gaz.Wards {
		m.ward = w.ID
		screens = append(screens, m.landmarkScreen().Text)
		if len(m.keys) != 5 {
			t.Errorf("%s offers %d places, want its five menu landmarks", w.ID, len(m.keys))
		}
	}
	for _, s := range screens {
		if n := utf8.RuneCountInString(menuInvalid + s); n > 160 {
			t.Errorf("%d characters with the retry line: %q", n, s)
		}
	}
	if !strings.HasPrefix(m.destScreen().Text, "PENDING-RESPONDER Which facility?\n1 Test Health Centre 1") {
		t.Errorf("the nearest facility to Test School 1 is not first: %q", m.destScreen().Text)
	}
}

// A smartphone's share names a VHT, a known facility and a fix inside the
// demo's wards.
func TestShareRequestsAreChecked(t *testing.T) {
	gaz, geo, phones := shareRig(t)
	ok := shareRequest{Phone: "vht-1", Incident: "0190f0c4-0000-7000-8000-000000000001", Facility: "hc_test_1", Lat: 0.3801, Lon: 32.5574}
	if p, _, err := checkShare(ok, phones, gaz, geo); err != nil || p.Label != "VHT 1" {
		t.Fatalf("a good share: %v %v", p.Label, err)
	}
	for name, q := range map[string]shareRequest{
		"a transporter":    {Phone: "transporter-1", Incident: ok.Incident, Facility: ok.Facility, Lat: ok.Lat, Lon: ok.Lon},
		"a caller":         {Phone: "caller-a", Incident: ok.Incident, Facility: ok.Facility, Lat: ok.Lat, Lon: ok.Lon},
		"no incident":      {Phone: "vht-1", Incident: "x", Facility: ok.Facility, Lat: ok.Lat, Lon: ok.Lon},
		"unknown facility": {Phone: "vht-1", Incident: ok.Incident, Facility: "Mulago", Lat: ok.Lat, Lon: ok.Lon},
		"far away":         {Phone: "vht-1", Incident: ok.Incident, Facility: ok.Facility, Lat: 0.3476, Lon: 32.5825},
		"no fix":           {Phone: "vht-1", Incident: ok.Incident, Facility: ok.Facility},
	} {
		_, _, err := checkShare(q, phones, gaz, geo)
		var pe *progressError
		if !errors.As(err, &pe) || pe.status != http.StatusBadRequest {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The page sees every number in an SMS masked, as pii masks one.
func TestSMSNumbersAreMaskedForThePage(t *testing.T) {
	got := maskSMS("PENDING-SMS Sauti 284 262 7491: VHT 1 is near Test Church 1. Call +256772000201. Or 0772000201.")
	if strings.Contains(got, "772000201") || !strings.Contains(got, "Call +256•••••••••.") {
		t.Fatalf("%q", got)
	}
	if !strings.Contains(got, "284 262 7491") {
		t.Fatalf("the incident number was masked: %q", got)
	}
}

// nextStep is responders.js's, for the basic phone's menu.
func TestNextStepFollowsTheUI(t *testing.T) {
	arrived := func(role string) boardEvent {
		return boardEvent{Type: "responder.arrived", Payload: `{"role":"` + role + `"}`}
	}
	for _, c := range []struct {
		state  string
		events []boardEvent
		t      map[string]string
		role   string
		want   string
	}{
		{"assigned", nil, nil, "vht", stepArrived},
		{"on_scene", []boardEvent{arrived("vht")}, nil, "vht", ""},
		{"on_scene", []boardEvent{arrived("vht")}, nil, "transporter", stepArrived},
		{"on_scene", []boardEvent{arrived("transporter")}, nil, "transporter", stepDeparted},
		{"transporting", []boardEvent{arrived("transporter")}, map[string]string{"T6": "x"}, "transporter", stepAtFacility},
		{"transporting", nil, map[string]string{"T6": "x"}, "vht", ""},
		{"at_facility", []boardEvent{arrived("transporter")}, map[string]string{"T6": "x", "T7": "y"}, "transporter", ""},
		{"closed", nil, nil, "vht", ""},
	} {
		inc := boardIncident{State: c.state, Events: c.events, T: c.t}
		if inc.T == nil {
			inc.T = map[string]string{}
		}
		if got := nextStep(inc, c.role); got != c.want {
			t.Errorf("%s %s %v: %q, want %q", c.state, c.role, c.events, got, c.want)
		}
	}
}
