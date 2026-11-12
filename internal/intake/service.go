// Package intake runs Data Collection Tool 1 (Build Prompt §10.1) over USSD
// and IVR: the incident and its reference exist from the first callback,
// every answer is persisted the moment it is given, severity is classified by
// the signed triage table after screen 5, and the location is resolved to a
// 500 m zone at the trigger screen (T2). A redial within the resume window
// continues the same incident on either channel. Channels only translate;
// the Machine decides (Build Prompt §7).
package intake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/triage"
)

// USSDRequest is one USSD callback, provider-neutral.
type USSDRequest struct {
	Provider    string // "at" (Africa's Talking) or "ucc" (dedicated code route)
	SessionID   string // provider session ID
	ServiceCode string
	NetworkCode string
	Text        string // all input so far, '*'-separated
	Phone       pii.Phone
}

// Screen is the reply: Text, and whether the session ends.
type Screen struct {
	Text string
	End  bool
}

// Step is the stored reply to one USSD callback. MAC is the keyed hash of the
// callback's input (StepMessage), never a plain hash: the input can hold a
// callback number (ADR-0021). ConsumedLen is the length of the text the step
// processed, so the next callback's new answer is text[ConsumedLen+1:], which
// keeps a '*' typed inside a free-text answer.
type Step struct {
	Screen      Screen
	Kind        Kind // the prompt shown; the next callback's input answers it
	ConsumedLen int
	MAC         crypto.MAC
	IncidentID  uuid.UUID
}

// StepStore persists the reply to each (session, input) so a retried
// callback returns the same screen without a second write.
type StepStore interface {
	// Lookup finds the step stored under any of macs (current and previous
	// key period).
	Lookup(ctx context.Context, provider, sessionID string, macs []crypto.MAC) (Step, bool, error)
	// Latest returns the session's furthest step.
	Latest(ctx context.Context, provider, sessionID string) (Step, bool, error)
	// Save stores st unless a step with its MAC exists; it returns the stored
	// screen, so concurrent duplicates answer identically.
	Save(ctx context.Context, provider, sessionID string, st Step) (Screen, error)
}

// StepKeys computes keyed step hashes (crypto.Identifiers).
type StepKeys interface {
	StepMAC(ctx context.Context, msg []byte) (crypto.MAC, error)
	StepLookupMACs(ctx context.Context, msg []byte) ([]crypto.MAC, error)
}

// StepMessage is the unambiguous encoding of one callback input that a step
// MAC covers: each part as a netstring ("<length>:<bytes>,").
func StepMessage(provider, sessionID, text string) []byte {
	var b []byte
	for _, part := range []string{provider, sessionID, text} {
		b = strconv.AppendInt(b, int64(len(part)), 10)
		b = append(b, ':')
		b = append(b, part...)
		b = append(b, ',')
	}
	return b
}

// Incidents is what intake needs from the incident context
// (incident.Service).
type Incidents interface {
	StartSession(ctx context.Context, cmd incident.StartSession) (incident.Opened, error)
	ResumeSession(ctx context.Context, cmd incident.ResumeSession) (incident.Opened, error)
	Append(ctx context.Context, p incident.AppendParams) (incident.Appended, error)
	StoreIdentifier(ctx context.Context, incidentID uuid.UUID, kind string, plaintext []byte, idemKey string) (uuid.UUID, error)
}

// Reader reads incident projections and finds a caller's resumable incident
// (the operational store adapter).
type Reader interface {
	Projection(ctx context.Context, id uuid.UUID) (incident.Projection, error)
	FindResumable(ctx context.Context, macs []crypto.MAC, window time.Duration) (uuid.UUID, bool, error)
}

// Keys computes keyed hashes for step keys and redial lookup
// (crypto.Identifiers).
type Keys interface {
	StepKeys
	PhoneLookupMACs(ctx context.Context, p pii.Phone) ([]crypto.MAC, error)
}

// Classifier is the signed triage decision table (triage.Engine).
type Classifier interface {
	Classify(in triage.Inputs) triage.Result
}

// Matcher fuzzy-matches free text to a ward's landmarks (location.Gazetteer).
type Matcher interface {
	Match(ctx context.Context, version, wardID, normalised string) (location.Match, error)
}

