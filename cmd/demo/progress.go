package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"uuid"

	"github.com/jackc/pgx/v5"

	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/location"
)

// The steps after an acceptance, recorded from a responder's smartphone in the
// demo: arrival (T5), departure with the patient (T6) and arrival at the
// facility (T7), then the incident closes so its responders can be alerted
// again. A coordinator's close stands in for the M5 console: it stands down
// every alert still in flight first (a closed incident never has a responder
// on the way, 0018), through the dispatch functions, as dispatch-worker's
// planner would.
//
// This is a demo preview of M3 (the responder's "My incident" menu): no
// service records these steps yet. The events go through the single write
// path (op.append_incident_event, ADR-0003) as the M0 demo's fixtures do
// (internal/platform/demo/m0.go), so the projection, T5–T7, the outbox and
// the audit ledger hold them like any other event.

const (
	stepArrived    = "arrived"
	stepDeparted   = "departed"
	stepAtFacility = "at_facility"
	stepClose      = "close"
)

// stepsByRole is what each role records: the VHT attests arrival; the
// transporter arrives, leaves with the patient and reaches the facility.
var stepsByRole = map[string][]string{
	"vht":         {stepArrived},
	"transporter": {stepArrived, stepDeparted, stepAtFacility},
}

type progressRequest struct {
	Phone    string `json:"phone"`    // a responder's handle; empty for a coordinator's close
	Incident string `json:"incident"` // the incident's ID, as the board names it
	Step     string `json:"step"`     // arrived, departed, at_facility or close
	Facility string `json:"facility"` // at_facility: a facility of the gazetteer
}

// progressError is a refusal the page shows as it is.
type progressError struct {
	status int
	msg    string
}

func (e *progressError) Error() string { return e.msg }

func refuse(status int, format string, args ...any) error {
	return &progressError{status: status, msg: fmt.Sprintf(format, args...)}
}

// checkProgress validates a request against the demo's phones and the
// gazetteer, before anything is read or written. It returns the responder's
// phone (zero for a close) and the incident ID.
func checkProgress(q progressRequest, phones []phone, gaz location.File) (phone, uuid.UUID, error) {
	id, err := uuid.Parse(q.Incident)
	if err != nil {
		return phone{}, uuid.UUID{}, refuse(http.StatusBadRequest, "unknown incident")
	}
	if q.Step == stepClose {
		if q.Phone != "" {
			return phone{}, uuid.UUID{}, refuse(http.StatusBadRequest, "a close comes from the coordinator, not a phone")
		}
		return phone{}, id, nil
	}
	var p phone
	for _, x := range phones {
		if x.Handle == q.Phone && x.Kind == "responder" {
			p = x
		}
	}
	if p.Handle == "" {
		return phone{}, uuid.UUID{}, refuse(http.StatusBadRequest, "unknown responder phone")
	}
	if !slices.Contains(stepsByRole[p.Role], q.Step) {
		return phone{}, uuid.UUID{}, refuse(http.StatusBadRequest, "a %s does not record %q", p.Role, q.Step)
	}
	if q.Step == stepAtFacility {
		if !slices.ContainsFunc(gaz.Facilities, func(f location.Facility) bool { return f.ID == q.Facility }) {
			return phone{}, uuid.UUID{}, refuse(http.StatusBadRequest, "unknown facility")
		}
	} else if q.Facility != "" {
		return phone{}, uuid.UUID{}, refuse(http.StatusBadRequest, "only the arrival at the facility names one")
	}
	return p, id, nil
}

// progress records a step and answers the incident's new state.
func (a *app) progress(w http.ResponseWriter, r *http.Request) {
	st := a.current(w)
	if st == nil {
		return
	}
	var q progressRequest
	if !readJSON(w, r, &q) {
		return
	}
	p, id, err := checkProgress(q, st.phones, a.gaz)
	if err == nil {
		var state incident.State
		if state, err = st.recordProgress(r.Context(), p, id, q); err == nil {
			writeJSON(w, http.StatusOK, map[string]string{"state": string(state), "note": "Demo preview of M3: recorded through the single write path."})
			return
		}
	}
	var pe *progressError
	switch {
	case errors.As(err, &pe):
		writeJSON(w, pe.status, map[string]string{"error": pe.msg})
	case errors.Is(err, incident.ErrIllegalTransition), errors.Is(err, incident.ErrNotFound):
		// The lifecycle refused it: say what the page can do about it.
		writeJSON(w, http.StatusConflict, map[string]string{"error": refusal(q.Step)})
	default:
		a.log.Error("demo: progress", "step", q.Step, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "The step could not be recorded."})
	}
}

