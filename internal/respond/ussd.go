// Package respond is the responder's own menu (Build Prompt §10.4, §10.5):
// on the responder USSD service code (pull; USSD push is disabled, O2) and,
// from Task 10, by voice. A registered responder, recognised by the keyed
// hash of their number, answers their most recent alert (1 Accept, 2 Decline
// — with decision 6's closed list of reasons once the amendment allows it)
// and switches availability on for 8 hours or off. Every answer goes through
// the dispatch functions of the operational store, so a USSD accept races a
// voice accept safely: the first acceptance per role wins.
package respond

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/responder"
)

// Store is the operational store as the responder menu sees it (op_intake,
// op_ivr): only the caller's own responder record, alert and availability.
type Store interface {
	ResponderByPhone(ctx context.Context, macs []crypto.MAC) (uuid.UUID, bool, error)
	MenuAlert(ctx context.Context, sessionKey []byte, responderID uuid.UUID) (dispatch.ResponderAlert, bool, error)
	AvailableUntil(ctx context.Context, responderID uuid.UUID) (time.Time, bool, error)
	SetAvailability(ctx context.Context, responderID uuid.UUID, on bool, source string) (responder.Period, error)
	AcceptAlert(ctx context.Context, alert, responderID uuid.UUID) (dispatch.AcceptOutcome, error)
	DeclineAlert(ctx context.Context, alert, responderID uuid.UUID, reason string) (string, error)
}

// Keys computes the lookup hashes of a number (crypto.Identifiers).
type Keys interface {
	PhoneLookupMACs(ctx context.Context, p pii.Phone) ([]crypto.MAC, error)
}

// Config wires the service.
type Config struct {
	Store       Store
	Keys        Keys
	Copy        config.Responder
	Tool1       config.Tool1 // category labels
	Gazetteer   location.File
	AskReasons  bool // decision 6 approved (dispatch.mode amended)
	ScreenLimit int  // characters per USSD screen
	// Language of the menu until the registry records each responder's (M5).
	Language string
}

// Service runs the responder menu.
type Service struct{ c Config }

// New validates the wiring and that every screen fits the limit in GSM-7.
func New(c Config) (*Service, error) {
	if c.Store == nil || c.Keys == nil || c.ScreenLimit <= 0 {
		return nil, errors.New("respond: a store, keys and a screen limit are required")
	}
	if c.Language == "" {
		c.Language = "en"
	}
	s := &Service{c: c}
	return s, s.checkScreens()
}

// eat is Uganda's time (UTC+3): times on screens are local.
var eat = time.FixedZone("EAT", 3*60*60)

type state int

const (
	atTop state = iota
	atReasons
	atAvailability
)

// HandleUSSD answers one callback. USSD is stateless: the screen is
// recomputed from the accumulated text, an invalid choice re-prompting in
// place, so a retried callback gets the same answer.
func (s *Service) HandleUSSD(ctx context.Context, r intake.USSDRequest) (intake.Screen, error) {
	lang := s.c.Language
	end := func(name string, v map[string]string) (intake.Screen, error) {
		return intake.Screen{Text: s.fill(s.c.Copy.Text(name, lang), v), End: true}, nil
	}
	if r.Phone.IsZero() {
		return end("not_registered", nil)
	}
	macs, err := s.c.Keys.PhoneLookupMACs(ctx, r.Phone)
	if err != nil {
		return intake.Screen{}, err
	}
	id, ok, err := s.c.Store.ResponderByPhone(ctx, macs)
	if err != nil {
		return intake.Screen{}, err
	}
	if !ok {
		return end("not_registered", nil)
	}
	// The session's alert, pinned at its first screen: a newer alert cannot
	// redirect an answer already under way.
	alert, hasAlert, err := s.c.Store.MenuAlert(ctx, sessionKey(r), id)
	if err != nil {
		return intake.Screen{}, err
	}
	pending := hasAlert && alert.Outcome == dispatch.OutcomePending
	at, invalid := atTop, false
	for _, in := range tokens(r.Text) {
		invalid = false
		switch at {
		case atTop:
			switch {
			case in == "1" && pending:
				return s.accept(ctx, id, alert)
			case in == "2" && pending && !s.c.AskReasons:
				return s.decline(ctx, id, alert, "")
			case in == "2" && pending:
				at = atReasons
			case in == "3":
				at = atAvailability
			default:
				invalid = true
			}
		case atReasons:
			reasons := s.c.Copy.DeclineReasons[string(alert.Role)]
			if k, err := strconv.Atoi(in); err == nil && k >= 1 && k <= len(reasons) {
				return s.decline(ctx, id, alert, reasons[k-1].Code)
			}
			invalid = true
		case atAvailability:
			switch in {
			case "1":
				p, err := s.c.Store.SetAvailability(ctx, id, true, "ussd")
				if err != nil {
					return intake.Screen{}, err
				}
				return end("available_until", map[string]string{"until": p.Until.In(eat).Format("15:04")})
			case "2":
				if _, err := s.c.Store.SetAvailability(ctx, id, false, "ussd"); err != nil {
					return intake.Screen{}, err
				}
				return end("switched_off", nil)
			default:
				invalid = true
			}
		}
	}
	switch at {
	case atReasons:
		return intake.Screen{Text: s.reasonsScreen(alert.Role, invalid, lang)}, nil
	case atAvailability:
		until, on, err := s.c.Store.AvailableUntil(ctx, id)
		if err != nil {
			return intake.Screen{}, err
		}
		return intake.Screen{Text: s.availabilityScreen(until, on, invalid, lang)}, nil
	}
	return intake.Screen{Text: s.topScreen(alert, pending, hasAlert && alert.Assigned, invalid, lang)}, nil
}