// Deps wires the intake service.
type Deps struct {
	Incidents     Incidents
	Reader        Reader
	Steps         StepStore
	Keys          Keys
	Machine       *Machine
	Triage        Classifier
	Gazetteer     location.File
	Matcher       Matcher
	ResumeWindow  time.Duration
	MinSimilarity float64
}

// Service runs Tool 1 over USSD (and, from Part C, IVR).
type Service struct{ d Deps }

// New validates and wires the service.
func New(d Deps) (*Service, error) {
	if d.Incidents == nil || d.Reader == nil || d.Steps == nil || d.Keys == nil || d.Machine == nil ||
		d.Triage == nil || d.Matcher == nil {
		return nil, errors.New("intake: missing dependency")
	}
	if d.ResumeWindow <= 0 || d.MinSimilarity <= 0 || d.MinSimilarity >= 1 {
		return nil, fmt.Errorf("intake: resume window %v / min similarity %v out of range", d.ResumeWindow, d.MinSimilarity)
	}
	return &Service{d: d}, nil
}

func channelFor(provider string) (incident.Channel, error) {
	switch provider {
	case "at":
		return incident.ChannelUSSDAT, nil
	case "ucc":
		return incident.ChannelUSSDUCC, nil
	}
	return "", fmt.Errorf("intake: unknown USSD provider %q", provider)
}

// ErrInputHistory reports a callback whose text does not extend the input
// the session already gave.
var ErrInputHistory = errors.New("intake: callback text does not extend the session's input")

// session identifies one channel session for idempotency keys.
type session struct {
	idem     string // "ussd:<provider>:<session>" or "ivr:<call>"
	hasPhone bool
}

func (s session) key(screen int, suffix string) string {
	k := s.idem + ":s" + strconv.Itoa(screen)
	if suffix != "" {
		k += ":" + suffix
	}
	return k
}

// open finds or creates the incident for a session's first callback: a
// redial from the same number within the resume window continues the
// caller's unfinished incident (Guide §4), otherwise a new incident opens.
func (s *Service) open(ctx context.Context, ch incident.Channel, providerSession, serviceCode, network string, phone pii.Phone, idem string) (incident.Opened, error) {
	if !phone.IsZero() {
		macs, err := s.d.Keys.PhoneLookupMACs(ctx, phone)
		if err != nil {
			return incident.Opened{}, err
		}
		id, ok, err := s.d.Reader.FindResumable(ctx, macs, s.d.ResumeWindow)
		if err != nil {
			return incident.Opened{}, err
		}
		if ok {
			return s.d.Incidents.ResumeSession(ctx, incident.ResumeSession{IncidentID: id, Channel: ch,
				ProviderSessionID: providerSession, NetworkCode: network, Phone: phone, IdemKey: idem})
		}
	}
	return s.d.Incidents.StartSession(ctx, incident.StartSession{Channel: ch, ProviderSessionID: providerSession,
		ServiceCode: serviceCode, NetworkCode: network, Phone: phone, IdemKey: idem})
}

// HandleUSSD answers one callback: replay a stored step, or interpret the
// new input against the prompt last shown, persist the answer and what it
// triggers, and show the next screen.
func (s *Service) HandleUSSD(ctx context.Context, r USSDRequest) (Screen, error) {
	ch, err := channelFor(r.Provider)
	if err != nil {
		return Screen{}, err
	}
	macs, err := s.d.Keys.StepLookupMACs(ctx, StepMessage(r.Provider, r.SessionID, r.Text))
	if err != nil {
		return Screen{}, err
	}
	if st, ok, err := s.d.Steps.Lookup(ctx, r.Provider, r.SessionID, macs); err != nil {
		return Screen{}, err
	} else if ok {
		return st.Screen, nil
	}
	sess := session{idem: "ussd:" + r.Provider + ":" + r.SessionID, hasPhone: !r.Phone.IsZero()}
	prev, hasPrev, err := s.d.Steps.Latest(ctx, r.Provider, r.SessionID)
	if err != nil {
		return Screen{}, err
	}
	var incidentID uuid.UUID
	var pr Prompt
	if !hasPrev {
		opened, err := s.open(ctx, ch, r.SessionID, r.ServiceCode, r.NetworkCode, r.Phone, sess.idem+":start")
		if err != nil {
			return Screen{}, err
		}
		incidentID = opened.IncidentID
		p, err := s.d.Reader.Projection(ctx, incidentID)
		if err != nil {
			return Screen{}, err
		}
		pr = s.d.Machine.Next(p, sess.hasPhone)
	} else {
		input, err := s.newInput(ctx, r, prev)
		if err != nil {
			return Screen{}, err
		}
		incidentID = prev.IncidentID
		if pr, err = s.answer(ctx, incidentID, sess, prev.Kind, input); err != nil {
			return Screen{}, err
		}
	}
	text, end := RenderUSSD(pr)
	return s.d.Steps.Save(ctx, r.Provider, r.SessionID, Step{Screen: Screen{Text: text, End: end}, Kind: pr.Kind,
		ConsumedLen: len(r.Text), MAC: macs[0], IncidentID: incidentID})
}

