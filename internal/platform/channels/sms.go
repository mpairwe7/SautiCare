// Package channels delivers dispatch's messages over the network channels
// (Build Prompt §10.4; ADR-0027): SMS now, voice from Task 10. It opens the
// number to send to (a responder's, or the caller's for caller notices —
// never one to the other) and fills the approved template from the
// incident's coded facts. What an SMS may carry is fixed by
// config/gated/sms.yaml (hard constraint 6, decision 1).
package channels

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/pii"
)

// Directory reads what a message needs (the operational store, op_dispatch).
type Directory interface {
	ResponderPhone(ctx context.Context, responderID uuid.UUID) (incident.SealedContact, error)
	CallerContact(ctx context.Context, incidentID uuid.UUID) (incident.SealedContact, bool, error)
	MessageFacts(ctx context.Context, incidentID uuid.UUID) (dispatch.MessageFacts, error)
	NoticeFacility(ctx context.Context, incidentID uuid.UUID, kind string) (string, error)
}

// Opener opens a sealed identifier (crypto.Envelope).
type Opener interface {
	Open(ctx context.Context, s crypto.Sealed, aad crypto.AAD) ([]byte, error)
}

// Sender sends one SMS and returns the provider's message ID; a refusal
// (nothing sent) wraps dispatch.ErrRejected.
type Sender interface {
	Send(ctx context.Context, to pii.Phone, text string) (string, error)
}

// SMSConfig wires the SMS channel.
type SMSConfig struct {
	Directory Directory
	Opener    Opener
	Sender    Sender
	Templates config.SMS
	Tool1     config.Tool1 // category labels
	Gazetteer location.File
	Amended   bool // decision 1 approved: ward and landmark in alert SMS
	// ResponderLanguage is the language of responder SMS until the registry
	// records each responder's (M5); "en" when empty.
	ResponderLanguage string
}

// SMS is the SMS channel: dispatch.Deliverer for alerts and stand-downs,
// and the deliverer of caller notices and expiry reminders.
type SMS struct{ c SMSConfig }

// NewSMS validates the wiring.
func NewSMS(c SMSConfig) (*SMS, error) {
	if c.Directory == nil || c.Opener == nil || c.Sender == nil || len(c.Templates.Templates) == 0 {
		return nil, errors.New("channels: SMS needs a directory, an opener, a sender and templates")
	}
	if c.ResponderLanguage == "" {
		c.ResponderLanguage = "en"
	}
	s := &SMS{c: c}
	if err := s.checkNames(); err != nil {
		return nil, err
	}
	return s, nil
}

// checkNames refuses names that would not fit one GSM-7 segment: every ward,
// landmark and facility name and category label in force must be GSM 03.38,
// and every template must fit with the longest of them (hard constraint 6
// keeps an SMS to one segment; a split or re-encoded message is never sent).
func (s *SMS) checkNames() error {
	var problems []string
	gsm := func(kind, id, lang, text string) {
		if !config.IsGSM7(text) {
			problems = append(problems, fmt.Sprintf("%s %s (%s) leaves the GSM 03.38 basic character set", kind, id, lang))
		}
	}
	longest := map[string]config.SMSValues{}
	for _, lang := range s.c.Templates.Languages {
		v := config.SMSValues{Ref: "123 456 7890", Colour: "YELLOW", Time: "23:59"}
		keep := func(cur *string, x string) {
			if len(x) > len(*cur) {
				*cur = x
			}
		}
		for _, o := range s.c.Tool1.Category.Options {
			gsm("category", o.Value, lang, label(o.Label, lang))
			keep(&v.Category, label(o.Label, lang))
		}
		for _, w := range s.c.Gazetteer.Wards {
			gsm("ward", w.ID, lang, name(w.Name, lang))
			keep(&v.Ward, name(w.Name, lang))
		}
		for _, l := range s.c.Gazetteer.Landmarks {
			gsm("landmark", l.ID, lang, name(l.Name, lang))
			keep(&v.Landmark, name(l.Name, lang))
		}
		for _, f := range s.c.Gazetteer.Facilities {
			gsm("facility", f.ID, lang, name(f.Name, lang))
			keep(&v.FacilityName, name(f.Name, lang))
			keep(&v.FacilityPhone, f.Phone)
		}
		longest[lang] = v
	}
	if len(problems) > 0 {
		return errors.New("channels: SMS: " + strings.Join(problems, "; "))
	}
	if err := s.c.Templates.CheckFits(func(lang string) config.SMSValues { return longest[lang] }); err != nil {
		return fmt.Errorf("channels: %w", err)
	}
	return nil
}

// eat is Uganda's time (UTC+3, no daylight saving): times in SMS are local.
var eat = time.FixedZone("EAT", 3*60*60)

func (s *SMS) open(ctx context.Context, c incident.SealedContact) (pii.Phone, error) {
	raw, err := s.c.Opener.Open(ctx, c.Sealed, c.AAD)
	if err != nil {
		return pii.Phone{}, fmt.Errorf("channels: open number: %w", err)
	}
	p, err := pii.ParsePhone(string(raw))
	if err != nil {
		return pii.Phone{}, fmt.Errorf("%w: the stored number is not a phone number", dispatch.ErrRejected)
	}
	return p, nil
}

func (s *SMS) send(ctx context.Context, to pii.Phone, name, lang string, v config.SMSValues) (string, error) {
	text, err := s.c.Templates.Render(name, lang, v)
	if err != nil {
		return "", fmt.Errorf("%w: %w", dispatch.ErrRejected, err)
	}
	return s.c.Sender.Send(ctx, to, text)
}

