package respond

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/responder"
)

type fakeStore struct {
	id        uuid.UUID
	known     bool
	alert     dispatch.ResponderAlert
	hasAlert  bool
	until     time.Time
	on        bool
	accept    dispatch.AcceptResult
	accepted  []uuid.UUID
	declined  map[uuid.UUID]string
	available []bool
}

func (f *fakeStore) ResponderByPhone(context.Context, []crypto.MAC) (uuid.UUID, bool, error) {
	return f.id, f.known, nil
}
func (f *fakeStore) RecentAlertFor(context.Context, uuid.UUID) (dispatch.ResponderAlert, bool, error) {
	return f.alert, f.hasAlert, nil
}
func (f *fakeStore) AvailableUntil(context.Context, uuid.UUID) (time.Time, bool, error) {
	return f.until, f.on, nil
}
func (f *fakeStore) SetAvailability(_ context.Context, _ uuid.UUID, on bool, _ string) (responder.Period, error) {
	f.available = append(f.available, on)
	return responder.Period{From: time.Now(), Until: time.Date(2026, 11, 13, 18, 30, 0, 0, time.UTC)}, nil
}
func (f *fakeStore) AcceptAlert(_ context.Context, a, _ uuid.UUID) (dispatch.AcceptOutcome, error) {
	f.accepted = append(f.accepted, a)
	return dispatch.AcceptOutcome{Result: f.accept}, nil
}
func (f *fakeStore) DeclineAlert(_ context.Context, a, _ uuid.UUID, reason string) (string, error) {
	if f.declined == nil {
		f.declined = map[uuid.UUID]string{}
	}
	f.declined[a] = reason
	return dispatch.OutcomeDeclined, nil
}

type fakeKeys struct{}

func (fakeKeys) PhoneLookupMACs(context.Context, pii.Phone) ([]crypto.MAC, error) {
	return []crypto.MAC{{KeyID: "k", Sum: make([]byte, 32)}}, nil
}