// sessionKey identifies a USSD session (provider and its session ID) without
// storing the provider's identifier.
func sessionKey(r intake.USSDRequest) []byte {
	h := sha256.Sum256([]byte(r.Provider + "\x00" + r.SessionID))
	return h[:]
}

func tokens(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(text, "*")
}

func (s *Service) accept(ctx context.Context, id uuid.UUID, a dispatch.ResponderAlert) (intake.Screen, error) {
	out, err := s.c.Store.AcceptAlert(ctx, a.AlertID, id)
	if err != nil {
		return intake.Screen{}, err
	}
	name := map[dispatch.AcceptResult]string{dispatch.Accepted: "accepted", dispatch.AlreadyTaken: "already_taken",
		dispatch.AlreadyAssigned: "already_assigned"}[out.Result]
	if name == "" {
		name = "no_longer_needed"
	}
	v := s.values(a.Facts, s.c.Language)
	return intake.Screen{Text: s.fill(s.c.Copy.Text(name, s.c.Language), v), End: true}, nil
}

func (s *Service) decline(ctx context.Context, id uuid.UUID, a dispatch.ResponderAlert, reason string) (intake.Screen, error) {
	out, err := s.c.Store.DeclineAlert(ctx, a.AlertID, id, reason)
	if err != nil {
		return intake.Screen{}, err
	}
	name := "declined"
	if out != dispatch.OutcomeDeclined {
		name = "no_longer_needed"
	}
	return intake.Screen{Text: s.fill(s.c.Copy.Text(name, s.c.Language), s.values(a.Facts, s.c.Language)), End: true}, nil
}

// values are what a screen may show about the alert.
func (s *Service) values(f dispatch.MessageFacts, lang string) map[string]string {
	// Every placeholder is filled: an unknown ward or a free-text landmark
	// (never shown) renders empty.
	v := map[string]string{"ref": f.Reference.Spoken(), "colour": strings.ToUpper(string(f.Severity)), "category": f.Category,
		"ward": "", "landmark": ""}
	for _, o := range s.c.Tool1.Category.Options {
		if o.Value == f.Category {
			v["category"] = text(o.Label, lang)
		}
	}
	if w, ok := s.c.Gazetteer.Ward(f.Ward); ok {
		v["ward"] = names(w.Name, lang)
	}
	for _, l := range s.c.Gazetteer.Landmarks {
		if l.ID == f.Landmark {
			v["landmark"] = names(l.Name, lang)
		}
	}
	return v
}

func text(t config.Text, lang string) string {
	if t[lang] != "" {
		return t[lang]
	}
	return t["en"]
}

func names(n location.Names, lang string) string {
	if lang == "lg" && n.LG != "" {
		return n.LG
	}
	return n.EN
}

func (s *Service) fill(t string, v map[string]string) string {
	for k, val := range v {
		t = strings.ReplaceAll(t, "{"+k+"}", val)
	}
	return t
}