// newInput returns the answer this callback adds: the text after the input
// the previous step consumed, which must be an unchanged prefix.
func (s *Service) newInput(ctx context.Context, r USSDRequest, prev Step) (string, error) {
	l := prev.ConsumedLen
	if len(r.Text) <= l || (l > 0 && r.Text[l] != '*') {
		return "", ErrInputHistory
	}
	prefix, err := s.d.Keys.StepLookupMACs(ctx, StepMessage(r.Provider, r.SessionID, r.Text[:l]))
	if err != nil {
		return "", err
	}
	if !slices.ContainsFunc(prefix, func(m crypto.MAC) bool { return m.KeyID == prev.MAC.KeyID && bytes.Equal(m.Sum, prev.MAC.Sum) }) {
		return "", ErrInputHistory
	}
	if l == 0 {
		return r.Text, nil
	}
	return r.Text[l+1:], nil
}

// answer applies input given to the prompt shown and returns what to show
// next. Every answer is persisted at once (Guide §4); idempotency keys make a
// retried or concurrent duplicate callback harmless.
func (s *Service) answer(ctx context.Context, id uuid.UUID, sess session, shown Kind, input string) (Prompt, error) {
	p, err := s.d.Reader.Projection(ctx, id)
	if err != nil {
		return Prompt{}, err
	}
	if shown == KindComplete || shown == KindOutOfArea || shown == "" {
		return s.d.Machine.Next(p, sess.hasPhone), nil
	}
	out, err := s.d.Machine.Answer(p, shown, input, sess.hasPhone)
	if err != nil {
		return Prompt{}, err
	}
	switch {
	case out.Invalid:
		return s.d.Machine.Reprompt(p, shown, sess.hasPhone), nil
	case out.Next != nil:
		return *out.Next, nil
	}
	if err := s.apply(ctx, p, sess, out); err != nil {
		return Prompt{}, err
	}
	if p, err = s.d.Reader.Projection(ctx, id); err != nil {
		return Prompt{}, err
	}
	return s.d.Machine.Next(p, sess.hasPhone), nil
}

func (s *Service) appendEvent(ctx context.Context, id uuid.UUID, typ incident.EventType, payload any, idem, logic string) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = s.d.Incidents.Append(ctx, incident.AppendParams{IncidentID: id, Type: typ, Payload: b,
		ActorKind: incident.ActorCaller, IdemKey: idem, LogicVersion: logic})
	return err
}

func (s *Service) appendSystem(ctx context.Context, id uuid.UUID, typ incident.EventType, payload any, idem string) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = s.d.Incidents.Append(ctx, incident.AppendParams{IncidentID: id, Type: typ, Payload: b,
		ActorKind: incident.ActorSystem, IdemKey: idem})
	return err
}

