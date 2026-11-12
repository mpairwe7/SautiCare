// Package intake turns USSD (and, from M1, IVR) input into incident
// commands. In M0 it handles StartSession only: the incident and its
// reference exist from the first screen (Build Prompt §10.1). The Tool 1
// eight-screen state machine arrives in M1 once the protocol wording is
// signed off; until then callers get a test-mode screen with their number.
package intake

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"uuid"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/pii"
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

// StepStore persists the reply to each (session, input) so a retried
// callback returns the same screen without a second write.
type StepStore interface {
	Lookup(ctx context.Context, provider, sessionID string, inputSHA [32]byte) (Screen, bool, error)
	Save(ctx context.Context, provider, sessionID string, inputSHA [32]byte, s Screen, incidentID uuid.UUID) (Screen, error)
}

// Starter opens or re-finds the incident for a session.
type Starter interface {
	StartSession(ctx context.Context, cmd incident.StartSession) (incident.Opened, error)
}

// Service handles USSD input.
type Service struct {
	starter Starter
	steps   StepStore
	screens config.Screens
	lang    string
}

// NewService wires the intake service. lang is the default language until
// screen 1 (language choice) exists in M1.
func NewService(st Starter, steps StepStore, screens config.Screens, lang string) *Service {
	return &Service{starter: st, steps: steps, screens: screens, lang: lang}
}

// Render substitutes the spoken reference into a catalogue string.
func Render(template string, ref incident.Reference) string {
	return strings.ReplaceAll(template, "{ref}", ref.Spoken())
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

// HandleUSSD answers one callback.
func (s *Service) HandleUSSD(ctx context.Context, r USSDRequest) (Screen, error) {
	inputSHA := sha256.Sum256([]byte(r.Text))
	if sc, ok, err := s.steps.Lookup(ctx, r.Provider, r.SessionID, inputSHA); err != nil {
		return Screen{}, err
	} else if ok {
		return sc, nil
	}
	ch, err := channelFor(r.Provider)
	if err != nil {
		return Screen{}, err
	}
	opened, err := s.starter.StartSession(ctx, incident.StartSession{
		Channel:           ch,
		ProviderSessionID: r.SessionID,
		ServiceCode:       r.ServiceCode,
		NetworkCode:       r.NetworkCode,
		Phone:             r.Phone,
		IdemKey:           "ussd:" + r.Provider + ":" + r.SessionID + ":start",
	})
	if err != nil {
		return Screen{}, err
	}
	// The final screen must carry the reference; a missing or placeholder
	// template (no {ref}) falls back to English.
	tmpl := s.screens.StartedTestMode[s.lang]
	if !strings.Contains(tmpl, "{ref}") {
		tmpl = s.screens.StartedTestMode["en"]
	}
	sc := Screen{Text: Render(tmpl, opened.Reference), End: true}
	return s.steps.Save(ctx, r.Provider, r.SessionID, inputSHA, sc, opened.IncidentID)
}
