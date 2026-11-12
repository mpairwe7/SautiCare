package channels

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/pii"
)

const (
	responderNumber = "+256772000301"
	callerNumber    = "+256772999888"
)

// fakeDir hands out sealed numbers whose "ciphertext" is the number itself,
// bound to the row the real store would bind it to.
type fakeDir struct {
	facts    dispatch.MessageFacts
	caller   bool
	facility string
}

func (d fakeDir) ResponderPhone(_ context.Context, id uuid.UUID) (incident.SealedContact, error) {
	return incident.SealedContact{Sealed: crypto.Sealed{KeyID: "k", Ciphertext: []byte(responderNumber)},
		AAD: crypto.AAD{Store: "operational", Table: "responders", Column: "phone", RowID: id.String()}}, nil
}

func (d fakeDir) CallerContact(_ context.Context, id uuid.UUID) (incident.SealedContact, bool, error) {
	return incident.SealedContact{Sealed: crypto.Sealed{KeyID: "k", Ciphertext: []byte(callerNumber)},
		AAD: crypto.AAD{Store: "operational", Table: "sessions", Column: "phone", RowID: id.String()}}, d.caller, nil
}

func (d fakeDir) MessageFacts(context.Context, uuid.UUID) (dispatch.MessageFacts, error) {
	return d.facts, nil
}

func (d fakeDir) NoticeFacility(context.Context, uuid.UUID, string) (string, error) {
	return d.facility, nil
}

// fakeOpener checks the AAD names the row the number belongs to.
type fakeOpener struct{ t *testing.T }

func (o fakeOpener) Open(_ context.Context, s crypto.Sealed, aad crypto.AAD) ([]byte, error) {
	if aad.Store != "operational" || aad.RowID == "" {
		o.t.Fatalf("unbound AAD %+v", aad)
	}
	return s.Ciphertext, nil
}

type sent struct{ to, text string }

type fakeSender struct {
	got []sent
	err error
}

func (f *fakeSender) Send(_ context.Context, to pii.Phone, text string) (string, error) {
	f.got = append(f.got, sent{to.E164(), text})
	if f.err != nil {
		return "", f.err
	}
	return "ATXid_1", nil
}