func repoConfig(t *testing.T) (config.Bundle, location.File) {
	t.Helper()
	b, err := config.Load(os.DirFS("../../config"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile("../../config/gated/gazetteer.yaml")
	g, err := location.ParseFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	return b, g
}

func service(t *testing.T, f *fakeStore, askReasons bool) *Service {
	t.Helper()
	b, g := repoConfig(t)
	s, err := New(Config{Store: f, Keys: fakeKeys{}, Copy: b.Responder, Tool1: b.Tool1, Gazetteer: g, AskReasons: askReasons, ScreenLimit: 160})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func dial(t *testing.T, s *Service, text string) intake.Screen {
	t.Helper()
	p, _ := pii.ParsePhone("0772000301")
	sc, err := s.HandleUSSD(context.Background(), intake.USSDRequest{Provider: "at", SessionID: "S", ServiceCode: "*384*2#", Phone: p, Text: text})
	if err != nil {
		t.Fatalf("%q: %v", text, err)
	}
	if len(sc.Text) > 160 || !config.IsGSM7(sc.Text) {
		t.Fatalf("%q: screen %q exceeds one GSM-7 USSD screen", text, sc.Text)
	}
	return sc
}

func pending(role responder.Role) dispatch.ResponderAlert {
	return dispatch.ResponderAlert{AlertID: uuid.NewV7(), IncidentID: uuid.NewV7(), Role: role, Outcome: dispatch.OutcomePending,
		Facts: dispatch.MessageFacts{Reference: "1234567894", Severity: incident.SevRed, Category: "trauma", Ward: "w01", Landmark: "w01_market"}}
}

// A number that is not a registered responder hears so and nothing else.
func TestUnregisteredNumber(t *testing.T) {
	if sc := dial(t, service(t, &fakeStore{}, false), ""); !sc.End || !strings.Contains(sc.Text, "not registered") {
		t.Fatalf("%+v", sc)
	}
}

// The menu shows the pending alert (incident number, colour, category) and
// the choices; "1" accepts and gives where to go; a late accept hears
// "already taken".
func TestAcceptFromTheMenu(t *testing.T) {
	f := &fakeStore{id: uuid.NewV7(), known: true, alert: pending(responder.RoleVHT), hasAlert: true, accept: dispatch.Accepted}
	s := service(t, f, false)
	menu := dial(t, s, "")
	if menu.End || !strings.Contains(menu.Text, "123 456 7894") || !strings.Contains(menu.Text, "RED") ||
		!strings.Contains(menu.Text, "1 Accept") || !strings.Contains(menu.Text, "2 Decline") || !strings.Contains(menu.Text, "3 Availability") {
		t.Fatalf("menu %q", menu.Text)
	}
	_, g := repoConfig(t)
	w, _ := g.Ward("w01")
	done := dial(t, s, "1")
	if !done.End || !strings.Contains(done.Text, "123 456 7894") || !strings.Contains(done.Text, w.Name.EN) || len(f.accepted) != 1 {
		t.Fatalf("accepted %q %v", done.Text, f.accepted)
	}
	f.accept = dispatch.AlreadyTaken
	if sc := dial(t, s, "1"); !sc.End || !strings.Contains(sc.Text, "Already taken") {
		t.Fatalf("late accept %q", sc.Text)
	}
}

// Decision 6: before the amendment a decline asks no reason; after it, the
// closed list of the alert's role (last option Other) — scenario 10: reason
// 3 for a transporter is "Bike or fuel problem". An invalid choice
// re-prompts; the accumulated USSD text still resolves.
func TestDeclineWithAndWithoutReasons(t *testing.T) {
	f := &fakeStore{id: uuid.NewV7(), known: true, alert: pending(responder.RoleTransporter), hasAlert: true}
	if sc := dial(t, service(t, f, false), "2"); !sc.End || f.declined[f.alert.AlertID] != "" {
		t.Fatalf("protocol mode decline %q %v", sc.Text, f.declined)
	}
	f = &fakeStore{id: uuid.NewV7(), known: true, alert: pending(responder.RoleTransporter), hasAlert: true}
	s := service(t, f, true)
	why := dial(t, s, "2")
	if why.End || !strings.Contains(why.Text, "3 Bike or fuel problem") || !strings.Contains(why.Text, "6 Other") {
		t.Fatalf("reasons %q", why.Text)
	}
	if sc := dial(t, s, "2*9"); sc.End || !strings.Contains(sc.Text, "Choose a number") {
		t.Fatalf("invalid reason %q", sc.Text)
	}
	if sc := dial(t, s, "2*9*3"); !sc.End || f.declined[f.alert.AlertID] != "bike_or_fuel" {
		t.Fatalf("declined %q %v", sc.Text, f.declined)
	}
}

// Availability on and off (decision 5: 8 hours), with the end time in
// Uganda time; an invalid choice at the top re-prompts.
func TestAvailabilityFromTheMenu(t *testing.T) {
	f := &fakeStore{id: uuid.NewV7(), known: true}
	s := service(t, f, false)
	top := dial(t, s, "")
	if top.End || strings.Contains(top.Text, "1 Accept") || !strings.Contains(top.Text, "3 Availability") {
		t.Fatalf("no-alert menu %q", top.Text)
	}
	if sc := dial(t, s, "3"); sc.End || !strings.Contains(sc.Text, "off duty") {
		t.Fatalf("availability menu %q", sc.Text)
	}
	if sc := dial(t, s, "3*1"); !sc.End || !strings.Contains(sc.Text, "21:30") {
		t.Fatalf("on %q", sc.Text)
	}
	if sc := dial(t, s, "7*3*2"); !sc.End || !strings.Contains(sc.Text, "off duty") || len(f.available) != 2 || f.available[1] {
		t.Fatalf("invalid then off %q %v", sc.Text, f.available)
	}
	// "1" with no alert at all is invalid, not an accept.
	if sc := dial(t, s, "1"); sc.End || len(f.accepted) != 0 {
		t.Fatalf("accept with no alert %q", sc.Text)
	}
}
