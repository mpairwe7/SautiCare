package incident

import (
	"uuid"

	"sautihealth.com/sauti/internal/pii"
)

// The ten commands of Build Prompt §7. Every channel adapter (USSD on either
// route, IVR, and the flagged-off app-api) translates its input into one of
// these, so a caller may start on USSD and finish on IVR and a responder may
// accept on USSD and report by voice. Fields beyond StartSession are the
// contract skeleton; their handlers arrive in M1–M3.

// StartSession opens (or, for a retried callback, re-finds) an incident.
type StartSession struct {
	Channel           Channel
	ProviderSessionID string
	ServiceCode       string
	NetworkCode       string
	Phone             pii.Phone // zero when withheld (IVR)
	IdemKey           string
}

// ResumeSession attaches a redial (USSD or IVR) to an unfinished incident.
type ResumeSession struct {
	IncidentID        uuid.UUID
	Channel           Channel
	ProviderSessionID string
	NetworkCode       string
	Phone             pii.Phone
	IdemKey           string
}

// AnswerScreen records a Tool 1 screen answer.
type AnswerScreen struct {
	IncidentID uuid.UUID
	Screen     int
	Answer     string
	Channel    Channel
	IdemKey    string
}

// AcceptAlert records a responder accepting an alert.
type AcceptAlert struct {
	AlertID, ResponderID uuid.UUID
	Channel              Channel
	IdemKey              string
}

// DeclineAlert records a decline with a closed-list reason code (decision 6).
type DeclineAlert struct {
	AlertID, ResponderID uuid.UUID
	ReasonCode           int
	Channel              Channel
	IdemKey              string
}

// ReportArrived records the responder-attested arrival (T5).
type ReportArrived struct {
	IncidentID, ResponderID uuid.UUID
	Channel                 Channel
	IdemKey                 string
}

// ReportDeparted records leaving the scene with the patient (T6).
type ReportDeparted struct {
	IncidentID, ResponderID uuid.UUID
	Channel                 Channel
	IdemKey                 string
}

// ReportAtFacility records arrival at a facility (T7).
type ReportAtFacility struct {
	IncidentID, ResponderID, FacilityID uuid.UUID
	Channel                             Channel
	IdemKey                             string
}

// SubmitForm2 records the Tool 2 responder post-incident form.
type SubmitForm2 struct {
	IncidentID, ResponderID uuid.UUID
	Answers                 map[string]string
	IdemKey                 string
}

// SetAvailability records a responder's availability toggle.
type SetAvailability struct {
	ResponderID uuid.UUID
	Available   bool
	Channel     Channel
	IdemKey     string
}

// RequestBridge asks for a masked call between caller and responder.
type RequestBridge struct {
	InitiatorRole string // "caller" | "responder"
	Last4         string // disambiguates several active assignments
	Phone         pii.Phone
	IdemKey       string
}
