package incident

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"uuid"
)

// Dispatch facts (M2; Build Prompt §9 Dispatch, §10.4). Each is written by
// its own function in the operational store (migration 0015), which keeps
// op.alerts, op.assignments, op.forwards and op.caller_notices consistent
// with the event in the same transaction. Payloads carry identifiers and
// codes only — never a name, number or free text — and mirror the SQL
// checks exactly.
const (
	EvAlertDeclined       EventType = "alert.declined"
	EvAlertNoResponse     EventType = "alert.no_response"
	EvAlertStoodDown      EventType = "alert.stood_down"
	EvForwarded           EventType = "incident.forwarded"
	EvForwardAcknowledged EventType = "incident.forward_acknowledged"
	EvForwardTimedOut     EventType = "incident.forward_timed_out"
	EvCallerNotified      EventType = "caller.notified"
)

// DispatchEvents are written only by the dispatch functions; the generic
// append refuses them.
func DispatchEvents() []EventType {
	return []EventType{EvAlertIssued, EvAccepted, EvAlertDeclined, EvAlertNoResponse, EvAlertStoodDown,
		EvForwarded, EvForwardAcknowledged, EvForwardTimedOut, EvCallerNotified}
}

// AlertRoles are the responder roles an alert names (internal/responder
// defines the same two; a test pins them together).
var AlertRoles = []string{"vht", "transporter"}

// Caller notice kinds (SMS templates).
const (
	NoticeHelperOnTheWay = "helper_on_the_way"
	NoticeForwarded      = "forwarded_to_facility"
)

var (
	codeText = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
	ringText = regexp.MustCompile(`^[1-9][0-9]?$`)
	waveText = regexp.MustCompile(`^[1-9][0-9]{0,3}$`)
)

// AlertFact is the payload of alert.issued (all fields), responder.accepted
// (Alert, Responder, Role), alert.declined (Alert, Responder, optional
// Reason), alert.no_response and alert.stood_down (Alert).
type AlertFact struct {
	Alert, Responder uuid.UUID
	Role             string
	Ring, Wave       int
	Reason           string
}

func illegal(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrIllegalTransition}, a...)...)
}

func jsonUUID(raw json.RawMessage) (uuid.UUID, bool) {
	s, ok := jsonString(raw)
	if !ok || !uuidText.MatchString(s) {
		return uuid.UUID{}, false
	}
	id, err := uuid.Parse(s)
	return id, err == nil
}

func jsonCode(raw json.RawMessage) (string, bool) {
	s, ok := jsonString(raw)
	return s, ok && codeText.MatchString(s)
}

// ParseAlertFact validates the payload of a dispatch alert event.
func ParseAlertFact(t EventType, raw json.RawMessage) (AlertFact, error) {
	var keys, optional []string
	switch t {
	case EvAlertIssued:
		keys = []string{"alert", "responder", "role", "ring", "wave"}
	case EvAccepted:
		keys = []string{"alert", "responder", "role"}
	case EvAlertDeclined:
		keys, optional = []string{"alert", "responder"}, []string{"reason"}
	case EvAlertNoResponse, EvAlertStoodDown:
		keys = []string{"alert"}
	default:
		return AlertFact{}, illegal("%s is not an alert event", t)
	}
	m, err := strictKeys(raw, append(keys, optional...)...)
	if err != nil {
		return AlertFact{}, illegal("%s: %v", t, err)
	}
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			return AlertFact{}, illegal("%s needs %q", t, k)
		}
	}
	var f AlertFact
	var ok bool
	if f.Alert, ok = jsonUUID(m["alert"]); !ok {
		return f, illegal("%s: alert must be a UUID", t)
	}
	if r, present := m["responder"]; present {
		if f.Responder, ok = jsonUUID(r); !ok {
			return f, illegal("%s: responder must be a UUID", t)
		}
	}
	if r, present := m["role"]; present {
		if f.Role, ok = jsonString(r); !ok || !slices.Contains(AlertRoles, f.Role) {
			return f, illegal("%s: role must be one of %v", t, AlertRoles)
		}
	}
	if r, present := m["ring"]; present {
		if f.Ring, ok = jsonNumber(r, ringText); !ok {
			return f, illegal("%s: ring must be 1-99", t)
		}
	}
	if r, present := m["wave"]; present {
		if f.Wave, ok = jsonNumber(r, waveText); !ok {
			return f, illegal("%s: wave must be 1-9999", t)
		}
	}
	if r, present := m["reason"]; present {
		if f.Reason, ok = jsonCode(r); !ok {
			return f, illegal("%s: reason must be a code", t)
		}
	}
	return f, nil
}

// ParseFacility validates the payload of a forwarding event: exactly a
// gazetteer facility ID.
func ParseFacility(raw json.RawMessage) (string, error) {
	m, err := strictKeys(raw, "facility")
	if err == nil {
		if f, ok := jsonCode(m["facility"]); ok {
			return f, nil
		}
	}
	return "", illegal("payload needs exactly a \"facility\" code")
}

// ParseCallerNotified validates caller.notified: a notice kind, and the
// facility for (and only for) a forwarded notice.
func ParseCallerNotified(raw json.RawMessage) (kind, facility string, err error) {
	m, err := strictKeys(raw, "kind", "facility")
	if err != nil {
		return "", "", illegal("caller.notified: %v", err)
	}
	kind, _ = jsonString(m["kind"])
	_, hasFacility := m["facility"]
	switch {
	case kind == NoticeHelperOnTheWay && !hasFacility:
		return kind, "", nil
	case kind == NoticeForwarded && hasFacility:
		if facility, ok := jsonCode(m["facility"]); ok {
			return kind, facility, nil
		}
	}
	return "", "", illegal("caller.notified needs a known kind, with a facility only when forwarded")
}

// ParseEscalated validates incident.escalated: a reason code and,
// optionally, the role that triggered it.
func ParseEscalated(raw json.RawMessage) (reason, role string, err error) {
	m, err := strictKeys(raw, "reason", "role")
	if err == nil {
		var ok bool
		if reason, ok = jsonCode(m["reason"]); ok {
			if r, present := m["role"]; !present {
				return reason, "", nil
			} else if role, ok = jsonString(r); ok && slices.Contains(AlertRoles, role) {
				return reason, role, nil
			}
		}
	}
	return "", "", illegal("incident.escalated needs a reason code and at most a role")
}