func (s *Service) topScreen(a dispatch.ResponderAlert, pending, assigned, invalid bool, lang string) string {
	var b strings.Builder
	if invalid {
		b.WriteString(s.c.Copy.Text("invalid", lang) + "\n")
	}
	switch {
	case pending:
		b.WriteString(s.fill(s.c.Copy.Text("alert", lang), s.values(a.Facts, lang)))
		fmt.Fprintf(&b, "\n1 %s\n2 %s", s.c.Copy.Text("accept", lang), s.c.Copy.Text("decline", lang))
	case assigned: // where to go, again (an accept by voice, or an earlier session)
		b.WriteString(s.fill(s.c.Copy.Text("assigned", lang), s.values(a.Facts, lang)))
	default:
		b.WriteString(s.c.Copy.Text("no_alert", lang))
	}
	fmt.Fprintf(&b, "\n3 %s", s.c.Copy.Text("availability", lang))
	return b.String()
}

func (s *Service) reasonsScreen(role responder.Role, invalid bool, lang string) string {
	head := s.c.Copy.Text("decline_why", lang)
	if invalid {
		head = s.c.Copy.Text("invalid", lang)
	}
	var b strings.Builder
	b.WriteString(head)
	for i, r := range s.c.Copy.DeclineReasons[string(role)] {
		fmt.Fprintf(&b, "\n%d %s", i+1, text(r.Label, lang))
	}
	return b.String()
}

func (s *Service) availabilityScreen(until time.Time, on, invalid bool, lang string) string {
	head := s.c.Copy.Text("switched_off", lang)
	if on {
		head = s.fill(s.c.Copy.Text("available_until", lang), map[string]string{"until": until.In(eat).Format("15:04")})
	}
	if invalid {
		head = s.c.Copy.Text("invalid", lang)
	}
	return fmt.Sprintf("%s\n1 %s\n2 %s", head, s.c.Copy.Text("switch_on", lang), s.c.Copy.Text("switch_off", lang))
}

// checkScreens renders every screen with the longest values in every
// language and refuses one that leaves GSM-7 or exceeds the limit.
func (s *Service) checkScreens() error {
	long := func(xs []string) string {
		out := ""
		for _, x := range xs {
			if len(x) > len(out) {
				out = x
			}
		}
		return out
	}
	var problems []string
	for _, lang := range s.c.Copy.Languages {
		var cats, wards, marks []string
		for _, o := range s.c.Tool1.Category.Options {
			cats = append(cats, text(o.Label, lang))
		}
		for _, w := range s.c.Gazetteer.Wards {
			wards = append(wards, names(w.Name, lang))
		}
		for _, l := range s.c.Gazetteer.Landmarks {
			marks = append(marks, names(l.Name, lang))
		}
		v := map[string]string{"ref": "123 456 7890", "colour": "YELLOW", "category": long(cats), "ward": long(wards),
			"landmark": long(marks), "until": "23:59"}
		screens := map[string]string{}
		for _, invalid := range []bool{false, true} {
			for _, kind := range []string{"none", "alert", "assigned"} {
				t := s.topScreen(dispatch.ResponderAlert{}, kind == "alert", kind == "assigned", invalid, lang)
				if kind != "none" {
					t = strings.Replace(t, s.fill(s.c.Copy.Text(kind, lang), s.values(dispatch.MessageFacts{}, lang)),
						s.fill(s.c.Copy.Text(kind, lang), v), 1)
				}
				screens[fmt.Sprintf("top/%s/%v", kind, invalid)] = t
			}
			for _, role := range responder.Roles() {
				screens[fmt.Sprintf("reasons/%s/%v", role, invalid)] = s.reasonsScreen(role, invalid, lang)
			}
			screens[fmt.Sprintf("availability/%v", invalid)] = s.availabilityScreen(time.Date(2026, 1, 1, 20, 59, 0, 0, time.UTC), true, invalid, lang)
		}
		for name := range s.c.Copy.Menu {
			screens["end/"+name] = s.fill(s.c.Copy.Text(name, lang), v)
		}
		for _, name := range slices.Sorted(mapKeys(screens)) {
			t := screens[name]
			if len(t) > s.c.ScreenLimit || !config.IsGSM7(t) {
				problems = append(problems, fmt.Sprintf("%s/%s (%d characters)", name, lang, len(t)))
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("respond: screens over %d characters or outside GSM-7: %s", s.c.ScreenLimit, strings.Join(problems, ", "))
	}
	return nil
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}
