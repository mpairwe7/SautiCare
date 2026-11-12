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
	IncidentID     uuid.UUID
	State          incident.State
	Severity       incident.Severity
	DispatchedAt   *time.Time
	Zone           *incident.Zone // nil for a ward-level location
	Ward           string
	Alerts         []Alert
	Assigned       map[responder.Role]uuid.UUID
	Forwards       []ForwardState
	Escalations    []string
	CallerNotified []string
}