func repo(t *testing.T) (config.Bundle, location.File) {
	t.Helper()
	b, err := config.Load(os.DirFS("../../../config"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../../config/gated/gazetteer.yaml")
	if err != nil {
		t.Fatal(err)
	}
	g, err := location.ParseFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	return b, g
}

func newSMS(t *testing.T, amended bool, dir fakeDir, sender *fakeSender) *SMS {
	t.Helper()
	b, g := repo(t)
	s, err := NewSMS(SMSConfig{Directory: dir, Opener: fakeOpener{t}, Sender: sender, Templates: b.SMS, Tool1: b.Tool1,
		Gazetteer: g, Amended: amended})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var facts = dispatch.MessageFacts{Reference: "1234567894", Severity: incident.SevRed, Category: "trauma", Ward: "w01", Landmark: "w01_l1", Language: "lg"}

// Decision 1 (behind the amendment gate) and hard constraint 6: in protocol
// mode a responder's alert carries the incident number, colour and category
// only; in amended mode it adds the ward and landmark names. Never the
// caller's number.
func TestAlertSMS(t *testing.T) {
	b, g := repo(t)
	for _, amended := range []bool{false, true} {
		s := &fakeSender{}
		ref, err := newSMS(t, amended, fakeDir{facts: facts}, s).Deliver(context.Background(),
			dispatch.Delivery{AttemptID: uuid.NewV7(), AlertID: uuid.NewV7(), IncidentID: uuid.NewV7(), ResponderID: uuid.NewV7(),
				Role: "vht", Purpose: dispatch.PurposeAlert, Channel: dispatch.ChannelSMS})
		if err != nil || ref != "ATXid_1" || len(s.got) != 1 || s.got[0].to != responderNumber {
			t.Fatalf("amended=%v: %q %v %+v", amended, ref, err, s.got)
		}
		text := s.got[0].text
		ward, _ := g.Ward("w01")
		if !strings.Contains(text, "123 456 7894") || !strings.Contains(text, "RED") || strings.Contains(text, callerNumber) {
			t.Fatalf("amended=%v: %q", amended, text)
		}
		if hasWard := strings.Contains(text, ward.Name.EN); hasWard != amended {
			t.Fatalf("amended=%v: ward in SMS = %v: %q", amended, hasWard, text)
		}
		want, _ := b.SMS.Render(map[bool]string{false: config.SMSAlert, true: config.SMSAlertAmended}[amended], "en", config.SMSValues{})
		if !strings.HasPrefix(text, strings.SplitN(want, " ", 2)[0]) {
			t.Fatalf("not the %v template: %q", amended, text)
		}
	}
}

// A stand-down names the incident only.
func TestStandDownSMS(t *testing.T) {
	s := &fakeSender{}
	if _, err := newSMS(t, false, fakeDir{facts: facts}, s).Deliver(context.Background(), dispatch.Delivery{AttemptID: uuid.NewV7(),
		ResponderID: uuid.NewV7(), IncidentID: uuid.NewV7(), Purpose: dispatch.PurposeStandDown, Channel: dispatch.ChannelSMS}); err != nil {
		t.Fatal(err)
	}
	if len(s.got) != 1 || !strings.Contains(s.got[0].text, "123 456 7894") || strings.Contains(s.got[0].text, "RED") {
		t.Fatalf("stand-down %+v", s.got)
	}
}

// Caller notices go to the caller's number in the caller's language: help
// on the way (no responder name or number), and the facility's name and
// public number once it acknowledged.
func TestCallerNotices(t *testing.T) {
	_, g := repo(t)
	s := &fakeSender{}
	sms := newSMS(t, true, fakeDir{facts: facts, caller: true, facility: "hc_test_1"}, s)
	for _, kind := range []string{dispatch.CallerHelperOnTheWay, dispatch.CallerForwarded} {
		if _, err := sms.Notify(context.Background(), dispatch.Notice{AttemptID: uuid.NewV7(), IncidentID: uuid.NewV7(), Kind: kind,
			Channel: dispatch.ChannelSMS}); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	fac := g.Facilities[0]
	if len(s.got) != 2 || s.got[0].to != callerNumber || strings.Contains(s.got[0].text, responderNumber) ||
		!strings.Contains(s.got[1].text, fac.Phone) || !strings.Contains(s.got[1].text, fac.Name.LG) {
		t.Fatalf("notices %+v", s.got)
	}
	// A caller who withheld the number and gave none cannot be texted:
	// nothing is sent and the attempt fails (the coordinator calls back).
	none := &fakeSender{}
	_, err := newSMS(t, false, fakeDir{facts: facts}, none).Notify(context.Background(), dispatch.Notice{IncidentID: uuid.NewV7(),
		Kind: dispatch.CallerHelperOnTheWay, Channel: dispatch.ChannelSMS})
	if !errors.Is(err, dispatch.ErrRejected) || len(none.got) != 0 {
		t.Fatalf("no number: %v %+v", err, none.got)
	}
}

// The provider's refusal is a failed attempt; anything else is unknown.
func TestSMSErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		err      error
		rejected bool
	}{{dispatch.ErrRejected, true}, {errors.New("timeout"), false}} {
		_, err := newSMS(t, false, fakeDir{facts: facts}, &fakeSender{err: tc.err}).Deliver(context.Background(),
			dispatch.Delivery{ResponderID: uuid.NewV7(), Purpose: dispatch.PurposeAlert})
		if err == nil || errors.Is(err, dispatch.ErrRejected) != tc.rejected {
			t.Errorf("%v: %v", tc.err, err)
		}
	}
}

// Review focus 4: the expiry reminder states the end time in Uganda time.
func TestAvailabilityReminderSMS(t *testing.T) {
	s := &fakeSender{}
	until := time.Date(2026, 11, 13, 14, 5, 0, 0, time.UTC) // 17:05 EAT
	if _, err := newSMS(t, false, fakeDir{facts: facts}, s).Remind(context.Background(), dispatch.Reminder{ResponderID: uuid.NewV7(), Until: until}); err != nil {
		t.Fatal(err)
	}
	if len(s.got) != 1 || !strings.Contains(s.got[0].text, "17:05") || s.got[0].to != responderNumber {
		t.Fatalf("reminder %+v", s.got)
	}
}