func refusal(step string) string {
	switch step {
	case stepArrived:
		return "Arrival can be recorded once help is assigned and before the patient leaves."
	case stepDeparted:
		return "Record the arrival on scene before leaving with the patient."
	case stepAtFacility:
		return "Record leaving with the patient before the arrival at the facility."
	default:
		return "The incident is already closed or merged."
	}
}

// closeAttempts bounds the stand-down-then-close loop: the planner may send a
// new alert between the two (a ring step), and the next round stands it down.
const closeAttempts = 4

// closeIncident stands down the incident's pending alerts, then closes it.
// Each stand-down is an alert.stood_down event (op.close_alert), and the
// close wakes the planner, which sends the stand-down SMS.
func (st *stack) closeIncident(ctx context.Context, id uuid.UUID, actor, key string) (incident.State, error) {
	var err error
	for range closeAttempts {
		var pending []uuid.UUID
		rows, qerr := st.ro.Query(ctx, `SELECT id FROM op.alerts WHERE incident_id = $1 AND outcome = 'pending'`, id)
		if qerr != nil {
			return "", qerr
		}
		for rows.Next() {
			var a uuid.UUID
			if err := rows.Scan(&a); err != nil {
				rows.Close()
				return "", err
			}
			pending = append(pending, a)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return "", err
		}
		for _, a := range pending {
			if _, err := st.dispatcher.CloseAlert(ctx, a, dispatch.OutcomeStoodDown); err != nil {
				return "", err
			}
		}
		var out incident.Appended
		out, err = st.intake.Append(ctx, incident.AppendParams{IncidentID: id, Type: incident.EvClosed,
			Payload: json.RawMessage(`{"reason":"completed"}`), ActorKind: actor, IdemKey: key})
		if err == nil {
			return out.NewState, nil
		}
		if !errors.Is(err, incident.ErrIllegalTransition) {
			return "", err
		}
	}
	return "", err
}

// recordProgress checks that the responder holds the accepted alert on the
// incident, then appends the step's events. Each append is idempotent on its
// key, so a double tap records once.
func (st *stack) recordProgress(ctx context.Context, p phone, id uuid.UUID, q progressRequest) (incident.State, error) {
	if q.Step != stepClose {
		var accepted bool
		if err := st.ro.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM op.alerts
			WHERE incident_id = $1 AND responder_id = $2::uuid AND outcome = 'accepted')`, id, st.ids[p.Handle]).Scan(&accepted); err != nil {
			return "", err
		}
		if !accepted {
			return "", refuse(http.StatusConflict, "%s has not accepted this incident.", p.Label)
		}
	}
	type ev struct {
		typ     incident.EventType
		payload any
		actor   string
	}
	var evs []ev
	switch q.Step {
	case stepArrived:
		evs = []ev{{incident.EvArrived, map[string]string{"role": p.Role}, incident.ActorResponder}}
	case stepDeparted:
		evs = []ev{{incident.EvDeparted, map[string]string{}, incident.ActorResponder}}
	case stepAtFacility:
		evs = []ev{{incident.EvAtFacility, map[string]string{"facility": q.Facility}, incident.ActorResponder}}
	case stepClose:
		// The incident must exist and be open: closing an unknown or ended
		// one is refused before anything is stood down.
		var state string
		if err := st.ro.QueryRow(ctx, `SELECT state FROM op.incidents WHERE id = $1`, id).Scan(&state); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return "", refuse(http.StatusNotFound, "unknown incident")
			}
			return "", err
		}
		if state == string(incident.StateClosed) || state == string(incident.StateMerged) {
			return "", refuse(http.StatusConflict, "The incident is already %s.", state)
		}
		return st.closeIncident(ctx, id, incident.ActorSystem, fmt.Sprintf("demo-progress:%s:close", id))
	}
	var state incident.State
	for _, e := range evs {
		payload, err := json.Marshal(e.payload)
		if err != nil {
			return "", err
		}
		out, err := st.intake.Append(ctx, incident.AppendParams{IncidentID: id, Type: e.typ, Payload: payload, ActorKind: e.actor,
			IdemKey: fmt.Sprintf("demo-progress:%s:%s:%s", id, e.typ, p.Role)})
		if err != nil {
			return "", err
		}
		state = out.NewState
	}
	if q.Step == stepAtFacility {
		// The handover ends the incident: closing frees both responders for
		// the next call (UX audit F-75; in production the M5 console closes).
		return st.closeIncident(ctx, id, incident.ActorSystem, fmt.Sprintf("demo-progress:%s:%s:transporter", id, incident.EvClosed))
	}
	return state, nil
}
