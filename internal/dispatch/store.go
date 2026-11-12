package dispatch

import (
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/responder"
)

// IssueParams alerts one responder (op.issue_alert). LogicVersion is the
// dispatch parameter version stamped on the alert and its event.
type IssueParams struct {
	IncidentID, ResponderID uuid.UUID
	Role                    responder.Role
	Ring, Wave              int
	LogicVersion            string
}

// Issued is the stored alert; Duplicate when the responder was already
// alerted for this incident (nobody is alerted twice).
type Issued struct {
	AlertID   uuid.UUID
	SentAt    time.Time
	Duplicate bool
}

// AcceptResult is what a responder hears after accepting.
type AcceptResult string

const (
	Accepted        AcceptResult = "accepted"
	AlreadyTaken    AcceptResult = "already_taken"    // "already taken, thank you"
	AlreadyAssigned AcceptResult = "already_assigned" // the responder is on another incident
	NoLongerNeeded  AcceptResult = "closed"           // the incident is closed, merged or past dispatch
)

// AcceptOutcome is the result of op.accept_alert.
type AcceptOutcome struct {
	Result     AcceptResult
	IncidentID uuid.UUID
	Role       responder.Role
}

// Delivery attempt purposes and channels (op.alert_attempts).
const (
	PurposeAlert     = "alert"
	PurposeStandDown = "stand_down"

	ChannelUSSDPush = "ussd_push"
	ChannelSMS      = "sms"
	ChannelVoice    = "voice"
	ChannelUSSDPull = "ussd_pull"
)

// Due is an incident whose dispatch wake has come, leased to this worker.
// Gen is the timer generation at the claim: a newer fact keeps it due.
type Due struct {
	IncidentID uuid.UUID
	Gen        int64
}

// Snapshot is one incident's dispatch facts as stored, before the worker
// adds the pool, facilities, suspension and parameters to make a State.
type Snapshot struct {
	IncidentID      uuid.UUID
	State           incident.State
	Severity        incident.Severity
	DispatchedAt    *time.Time
	RedSince        *time.Time     // when the incident became red, if it is
	Zone            *incident.Zone // for a ward-level location, the ward's centre
	LocationQuality string         // landmark, fuzzy or ward (incident.Quality*)
	Ward            string
	Alerts          []Alert
	Assigned        map[responder.Role]uuid.UUID
	Forwards        []ForwardState
	Escalations     []string
	CallerNotified  []string
}

// ControlEntry is one row of the dispatch control log: the dated register
// of suspensions, resumptions and stopping-rule breaches (O6).
type ControlEntry struct {
	At       time.Time
	Action   string // suspended, resumed, stopping_rule_breached
	Reason   string
	StaffID  *uuid.UUID // nil: automatic (the stopping rule)
	Failures int        // stopping_rule_breached only
	Window   int
}

// Reminder is an availability period about to end (review focus 4: the
// expiry reminder is sent once per period).
type Reminder struct {
	ResponderID uuid.UUID
	Until       time.Time
}

// Notice is a caller notice to deliver (a recorded caller.notified).
type Notice struct {
	AttemptID, IncidentID uuid.UUID
	Kind                  string
	Channel               string
}

// MessageFacts are the coded facts a message may show about an incident
// (hard constraint 6: never a name, a clinical answer or a number).
type MessageFacts struct {
	Reference incident.Reference
	Severity  incident.Severity
	Category  string // Tool 1 category code
	Ward      string // gazetteer ward ID
	Landmark  string // gazetteer landmark ID ("other": a free-text landmark, never sent)
	Language  string // the caller's Tool 1 language
}

// ResponderAlert is a responder's own pending alert, as their menu shows it.
type ResponderAlert struct {
	AlertID, IncidentID uuid.UUID
	Role                responder.Role
	Facts               MessageFacts
}

// AlertHistory is one line of a responder's history (scenario 10).
type AlertHistory struct {
	AlertID, IncidentID uuid.UUID
	Role                responder.Role
	SentAt              time.Time
	Outcome             string
	DeclineReason       string
	RespondedAt         *time.Time
}
