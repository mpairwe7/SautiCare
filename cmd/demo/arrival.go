package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/sim"
)

// The caller asked whether help has arrived: a demo preview of Build Prompt
// §10.4 ("the caller is asked at set intervals whether help has arrived"),
// which ADR-0028 left to M3.
//
//   - Every interval (5 minutes; faster for a presentation) after the call,
//     the caller is asked "Has a health worker arrived yet?". A basic phone
//     gets a USSD push, a session the network opens on the handset, served
//     here; a smartphone gets it in the app (POST /api/arrival).
//   - Yes: the asking stops. Help arrived after the caller's last "no" (or
//     the call) and before this answer, which bounds the arrival time even
//     when no responder recorded it.
//   - No: each responder who accepted is sent an SMS that the caller is still
//     waiting. For a role nobody has accepted, the search is pressed within
//     the protocol: dispatch is woken at once; each responder still holding
//     an unanswered alert for that role, or a lapsed one they can still
//     accept, is reminded by SMS; and when nobody is being asked for that
//     role and no coordinator has it yet, a coordinator is alerted with
//     no_eligible_responder, the reason for a role nobody can be found for.
//     The search never widens past what the dispatch mode allows.
//   - No answer within the window: asked again after the interval.
//
// The answers stay in the demo process. The only writes are dispatch's own
// wake-up and, on a "no", that existing escalation event.

// arrivalPaces are the intervals the page offers: the one asked for, then
// two that fit a presentation.
var arrivalPaces = []time.Duration{5 * time.Minute, time.Minute, 30 * time.Second}

// arrivalWindow is how long an ask stays open, at most: a network USSD
// session ends within about 180 s.
const arrivalWindow = 2 * time.Minute

// Answers.
const (
	answerYes  = "yes"
	answerNo   = "no"
	answerNone = "none" // not answered within the window
)

type arrivalAsk struct {
	At         time.Time  `json:"at"`
	Answer     string     `json:"answer,omitempty"`
	AnsweredAt *time.Time `json:"answeredAt,omitempty"`
	Via        string     `json:"via,omitempty"` // ussd or app
	Did        string     `json:"did,omitempty"` // what Sauti did on a "no"
}

type arrivalCheck struct {
	incident  string
	ref       string
	phone     string // the caller's handle
	t1        time.Time
	asks      []arrivalAsk
	open      bool      // the last ask waits for an answer
	token     string    // its USSD push session
	base      time.Time // the next ask comes one interval after it
	confirmed time.Time
}

func (c *arrivalCheck) next(every time.Duration) time.Time { return c.base.Add(every) }

// arrivals keeps one run's checks.
type arrivals struct {
	mu        sync.Mutex
	every     time.Duration
	reporters map[string]string // incident reference → the caller's handle
	checks    map[string]*arrivalCheck
}

func newArrivals() *arrivals {
	return &arrivals{every: arrivalPaces[0], reporters: map[string]string{}, checks: map[string]*arrivalCheck{}}
}

// reported notes which phone reported a reference, from its closing screen.
func (r *arrivals) reported(ref, phone string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reporters[ref] = phone
}

func (r *arrivals) pace() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.every
}

