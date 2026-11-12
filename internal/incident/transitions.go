package incident

// Any matches every non-terminal state in a Transition's From.
const Any State = "*"

// Transition says that Event is allowed in From and moves the incident to To.
// An empty To leaves the state unchanged.
//
// This table is the single definition of the lifecycle. Migration
// db/operational/migrations/0001_core.sql seeds op.incident_transitions with
// exactly these rows, and an integration test fails if the two diverge.
type Transition struct {
	From  State
	Event EventType
	To    State
}

// Transitions returns the lifecycle table. EvSessionStarted is absent: it is
// only ever written by op.open_incident as an incident's first event.
func Transitions() []Transition {
	return []Transition{
		{StateIntake, EvClassified, StateTriaged},
		{StateTriaged, EvDispatchReady, StateDispatching},
		{StateDispatching, EvAlertIssued, ""},
		{StateAssigned, EvAlertIssued, ""},
		{StateDispatching, EvAccepted, StateAssigned},
		{StateAssigned, EvAccepted, ""},
		{StateAssigned, EvArrived, StateOnScene},
		{StateOnScene, EvArrived, ""},
		{StateOnScene, EvDeparted, StateTransporting},
		{StateTransporting, EvAtFacility, StateAtFacility},
		{Any, EvScreenAnswered, ""},
		{Any, EvSessionResumed, ""},
		{Any, EvFlagged, ""},
		{Any, EvEscalated, ""},
		{Any, EvClosed, StateClosed},
		{Any, EvMergeAbsorbed, ""},
		{Any, EvSplit, ""},
		{Any, EvSeverityRaised, ""},
		{StateMerged, EvUnmerged, StateDispatching},
		{StateMerged, EvScreenAnswered, ""},
		{StateIntake, EvMerged, StateMerged},
		{StateTriaged, EvMerged, StateMerged},
		{StateDispatching, EvMerged, StateMerged},
	}
}

// lookup finds the transition for event in state, preferring an exact match
// over a wildcard. A closed incident accepts nothing; a merged one accepts
// only its exact transitions (its unmerge, and its own caller's answers),
// never a wildcard event.
func lookup(state State, event EventType) (Transition, bool) {
	if state == StateClosed {
		return Transition{}, false
	}
	var wildcard *Transition
	for _, tr := range Transitions() {
		if tr.Event != event {
			continue
		}
		if tr.From == state {
			return tr, true
		}
		if tr.From == Any && state != StateMerged {
			w := tr
			wildcard = &w
		}
	}
	if wildcard != nil {
		return *wildcard, true
	}
	return Transition{}, false
}
