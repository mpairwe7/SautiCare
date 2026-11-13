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
		// Each role searches on its own: the other role may still be alerted
		// and accept once the first responder is on scene.
		{StateOnScene, EvAlertIssued, ""},
		{StateOnScene, EvAccepted, ""},
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
		{Any, EvAlertDeclined, ""},
		{Any, EvAlertNoResponse, ""},
		{Any, EvAlertStoodDown, ""},
		{Any, EvForwarded, ""},
		{Any, EvForwardAcknowledged, ""},
		{Any, EvForwardTimedOut, ""},
		{Any, EvCallerNotified, ""},
		{StateMerged, EvUnmerged, StateDispatching},
		// A wrongly merged call can be split off a survivor that has since
		// closed (PR #8 review); the survivor stays closed.
		{StateClosed, EvSplit, ""},
		{StateMerged, EvScreenAnswered, ""},
		{StateIntake, EvMerged, StateMerged},
		{StateTriaged, EvMerged, StateMerged},
		{StateDispatching, EvMerged, StateMerged},
	}
}

// lookup finds the transition for event in state, preferring an exact match
// over a wildcard. A closed or merged incident accepts only its exact
// transitions (a closed survivor: a coordinator's split; a merged call: its
// unmerge and its own caller's answers), never a wildcard event.
func lookup(state State, event EventType) (Transition, bool) {
	var wildcard *Transition
	for _, tr := range Transitions() {
		if tr.Event != event {
			continue
		}
		if tr.From == state {
			return tr, true
		}
		if tr.From == Any && state != StateMerged && state != StateClosed {
			w := tr
			wildcard = &w
		}
	}
	if wildcard != nil {
		return *wildcard, true
	}
	return Transition{}, false
}