// apply persists an answer and everything it triggers, in order: sealed
// identifier, the answer, classification (screen 5), out of area (screen 6),
// dispatch readiness and T2 (trigger screen).
func (s *Service) apply(ctx context.Context, p incident.Projection, sess session, out Outcome) error {
	a := *out.Answer
	id := p.ID
	switch {
	case out.FreeText != "":
		ident, err := s.d.Incidents.StoreIdentifier(ctx, id, incident.IdentifierLandmarkText, []byte(out.FreeText), sess.key(a.Screen, "landmark_text"))
		if err != nil {
			return err
		}
		a.IdentifierID = &ident
	case out.Callback != nil:
		ident, err := s.d.Incidents.StoreIdentifier(ctx, id, incident.IdentifierCallback, []byte(out.Callback.E164()), sess.key(a.Screen, "callback"))
		if err != nil {
			return err
		}
		a.IdentifierID = &ident
	}
	payload, err := a.Payload()
	if err != nil {
		return err
	}
	if _, err := s.d.Incidents.Append(ctx, incident.AppendParams{IncidentID: id, Type: incident.EvScreenAnswered,
		Payload: payload, ActorKind: incident.ActorCaller, IdemKey: sess.key(a.Screen, "")}); err != nil {
		return err
	}
	if out.Classify && p.Severity == "" {
		res := s.d.Triage.Classify(triage.Inputs{Danger: p.Danger, Category: p.Category, AgeBand: p.AgeBand, SpecificDanger: a.Value})
		if err := s.appendEvent(ctx, id, incident.EvClassified, map[string]string{"severity": res.Severity, "rule_id": res.RuleID},
			sess.key(a.Screen, "classify"), res.LogicVersion); err != nil {
			return err
		}
		if res.RuleError {
			// Fail safe to a human (Build Prompt §14): red, flagged, escalated.
			if err := s.appendSystem(ctx, id, incident.EvFlagged, map[string]string{"flag": FlagTriageRuleError}, sess.key(a.Screen, "rule_error")); err != nil {
				return err
			}
			if err := s.appendSystem(ctx, id, incident.EvEscalated, map[string]string{"reason": FlagTriageRuleError}, sess.key(a.Screen, "escalate")); err != nil {
				return err
			}
		}
	}
	if out.OutOfArea {
		if err := s.appendSystem(ctx, id, incident.EvFlagged, map[string]string{"flag": FlagOutOfArea}, sess.key(a.Screen, "out_of_area")); err != nil {
			return err
		}
		return s.appendSystem(ctx, id, incident.EvClosed, map[string]string{"reason": FlagOutOfArea}, sess.key(a.Screen, "close"))
	}
	if out.DispatchReady && p.T[2] == nil {
		return s.dispatchReady(ctx, p, sess, a, out.FreeText)
	}
	return nil
}

// Flags recorded by intake.
const (
	FlagOutOfArea             = "out_of_area"
	FlagTriageRuleError       = "triage_rule_error"
	FlagNeedsLocationCallback = "needs_location_callback"
)

// dispatchReady resolves the location to a 500 m zone and records T2: a
// listed landmark's cell; a free-text landmark matched within the ward above
// the similarity threshold; otherwise the ward's centre, flagged for a
// location call-back, and dispatch proceeds on the ward's zones (Build
// Prompt §10.1, §12).
func (s *Service) dispatchReady(ctx context.Context, p incident.Projection, sess session, a incident.ScreenAnswer, freeText string) error {
	landmark := p.Landmark
	if a.Field == incident.FieldLandmark {
		landmark = a.Value
	}
	ward, ok := s.d.Gazetteer.Ward(p.Ward)
	if !ok {
		return fmt.Errorf("intake: ward %q is not in gazetteer %s", p.Ward, s.d.Gazetteer.Version)
	}
	cell, quality := ward.Centre, incident.QualityWard
	if l, ok := s.d.Gazetteer.Landmark(landmark); ok && l.WardID == ward.ID {
		cell, quality = l.Cell, incident.QualityLandmark
	} else if landmark == incident.LandmarkOther && freeText != "" {
		m, err := s.d.Matcher.Match(ctx, s.d.Gazetteer.Version, ward.ID, location.Normalise(freeText))
		if err != nil {
			return err
		}
		if l, ok := s.d.Gazetteer.Landmark(m.LandmarkID); m.Found && m.Similarity >= s.d.MinSimilarity && ok && l.WardID == ward.ID {
			cell, quality = l.Cell, incident.QualityFuzzy
		}
	}
	if quality == incident.QualityWard {
		if err := s.appendSystem(ctx, p.ID, incident.EvFlagged, map[string]string{"flag": FlagNeedsLocationCallback},
			sess.key(a.Screen, "needs_location_callback")); err != nil {
			return err
		}
	}
	return s.appendSystem(ctx, p.ID, incident.EvDispatchReady, map[string]any{
		"trigger_screen": a.Screen, "zone": map[string]int{"col": cell.Col, "row": cell.Row},
		"location_quality": quality, "gazetteer_version": s.d.Gazetteer.Version,
	}, sess.key(a.Screen, "dispatch_ready"))
}
