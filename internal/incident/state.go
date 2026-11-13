package incident

import (
	"time"
	"uuid"
)

// State is the incident lifecycle state, derived from events (never set
// directly): Build Prompt §7 "incident.state is derived from those rows".
type State string

const (
	StateIntake       State = "intake"
	StateTriaged      State = "triaged"
	StateDispatching  State = "dispatching"
	StateAssigned     State = "assigned"
	StateOnScene      State = "on_scene"
	StateTransporting State = "transporting"
	StateAtFacility   State = "at_facility"
	StateClosed       State = "closed"
	StateMerged       State = "merged"
)

// Terminal reports whether no further events may be applied.
func (s State) Terminal() bool { return s == StateClosed || s == StateMerged }

// Severity is the Tool 1 three-level classification.
type Severity string

const (
	SevRed    Severity = "red"
	SevYellow Severity = "yellow"
	SevGreen  Severity = "green"
)

// Rank orders severities; a higher rank is more urgent. Unknown is 0.
func (s Severity) Rank() int {
	switch s {
	case SevRed:
		return 3
	case SevYellow:
		return 2
	case SevGreen:
		return 1
	}
	return 0
}

// Channel is the intake route an incident started on.
type Channel string

const (
	ChannelUSSDAT  Channel = "ussd_at"
	ChannelUSSDUCC Channel = "ussd_ucc"
	ChannelIVR     Channel = "ivr"
)

// Projection is the current state of one incident, derived from its events.
// T[1]..T[7] are the protocol timestamps T1..T7; T[0] is unused.
type Projection struct {
	ID           uuid.UUID
	Reference    Reference
	State        State
	Version      int64
	Channel      string
	Severity     Severity
	LogicVersion string
	T            [8]*time.Time
	Escalated    bool
	Flags        []string
	MergedInto   *uuid.UUID

	// Tool 1 answers (coded values only; identifiers are sealed elsewhere).
	Language, Danger, Category, AgeBand, SpecificDanger, Ward, Landmark string
	// LastScreen is the highest Tool 1 screen answered (0 = none).
	LastScreen int
	// Zone and LocationQuality are set by dispatch_ready.
	Zone            *Zone
	LocationQuality string
}