// values fills what a template may show from the incident's codes.
func (s *SMS) values(f dispatch.MessageFacts, lang string) config.SMSValues {
	v := config.SMSValues{Ref: f.Reference.Spoken(), Colour: strings.ToUpper(string(f.Severity)), Category: f.Category}
	for _, o := range s.c.Tool1.Category.Options {
		if o.Value == f.Category {
			v.Category = label(o.Label, lang)
		}
	}
	if w, ok := s.c.Gazetteer.Ward(f.Ward); ok {
		v.Ward = name(w.Name, lang)
	}
	for _, l := range s.c.Gazetteer.Landmarks {
		if l.ID == f.Landmark {
			v.Landmark = name(l.Name, lang)
		}
	}
	return v
}

func label(t config.Text, lang string) string {
	if t[lang] != "" {
		return t[lang]
	}
	return t["en"]
}

func name(n location.Names, lang string) string {
	if lang == "lg" && n.LG != "" {
		return n.LG
	}
	return n.EN
}

// Deliver sends an alert or a stand-down to a responder (dispatch.Deliverer).
func (s *SMS) Deliver(ctx context.Context, d dispatch.Delivery) (string, error) {
	contact, err := s.c.Directory.ResponderPhone(ctx, d.ResponderID)
	if err != nil {
		return "", err
	}
	to, err := s.open(ctx, contact)
	if err != nil {
		return "", err
	}
	f, err := s.c.Directory.MessageFacts(ctx, d.IncidentID)
	if err != nil {
		return "", err
	}
	lang := s.c.ResponderLanguage
	v := s.values(f, lang)
	switch d.Purpose {
	case dispatch.PurposeStandDown:
		return s.send(ctx, to, config.SMSStandDown, lang, config.SMSValues{Ref: v.Ref})
	case dispatch.PurposeAlert:
		if s.c.Amended {
			return s.send(ctx, to, config.SMSAlertAmended, lang, v)
		}
		v.Ward, v.Landmark = "", "" // decision 1: no location in SMS before the amendment
		return s.send(ctx, to, config.SMSAlert, lang, v)
	}
	return "", fmt.Errorf("%w: unknown purpose %q", dispatch.ErrRejected, d.Purpose)
}

// Notify sends a caller notice to the caller's number, in the caller's
// language. A caller who withheld the number and gave none cannot be texted:
// the attempt fails and the coordinator calls back.
func (s *SMS) Notify(ctx context.Context, n dispatch.Notice) (string, error) {
	contact, ok, err := s.c.Directory.CallerContact(ctx, n.IncidentID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%w: the caller left no number", dispatch.ErrRejected)
	}
	to, err := s.open(ctx, contact)
	if err != nil {
		return "", err
	}
	f, err := s.c.Directory.MessageFacts(ctx, n.IncidentID)
	if err != nil {
		return "", err
	}
	v := config.SMSValues{Ref: f.Reference.Spoken()}
	switch n.Kind {
	case dispatch.CallerHelperOnTheWay:
		return s.send(ctx, to, config.SMSHelperOnTheWay, f.Language, v)
	case dispatch.CallerForwarded:
		id, err := s.c.Directory.NoticeFacility(ctx, n.IncidentID, n.Kind)
		if err != nil {
			return "", err
		}
		for _, fac := range s.c.Gazetteer.Facilities {
			if fac.ID == id {
				v.FacilityName, v.FacilityPhone = name(fac.Name, f.Language), fac.Phone
				return s.send(ctx, to, config.SMSForwardedToFacility, f.Language, v)
			}
		}
		return "", fmt.Errorf("%w: facility %q is not in the gazetteer in force", dispatch.ErrRejected, id)
	}
	return "", fmt.Errorf("%w: unknown notice %q", dispatch.ErrRejected, n.Kind)
}

// Remind sends the availability-expiry reminder (review focus 4).
func (s *SMS) Remind(ctx context.Context, r dispatch.Reminder) (string, error) {
	contact, err := s.c.Directory.ResponderPhone(ctx, r.ResponderID)
	if err != nil {
		return "", err
	}
	to, err := s.open(ctx, contact)
	if err != nil {
		return "", err
	}
	return s.send(ctx, to, config.SMSAvailabilityExpiring, s.c.ResponderLanguage,
		config.SMSValues{Time: r.Until.In(eat).Format("15:04")})
}

// NotifyFacility sends the forwarded facility its SMS (scenario 7): to its
// public number, in English (a facility's working language), with the
// incident number, colour, category, ward and landmark.
func (s *SMS) NotifyFacility(ctx context.Context, incidentID uuid.UUID, facilityID string) (string, error) {
	for _, fac := range s.c.Gazetteer.Facilities {
		if fac.ID != facilityID {
			continue
		}
		to, err := pii.ParsePhone(fac.Phone)
		if err != nil {
			return "", fmt.Errorf("%w: facility %s has no usable number", dispatch.ErrRejected, facilityID)
		}
		f, err := s.c.Directory.MessageFacts(ctx, incidentID)
		if err != nil {
			return "", err
		}
		return s.send(ctx, to, config.SMSFacilityForward, "en", s.values(f, "en"))
	}
	return "", fmt.Errorf("%w: facility %q is not in the gazetteer in force", dispatch.ErrRejected, facilityID)
}