func (r *arrivals) setPace(d time.Duration) bool {
	if !slices.Contains(arrivalPaces, d) {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.every = d
	return true
}

// arrivalStep is what the clock calls for on one check.
type arrivalStep struct {
	incident, ref, phone string
	ask                  bool   // open a new ask
	lapsed               bool   // the open ask went unanswered
	token                string // the session to close
}

// tick opens the asks that are due and lapses those left unanswered,
// following the board: an incident closed or merged is no longer asked.
func (r *arrivals) tick(b board, now time.Time) []arrivalStep {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []arrivalStep
	for _, inc := range b.Incidents {
		c := r.checks[inc.ID]
		if c == nil {
			phone := r.reporters[inc.Reference]
			t1, err := time.Parse(time.RFC3339Nano, inc.T["T1"])
			if phone == "" || err != nil || ended(inc) {
				continue
			}
			c = &arrivalCheck{incident: inc.ID, ref: inc.Reference, phone: phone, t1: t1, base: t1}
			r.checks[inc.ID] = c
		}
		if !c.confirmed.IsZero() {
			continue
		}
		if ended(inc) {
			if c.open {
				out = append(out, arrivalStep{incident: c.incident, token: c.token})
				c.open, c.token = false, ""
			}
			continue
		}
		if c.open {
			last := &c.asks[len(c.asks)-1]
			if now.Sub(last.At) >= min(arrivalWindow, r.every) {
				last.Answer = answerNone
				out = append(out, arrivalStep{incident: c.incident, lapsed: true, token: c.token})
				c.open, c.token, c.base = false, "", last.At
			}
			continue
		}
		if !now.Before(c.next(r.every)) {
			c.asks = append(c.asks, arrivalAsk{At: now})
			c.open = true
			out = append(out, arrivalStep{incident: c.incident, ref: c.ref, phone: c.phone, ask: true})
		}
	}
	return out
}

func ended(inc boardIncident) bool { return inc.State == "closed" || inc.State == "merged" }

// opened records the push session of the ask just opened.
func (r *arrivals) opened(incidentID, token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.checks[incidentID]; c != nil && c.open {
		c.token = token
	}
}

var errNoAsk = errors.New("no question is waiting for this caller")

// answer records the caller's answer to the open ask, and returns its
// session to close.
func (r *arrivals) answer(incidentID, phone, ans, via string, now time.Time) (token string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.checks[incidentID]
	if c == nil || !c.open || (phone != "" && c.phone != phone) {
		return "", errNoAsk
	}
	last := &c.asks[len(c.asks)-1]
	last.Answer, last.AnsweredAt, last.Via = ans, &now, via
	token = c.token
	c.open, c.token, c.base = false, "", now
	if ans == answerYes {
		c.confirmed = now
	}
	return token, nil
}

// did notes what Sauti did on the latest "no".
func (r *arrivals) did(incidentID, what string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.checks[incidentID]; c != nil && len(c.asks) > 0 {
		c.asks[len(c.asks)-1].Did = what
	}
}

// paceWords says an interval as the caller hears it.
func paceWords(d time.Duration) string {
	if d >= time.Minute {
		return fmt.Sprintf("%d min", int(d/time.Minute))
	}
	return fmt.Sprintf("%d s", int(d/time.Second))
}

func arrivalQuestion(ref string) string {
	return "PENDING-MD-PI Sauti " + ref + ": has a health worker arrived yet?\n1 Yes\n2 No"
}

// arrivalView is a check as the board shows it.
type arrivalView struct {
	Incident    string       `json:"incident"`
	Phone       string       `json:"phone"`
	EverySec    int          `json:"everySec"`
	Asks        []arrivalAsk `json:"asks"`
	Open        *openAsk     `json:"open,omitempty"`
	ConfirmedAt *time.Time   `json:"confirmedAt,omitempty"`
	After       *time.Time   `json:"after,omitempty"` // with ConfirmedAt: help arrived after this
	Next        *time.Time   `json:"next,omitempty"`
}

type openAsk struct {
	At      time.Time `json:"at"`
	Session string    `json:"session"` // the USSD push session a basic phone answers on
	Text    string    `json:"text"`
}

func (r *arrivals) views(b board) []arrivalView {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []arrivalView{}
	for _, inc := range b.Incidents {
		c := r.checks[inc.ID]
		if c == nil {
			continue
		}
		v := arrivalView{Incident: c.incident, Phone: c.phone, EverySec: int(r.every / time.Second), Asks: append([]arrivalAsk{}, c.asks...)} // [] in JSON before the first ask
		switch {
		case !c.confirmed.IsZero():
			at, after := c.confirmed, c.t1
			for _, a := range c.asks {
				if a.Answer == answerNo && a.AnsweredAt != nil {
					after = *a.AnsweredAt
				}
			}
			v.ConfirmedAt, v.After = &at, &after
		case c.open:
			v.Open = &openAsk{At: c.asks[len(c.asks)-1].At, Session: c.token, Text: arrivalQuestion(c.ref)}
		case !ended(inc):
			n := c.next(r.every)
			v.Next = &n
		}
		out = append(out, v)
	}
	return out
}

// pressed is what a "no" calls for: the SMS to send, whether to wake
// dispatch, the roles to escalate, and what the caller and the board are told.
type pressed struct {
	sms      []outgoing
	wake     bool
	escalate []string
	did      []string
	told     bool // a responder on the way was told
	harder   bool // a role nobody accepted was pressed
	coord    bool // a coordinator has, or now gets, an unaccepted role
}

// pressFor decides what the caller's "no" calls for on an incident.
func pressFor(inc boardIncident, phones []phone) pressed {
	byLabel := map[string]phone{}
	for _, p := range phones {
		byLabel[p.Label] = p
	}
	looking := slices.Contains([]string{"dispatching", "assigned", "on_scene"}, inc.State)
	var out pressed
	for _, role := range []string{"vht", "transporter"} {
		var took string
		for _, a := range inc.Alerts {
			if a.Role == role && a.Outcome == "accepted" {
				took = a.Responder
			}
		}
		if took != "" {
			if p := byLabel[took]; p.e164 != "" {
				out.sms = append(out.sms, outgoing{to: p.e164, text: fmt.Sprintf("PENDING-SMS Sauti %s: the caller says help has not arrived yet and is still waiting.", inc.Reference)})
				out.did = append(out.did, "told "+took+" the caller is waiting")
				out.told = true
			}
			continue
		}
		out.wake, out.harder = true, true
		asked := 0
		for _, a := range inc.Alerts {
			if a.Role != role || !(a.Outcome == "pending" || (a.Outcome == "no_response" && looking)) {
				continue
			}
			asked++
			if p := byLabel[a.Responder]; p.e164 != "" {
				out.sms = append(out.sms, outgoing{to: p.e164, text: fmt.Sprintf("PENDING-SMS Sauti %s: the caller is still waiting and nobody has taken this. Dial %s to accept.", inc.Reference, responderCode)})
				out.did = append(out.did, "reminded "+a.Responder)
			}
		}
		has := slices.ContainsFunc(inc.Escalations, func(e boardEscalation) bool { return e.Role == role })
		switch {
		case has:
			out.coord = true
		case asked == 0:
			out.escalate = append(out.escalate, role)
			out.did = append(out.did, "a coordinator alerted for the "+role)
			out.coord = true
		}
	}
	if out.wake {
		out.did = append(out.did, "dispatch retried at once")
	}
	return out
}

// callerSorry is the caller's last screen after a "no".
func callerSorry(p pressed, every time.Duration) string {
	var parts []string
	if p.told {
		parts = append(parts, "Sauti has told your helpers you are still waiting.")
	}
	if p.harder {
		parts = append(parts, "Sauti is looking harder for help.")
	}
	if p.coord {
		parts = append(parts, "A coordinator knows.")
	}
	return "PENDING-MD-PI Sorry. " + strings.Join(parts, " ") + " We will ask again in " + paceWords(every) + "."
}

// arrivalTick runs the clock's steps: each new ask opens a push session on
// the caller's phone; a lapsed or ended ask closes its own.
func (a *app) arrivalTick(st *stack, b board, now time.Time) {
	for _, s := range st.arrivals.tick(b, now) {
		if s.token != "" {
			st.hangUp(s.token)
		}
		if !s.ask {
			continue
		}
		tok := secret()
		st.mu.Lock()
		st.sessions[tok] = &session{s: &pushSession{a: a, st: st, incident: s.incident, ref: s.ref}, used: now}
		st.mu.Unlock()
		st.arrivals.opened(s.incident, tok)
	}
}

// answerArrival records a caller's answer and, on a "no", presses the
// search. It returns the caller's last screen.
func (a *app) answerArrival(ctx context.Context, st *stack, incidentID, phone string, yes bool, via string) (string, error) {
	ans := answerNo
	if yes {
		ans = answerYes
	}
	tok, err := st.arrivals.answer(incidentID, phone, ans, via, time.Now().UTC())
	if err != nil {
		return "", err
	}
	if tok != "" && via != viaUSSD {
		st.hangUp(tok) // a USSD answer ends its own session
	}
	if yes {
		return "PENDING-MD-PI Thank you. Sauti has noted that help arrived.", nil
	}
	b, err := st.readBoard(ctx, a.cfg, a.gaz)
	if err != nil {
		return "", err
	}
	var inc boardIncident
	for _, x := range b.Incidents {
		if x.ID == incidentID {
			inc = x
		}
	}
	p := pressFor(inc, st.phones)
	for _, m := range p.sms {
		st.sink.add(m.to, m.text)
	}
	id, err := uuid.Parse(incidentID)
	if err != nil {
		return "", err
	}
	for _, role := range p.escalate {
		payload, _ := json.Marshal(map[string]string{"reason": incident.ReasonNoEligibleResponder, "role": role})
		if _, err := st.dispatcher.Append(ctx, incident.AppendParams{IncidentID: id, Type: incident.EvEscalated, Payload: payload,
			ActorKind: incident.ActorSystem, IdemKey: "demo-arrival:" + incidentID + ":" + role}); err != nil && !errors.Is(err, incident.ErrIllegalTransition) {
			return "", err
		}
	}
	if p.wake {
		if err := st.dispatcher.WakeOpenDispatch(ctx); err != nil {
			return "", err
		}
	}
	st.arrivals.did(incidentID, strings.Join(p.did, "; "))
	return callerSorry(p, st.arrivals.pace()), nil
}

// pushSession is a USSD push on the caller's phone: the network opens the
// session, so its first screen is already on the handset.
type pushSession struct {
	a        *app
	st       *stack
	incident string
	ref      string
}

func (p *pushSession) Send(input string) (sim.Screen, error) {
	ctx, cancel := context.WithTimeout(p.st.ctx, 10*time.Second)
	defer cancel()
	switch strings.TrimSpace(input) {
	case "":
		return sim.Screen{Text: arrivalQuestion(p.ref)}, nil
	case "1", "2":
		text, err := p.a.answerArrival(ctx, p.st, p.incident, "", input == "1", viaUSSD)
		if errors.Is(err, errNoAsk) {
			return end("PENDING-MD-PI Sauti: this question has closed. We will ask again."), nil
		}
		if err != nil {
			p.a.log.Error("demo: arrival answer", "err", err)
			return end("PENDING-MD-PI Sauti could not record that. We will ask again."), nil
		}
		return end(text), nil
	default:
		return sim.Screen{Text: menuInvalid + arrivalQuestion(p.ref)}, nil
	}
}

// ---------- POST /api/arrival: a smartphone's answer ----------

type arrivalRequest struct {
	Phone    string `json:"phone"`
	Incident string `json:"incident"`
	Answer   string `json:"answer"` // yes or no
}

func (a *app) arrival(w http.ResponseWriter, r *http.Request) {
	st := a.current(w)
	if st == nil {
		return
	}
	var q arrivalRequest
	if !readJSON(w, r, &q) {
		return
	}
	if q.Answer != answerYes && q.Answer != answerNo {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "answer yes or no"})
		return
	}
	if _, err := uuid.Parse(q.Incident); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown incident"})
		return
	}
	text, err := a.answerArrival(r.Context(), st, q.Incident, q.Phone, q.Answer == answerYes, viaApp)
	switch {
	case errors.Is(err, errNoAsk):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "This question has closed. Sauti will ask again."})
	case err != nil:
		a.log.Error("demo: arrival", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "The answer could not be recorded."})
	default:
		writeJSON(w, http.StatusOK, map[string]string{"text": text})
	}
}

// arrivalPace sets how often callers are asked, for this run.
func (a *app) arrivalPace(w http.ResponseWriter, r *http.Request) {
	st := a.current(w)
	if st == nil {
		return
	}
	var q struct {
		Seconds int `json:"seconds"`
	}
	if !readJSON(w, r, &q) {
		return
	}
	if !st.arrivals.setPace(time.Duration(q.Seconds) * time.Second) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "choose 300, 60 or 30 seconds"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"seconds": q.Seconds})
}
