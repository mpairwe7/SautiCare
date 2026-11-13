package incident

import (
	"encoding/json"
	"time"
	"uuid"
)

// EventType names an incident_events row. Each one that marks a protocol
// timestamp says so; those T-times are set by the database, never by Go
// (hard constraint 4; Build Prompt §15.1).
type EventType string

const (
	EvSessionStarted EventType = "session.started"         // T1
	EvSessionResumed EventType = "session.resumed"         //
	EvScreenAnswered EventType = "screen.answered"         //
	EvClassified     EventType = "incident.classified"     //
	EvDispatchReady  EventType = "incident.dispatch_ready" // T2
	EvAlertIssued    EventType = "alert.issued"            // T3 (first)
	EvAccepted       EventType = "responder.accepted"      // T4 (first)
	EvArrived        EventType = "responder.arrived"       // T5 (first, attested)
	EvDeparted       EventType = "responder.departed"      // T6
	EvAtFacility     EventType = "responder.at_facility"   // T7
	EvFlagged        EventType = "incident.flagged"
	EvEscalated      EventType = "incident.escalated"
	EvMerged         EventType = "incident.merged"
	EvMergeAbsorbed  EventType = "incident.merge_absorbed"  // on the survivor: a call joined; T1 = earliest
	EvSplit          EventType = "incident.split"           // on the survivor: a coordinator split a wrong merge
	EvUnmerged       EventType = "incident.unmerged"        // on the member: revived by the split
	EvSeverityRaised EventType = "incident.severity_raised" // on the survivor: a more urgent call merged into it
	EvClosed         EventType = "incident.closed"
)

// EventTypes lists every known event type.
func EventTypes() []EventType {
	return []EventType{
		EvSessionStarted, EvSessionResumed, EvScreenAnswered, EvClassified, EvDispatchReady,
		EvAlertIssued, EvAccepted, EvArrived, EvDeparted, EvAtFacility,
		EvFlagged, EvEscalated, EvMerged, EvMergeAbsorbed, EvSplit, EvUnmerged, EvSeverityRaised, EvClosed,
		EvAlertDeclined, EvAlertNoResponse, EvAlertStoodDown, EvForwarded, EvForwardAcknowledged, EvForwardTimedOut,
		EvCallerNotified,
	}
}

// Known reports whether t is a defined event type.
func (t EventType) Known() bool {
	for _, k := range EventTypes() {
		if k == t {
			return true
		}
	}
	return false
}

// tIndex maps the event types that set a protocol timestamp to its index.
var tIndex = map[EventType]int{
	EvSessionStarted: 1, EvDispatchReady: 2, EvAlertIssued: 3, EvAccepted: 4,
	EvArrived: 5, EvDeparted: 6, EvAtFacility: 7,
}

// Actor kinds. An event never records a caller's or responder's phone
// number; staff are recorded by their identity-provider subject UUID.
const (
	ActorSystem    = "system"
	ActorCaller    = "caller"
	ActorResponder = "responder"
	ActorStaff     = "staff"
)

// Event is one row of the append-only incident_events log.
type Event struct {
	ID           uuid.UUID
	IncidentID   uuid.UUID
	Seq          int64
	Type         EventType
	RecordedAt   time.Time
	ActorKind    string
	StaffID      *uuid.UUID
	Payload      json.RawMessage
	IdemKey      string
	LogicVersion string
}
