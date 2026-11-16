package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/platform/sim"
)

// The VHT and the transporter of one incident, told about each other: a demo
// preview of responder coordination.
//
// Once a VHT and a transporter have both accepted an incident, the transporter
// is sent where the VHT is (with the distance to the patient), the health
// facility the VHT works at, and the number to call them on, so the rider can
// decide whether to pick the VHT up on the way or go straight to the patient.
// The VHT is sent which transporter is coming and their number.
//
//   - A VHT on a smartphone shares at once: the app fills in the facility and
//     number they registered with and the phone's location (simulated GPS in
//     the demo), through POST /api/share.
//   - A VHT on a basic phone answers the "My incident" menu on *384*3#: the
//     facility they work at, whether this phone's number is the one to call,
//     then their ward and nearest landmark, as the caller's screens 6 and 7
//     do. If they have not shared a few seconds after both roles accepted,
//     their SMS says to dial it.
//
// The same menu lets a basic phone record what a smartphone records (arrival,
// leaving with the patient, the arrival at the facility) through progress.go,
// so a call answered on basic phones can be completed too.
//
// This is a preview: no Sauti service records these details yet. The wording
// is a placeholder (PENDING-RESPONDER, PENDING-SMS); the SMS go straight into
// the demo's SMS simulator, not through dispatch-worker; and the details stay
// in the demo process, never in the operational store or the audit ledger.

const incidentCode = "*384*3#"

// vhtFacility is the health facility each VHT registered with, which their
// smartphone app fills in. Synthetic, like every demo responder.
var vhtFacility = map[string]string{"VHT 1": "hc_test_1", "VHT 2": "hc_test_2", "VHT 3": "hc_test_1", "VHT 4": "hc_test_1"}

// shareGrace is how long after both roles accepted a VHT who has not shared
// is sent the prompt to dial the menu: a smartphone shares well within it.
const shareGrace = 8 * time.Second

// The ways a VHT shares.
const (
	viaApp  = "app"
	viaUSSD = "ussd"
)

// vhtShare is what a VHT shared for one incident: the latest replaces it.
type vhtShare struct {
	vht      string // the VHT's handle
	facility string // a gazetteer facility ID
	landmark string // a gazetteer landmark ID: chosen on USSD, or the nearest to the phone's fix
	point    location.Point
	number   pii.Phone // the number to call the VHT on
	via      string
	version  int // counts the shares, so a move tells the transporter again
	at       time.Time
}

// coordination keeps one run's shares and what each responder was told.
type coordination struct {
	mu      sync.Mutex
	shares  map[string]*vhtShare // incident ID → the VHT's latest share
	toldTr  map[string]int       // incident ID → the share version the transporter was sent
	toldVHT map[string]bool      // incident ID → the VHT was told who is coming
	paired  map[string]time.Time // incident ID → when both roles were first seen accepted
}

func newCoordination() *coordination {
	return &coordination{shares: map[string]*vhtShare{}, toldTr: map[string]int{}, toldVHT: map[string]bool{}, paired: map[string]time.Time{}}
}

// put records a share, keeping the count of earlier ones.
func (c *coordination) put(incidentID string, s vhtShare) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old := c.shares[incidentID]; old != nil && old.vht == s.vht {
		s.version = old.version
	}
	s.version++
	c.shares[incidentID] = &s
}

func (c *coordination) get(incidentID string) (vhtShare, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.shares[incidentID]
	if s == nil {
		return vhtShare{}, false
	}
	return *s, true
}

// outgoing is one SMS the demo sends.
type outgoing struct {
	to   string // E.164
	text string
}

// pair is an incident's accepted VHT and transporter, by label; either may be
// empty.
func pair(inc boardIncident) (vht, transporter string) {
	for _, a := range inc.Alerts {
		if a.Outcome != "accepted" {
			continue
		}
		switch a.Role {
		case "vht":
			vht = a.Responder
		case "transporter":
			transporter = a.Responder
		}
	}
	return vht, transporter
}

// open: the incident has responders on the way or on the job.
func open(inc boardIncident) bool {
	return slices.Contains([]string{"assigned", "on_scene", "transporting", "at_facility"}, inc.State)
}

// due returns the SMS the board now calls for, and marks them sent: the
// transporter is sent the VHT's details once both accepted and the VHT
// shared (again after each new share); the VHT is sent who is coming, with
// the prompt to share when they have not within shareGrace.
func (c *coordination) due(b board, now time.Time, phones []phone, gaz location.File, geo mapView) []outgoing {
	byLabel := map[string]phone{}
	for _, p := range phones {
		byLabel[p.Label] = p
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []outgoing
	for _, inc := range b.Incidents {
		vhtLabel, trLabel := pair(inc)
		if !open(inc) || vhtLabel == "" || trLabel == "" {
			continue
		}
		vht, tr := byLabel[vhtLabel], byLabel[trLabel]
		if vht.e164 == "" || tr.e164 == "" {
			continue
		}
		if _, ok := c.paired[inc.ID]; !ok {
			c.paired[inc.ID] = now
		}
		s := c.shares[inc.ID]
		if s != nil && s.vht != vht.Handle {
			s = nil // shared by a VHT no longer on the incident
		}
		if s != nil && c.toldTr[inc.ID] < s.version {
			out = append(out, outgoing{to: tr.e164, text: transporterSMS(inc, vhtLabel, *s, c.toldTr[inc.ID] > 0, gaz, geo)})
			c.toldTr[inc.ID] = s.version
		}
		if c.toldVHT[inc.ID] {
			continue
		}
		switch {
		case s != nil:
			out = append(out, outgoing{to: vht.e164, text: fmt.Sprintf("PENDING-SMS Sauti %s: %s is coming to help. Call %s. They have your place and number.",
				inc.Reference, trLabel, tr.e164)})
			c.toldVHT[inc.ID] = true
		case now.Sub(c.paired[inc.ID]) >= shareGrace:
			out = append(out, outgoing{to: vht.e164, text: fmt.Sprintf("PENDING-SMS Sauti %s: %s is coming to help. Call %s. Dial %s to send them where you are.",
				inc.Reference, trLabel, tr.e164, incidentCode)})
			c.toldVHT[inc.ID] = true
		}
	}
	return out
}

// transporterSMS is the transporter's message: where the VHT is and how far
// from the patient, the facility they work at, and their number.
func transporterSMS(inc boardIncident, vhtLabel string, s vhtShare, update bool, gaz location.File, geo mapView) string {
	lm, ward := landmarkNames(gaz, s.landmark)
	now := ""
	if update {
		now = "now "
	}
	far := ""
	if km, ok := kmToPatient(inc, s.point, gaz, geo); ok {
		far = fmt.Sprintf(", %s km from the patient", strconv.FormatFloat(km, 'f', 1, 64))
	}
	return fmt.Sprintf("PENDING-SMS Sauti %s: %s is %snear %s, %s%s. Works at %s. Call %s.",
		inc.Reference, vhtLabel, now, lm, ward, far, facilityName(gaz, s.facility), s.number.E164())
}

// landmarkNames returns a landmark's English name and its ward's.
func landmarkNames(gaz location.File, id string) (landmark, ward string) {
	for _, l := range gaz.Landmarks {
		if l.ID == id {
			landmark = l.Name.EN
			for _, w := range gaz.Wards {
				if w.ID == l.WardID {
					ward = w.Name.EN
				}
			}
		}
	}
	return landmark, ward
}

func facilityName(gaz location.File, id string) string {
	for _, f := range gaz.Facilities {
		if f.ID == id {
			return f.Name.EN
		}
	}
	return ""
}

// patientPoint is where the incident is: its landmark, else its ward's centre.
func patientPoint(inc boardIncident, gaz location.File, geo mapView) (location.Point, bool) {
	for _, l := range gaz.Landmarks {
		if l.ID == inc.LandmarkID {
			return l.Point, true
		}
	}
	for _, w := range geo.Wards {
		if w.ID == inc.WardID {
			return location.Point{Lat: w.Lat, Lon: w.Lon}, true
		}
	}
	return location.Point{}, false
}

// kmToPatient is the distance from p to the incident, in km to one decimal.
func kmToPatient(inc boardIncident, p location.Point, gaz location.File, geo mapView) (float64, bool) {
	at, ok := patientPoint(inc, gaz, geo)
	if !ok {
		return 0, false
	}
	return math.Round(distanceM(p, at)/100) / 10, true
}

// distanceM is the great-circle distance in metres (haversine; the wards
// span a few kilometres), as the UI's geo.js computes it.
func distanceM(a, b location.Point) float64 {
	const r = 6371000
	rad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat, dLon := rad(b.Lat-a.Lat), rad(b.Lon-a.Lon)
	s := math.Pow(math.Sin(dLat/2), 2) + math.Cos(rad(a.Lat))*math.Cos(rad(b.Lat))*math.Pow(math.Sin(dLon/2), 2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(s)))
}

// nearestLandmark is the gazetteer landmark nearest p.
func nearestLandmark(gaz location.File, p location.Point) location.Landmark {
	var best location.Landmark
	bestD := math.Inf(1)
	for _, l := range gaz.Landmarks {
		if d := distanceM(p, l.Point); d < bestD {
			best, bestD = l, d
		}
	}
	return best
}

// shareView is a share as the board shows it.
type shareView struct {
	Incident   string    `json:"incident"`
	VHT        string    `json:"vht"` // a label
	Facility   string    `json:"facility"`
	FacilityID string    `json:"facilityId"`
	Landmark   string    `json:"landmark"`
	LandmarkID string    `json:"landmarkId"`
	Ward       string    `json:"ward"`
	Lat        float64   `json:"lat"`
	Lon        float64   `json:"lon"`
	KM         *float64  `json:"km,omitempty"` // to the patient
	Number     pii.Phone `json:"number"`       // masked in JSON
	Via        string    `json:"via"`
	Told       bool      `json:"told"` // the transporter has been sent the latest
	At         time.Time `json:"at"`
}

// views lists the shares of the board's incidents.
func (c *coordination) views(b board, phones []phone, gaz location.File, geo mapView) []shareView {
	label := map[string]string{}
	for _, p := range phones {
		label[p.Handle] = p.Label
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []shareView{}
	for _, inc := range b.Incidents {
		s := c.shares[inc.ID]
		if s == nil {
			continue
		}
		lm, ward := landmarkNames(gaz, s.landmark)
		v := shareView{Incident: inc.ID, VHT: label[s.vht], Facility: facilityName(gaz, s.facility), FacilityID: s.facility,
			Landmark: lm, LandmarkID: s.landmark, Ward: ward, Lat: s.point.Lat, Lon: s.point.Lon, Number: s.number, Via: s.via,
			Told: c.toldTr[inc.ID] >= s.version, At: s.at}
		if km, ok := kmToPatient(inc, s.point, gaz, geo); ok {
			v.KM = &km
		}
		out = append(out, v)
	}
	return out
}

// coordinate sends what due calls for, each second, until the stack stops.
func (a *app) coordinate(st *stack) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-st.ctx.Done():
			return
		case <-t.C:
		}
		b, err := st.readBoard(st.ctx, a.cfg, a.gaz)
		if err != nil {
			if st.ctx.Err() == nil {
				a.log.Warn("demo: coordination could not read the board", "err", err)
			}
			continue
		}
		for _, m := range st.coord.due(b, time.Now(), st.phones, a.gaz, a.geo) {
			st.sink.add(m.to, m.text)
		}
		a.arrivalTick(st, b, time.Now().UTC())
	}
}

// ---------- POST /api/share: a VHT's smartphone ----------

type shareRequest struct {
	Phone    string  `json:"phone"`    // the VHT's handle
	Incident string  `json:"incident"` // the incident's ID
	Facility string  `json:"facility"` // a gazetteer facility: the one they registered with
	Lat      float64 `json:"lat"`      // the phone's fix
	Lon      float64 `json:"lon"`
}

// shareMargin is how far outside the wards' landmarks a fix may lie, in
// degrees (about a kilometre).
const shareMargin = 0.01

// checkShare validates a smartphone's share against the demo's phones, the
// gazetteer and the map, before anything is read or written.
func checkShare(q shareRequest, phones []phone, gaz location.File, geo mapView) (phone, uuid.UUID, error) {
	id, err := uuid.Parse(q.Incident)
	if err != nil {
		return phone{}, uuid.UUID{}, refuse(http.StatusBadRequest, "unknown incident")
	}
	var p phone
	for _, x := range phones {
		if x.Handle == q.Phone && x.Kind == "responder" {
			p = x
		}
	}
	if p.Handle == "" || p.Role != "vht" {
		return phone{}, uuid.UUID{}, refuse(http.StatusBadRequest, "only a VHT's phone shares where they are")
	}
	if facilityName(gaz, q.Facility) == "" {
		return phone{}, uuid.UUID{}, refuse(http.StatusBadRequest, "unknown facility")
	}
	if !inArea(geo, q.Lat, q.Lon) {
		return phone{}, uuid.UUID{}, refuse(http.StatusBadRequest, "That location is outside the demo's wards.")
	}
	return p, id, nil
}

// inArea: (lat, lon) lies within shareMargin of the wards and landmarks.
func inArea(geo mapView, lat, lon float64) bool {
	if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) {
		return false
	}
	minLat, maxLat, minLon, maxLon := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for _, p := range append(slices.Clone(geo.Wards), geo.Landmarks...) {
		minLat, maxLat = math.Min(minLat, p.Lat), math.Max(maxLat, p.Lat)
		minLon, maxLon = math.Min(minLon, p.Lon), math.Max(maxLon, p.Lon)
	}
	return lat >= minLat-shareMargin && lat <= maxLat+shareMargin && lon >= minLon-shareMargin && lon <= maxLon+shareMargin
}

// accepted: the responder holds the accepted alert of an incident still open.
func (st *stack) accepted(ctx context.Context, p phone, id uuid.UUID) (bool, error) {
	var ok bool
	err := st.ro.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM op.alerts a JOIN op.incidents i ON i.id = a.incident_id
		WHERE a.incident_id = $1 AND a.responder_id = $2::uuid AND a.outcome = 'accepted' AND i.state NOT IN ('closed', 'merged'))`,
		id, st.ids[p.Handle]).Scan(&ok)
	return ok, err
}

// transporterOn names the transporter who accepted the incident, or "".
func (st *stack) transporterOn(ctx context.Context, id uuid.UUID) (string, error) {
	var rid string
	err := st.ro.QueryRow(ctx, `SELECT responder_id::text FROM op.alerts
		WHERE incident_id = $1 AND role = 'transporter' AND outcome = 'accepted' LIMIT 1`, id).Scan(&rid)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return st.labels[rid], nil
}

// share records a smartphone's share and answers where it put the VHT.
func (a *app) share(w http.ResponseWriter, r *http.Request) {
	st := a.current(w)
	if st == nil {
		return
	}
	var q shareRequest
	if !readJSON(w, r, &q) {
		return
	}
	p, id, err := checkShare(q, st.phones, a.gaz, a.geo)
	if err == nil {
		var ok bool
		if ok, err = st.accepted(r.Context(), p, id); err == nil && !ok {
			err = refuse(http.StatusConflict, "%s has not accepted this incident.", p.Label)
		}
	}
	if err == nil {
		fix := location.Point{Lat: q.Lat, Lon: q.Lon}
		lm := nearestLandmark(a.gaz, fix)
		st.coord.put(id.String(), vhtShare{vht: p.Handle, facility: q.Facility, landmark: lm.ID, point: fix, number: p.Number, via: viaApp, at: time.Now().UTC()})
		name, ward := landmarkNames(a.gaz, lm.ID)
		var to string
		if to, err = st.transporterOn(r.Context(), id); err == nil {
			writeJSON(w, http.StatusOK, map[string]string{"landmark": name, "ward": ward, "transporter": to})
			return
		}
	}
	var pe *progressError
	if errors.As(err, &pe) {
		writeJSON(w, pe.status, map[string]string{"error": pe.msg})
		return
	}
	a.log.Error("demo: share", "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "The details could not be shared."})
}

// ---------- *384*3#: the "My incident" menu, for basic phones ----------

// ussdDialer opens a session on a service code; ussdSession takes its inputs.
type (
	ussdDialer  interface{ Dial(e164 string) ussdSession }
	ussdSession interface {
		Send(input string) (sim.Screen, error)
	}
)

// intakeRoute is a service code intake-gw serves, through its callback.
type intakeRoute struct{ *sim.USSD }

func (r intakeRoute) Dial(e164 string) ussdSession { return r.USSD.Dial(e164) }

// menuRoute is *384*3#, served by the demo process itself.
type menuRoute struct {
	a  *app
	st *stack
}

func (r menuRoute) Dial(e164 string) ussdSession { return &menuSession{a: r.a, st: r.st, e164: e164} }

// The menu's screens.
const (
	mRoot     = "root"
	mFacility = "facility" // the VHT's facility
	mNumber   = "number"   // this phone's number, or another
	mTyped    = "typed"    // another number, typed
	mWard     = "ward"
	mLandmark = "landmark"
	mDest     = "dest" // the transporter's destination facility
)

const menuInvalid = "Choose from the list.\n"

// menuSession is one session of the menu. Inputs arrive one at a time, as
// the demo's USSD handler sends them.
type menuSession struct {
	a    *app
	st   *stack
	e164 string

	p        phone
	inc      boardIncident
	screen   string
	text     string   // the screen shown, for an answer that is not on it
	keys     []string // what each numbered option means
	facility string
	number   pii.Phone
	ward     string
}

func (m *menuSession) Send(input string) (sim.Screen, error) {
	ctx, cancel := context.WithTimeout(m.st.ctx, 10*time.Second)
	defer cancel()
	if m.screen == "" {
		return m.root(ctx)
	}
	in := strings.TrimSpace(input)
	if m.screen == mTyped {
		n, err := pii.ParsePhone(in)
		if err != nil {
			return m.show(mTyped, "PENDING-RESPONDER That is not a phone number. Type the number to call you on", nil), nil
		}
		m.number = n
		return m.wardScreen(), nil
	}
	k, err := strconv.Atoi(in)
	if err != nil || k < 1 || k > len(m.keys) {
		return sim.Screen{Text: menuInvalid + m.text}, nil
	}
	choice := m.keys[k-1]
	switch m.screen {
	case mRoot:
		switch choice {
		case "share":
			return m.facilityScreen(), nil
		case stepAtFacility:
			return m.destScreen(), nil
		default:
			return m.record(ctx, choice, ""), nil
		}
	case mFacility:
		m.facility = choice
		return m.show(mNumber, "PENDING-RESPONDER Is this phone's number the one to call you on?", [][2]string{{"yes", "Yes"}, {"no", "No, type another"}}), nil
	case mNumber:
		if choice == "no" {
			return m.show(mTyped, "PENDING-RESPONDER Type the number to call you on", nil), nil
		}
		m.number = m.p.Number
		return m.wardScreen(), nil
	case mWard:
		m.ward = choice
		return m.landmarkScreen(), nil
	case mLandmark:
		return m.finishShare(ctx, choice), nil
	case mDest:
		return m.record(ctx, stepAtFacility, choice), nil
	}
	return end("PENDING-RESPONDER Session ended."), nil
}

func end(text string) sim.Screen { return sim.Screen{Text: text, End: true} }

// show sets the screen: a prompt, then each option numbered from 1.
func (m *menuSession) show(screen, prompt string, opts [][2]string) sim.Screen {
	m.screen, m.keys = screen, nil
	lines := []string{prompt}
	for i, o := range opts {
		m.keys = append(m.keys, o[0])
		lines = append(lines, fmt.Sprintf("%d %s", i+1, o[1]))
	}
	m.text = strings.Join(lines, "\n")
	return sim.Screen{Text: m.text}
}

// job finds the responder's open job: the incident whose alert they accepted.
func (m *menuSession) job(ctx context.Context) (boardIncident, bool, error) {
	b, err := m.st.readBoard(ctx, m.a.cfg, m.a.gaz)
	if err != nil {
		return boardIncident{}, false, err
	}
	for _, inc := range b.Incidents { // newest first
		if inc.State == "closed" || inc.State == "merged" {
			continue
		}
		for _, a := range inc.Alerts {
			if a.Responder == m.p.Label && a.Outcome == "accepted" {
				return inc, true, nil
			}
		}
	}
	return boardIncident{}, false, nil
}

func (m *menuSession) root(ctx context.Context) (sim.Screen, error) {
	for _, p := range m.st.phones {
		if p.e164 == m.e164 && p.Kind == "responder" {
			m.p = p
		}
	}
	if m.p.Handle == "" {
		return end("PENDING-RESPONDER This number is not registered as a Sauti responder."), nil
	}
	inc, ok, err := m.job(ctx)
	if err != nil {
		return sim.Screen{}, err
	}
	if !ok {
		return end("PENDING-RESPONDER Sauti: you have no open incident. Accept an alert on " + responderCode + " first."), nil
	}
	m.inc = inc
	head := fmt.Sprintf("PENDING-RESPONDER My incident %s\n%s", inc.Reference, placeOf(inc))
	var opts [][2]string
	step := nextStep(inc, m.p.Role)
	if m.p.Role == "vht" {
		label := "Send the transporter where I am"
		if s, ok := m.st.coord.get(inc.ID); ok && s.vht == m.p.Handle {
			label = "Update where I am"
		}
		opts = append(opts, [2]string{"share", label})
	} else {
		vht, _ := pair(inc)
		if s, ok := m.st.coord.get(inc.ID); ok && vht != "" {
			lm, _ := landmarkNames(m.a.gaz, s.landmark)
			head += fmt.Sprintf("\n%s: near %s", vht, lm)
		} else if vht != "" {
			head += "\n" + vht + " is coming"
		}
	}
	if step != "" {
		opts = append(opts, [2]string{step, stepWords[step]})
	}
	if len(opts) == 0 {
		return end(head + "\nNothing to record now."), nil
	}
	return m.show(mRoot, head, opts), nil
}

// stepWords are the menu's words for each step.
var stepWords = map[string]string{stepArrived: "I have arrived", stepDeparted: "Patient picked up, leaving now", stepAtFacility: "Arrived at the facility"}

func placeOf(inc boardIncident) string {
	var parts []string
	if inc.Landmark != "" && inc.Landmark != "other" {
		parts = append(parts, inc.Landmark)
	}
	if inc.Ward != "" {
		parts = append(parts, inc.Ward)
	}
	if len(parts) == 0 {
		return "Place not given yet"
	}
	return strings.Join(parts, ", ")
}

// nextStep is the step a responder of role records next, as the UI's
// responders.js decides it: a VHT attests arrival; a transporter arrives,
// leaves with the patient, then reaches the facility.
func nextStep(inc boardIncident, role string) string {
	if inc.State == "closed" || inc.State == "merged" || inc.State == "at_facility" {
		return ""
	}
	if !arrivedRoles(inc)[role] {
		if inc.State == "assigned" || inc.State == "on_scene" {
			return stepArrived
		}
		return ""
	}
	if role != "transporter" {
		return ""
	}
	if inc.T["T6"] == "" {
		if inc.State == "on_scene" {
			return stepDeparted
		}
		return ""
	}
	if inc.T["T7"] == "" {
		return stepAtFacility
	}
	return ""
}

// arrivedRoles are the roles whose arrival is recorded on the incident.
func arrivedRoles(inc boardIncident) map[string]bool {
	out := map[string]bool{}
	for _, e := range inc.Events {
		if e.Type != string(incident.EvArrived) {
			continue
		}
		var p struct {
			Role string `json:"role"`
		}
		if json.Unmarshal([]byte(e.Payload), &p) == nil && p.Role != "" {
			out[p.Role] = true
		}
	}
	return out
}

func (m *menuSession) facilityScreen() sim.Screen {
	var opts [][2]string
	for _, f := range m.a.gaz.Facilities {
		opts = append(opts, [2]string{f.ID, f.Name.EN})
	}
	return m.show(mFacility, "PENDING-RESPONDER Which health facility do you work at?", opts)
}

func (m *menuSession) wardScreen() sim.Screen {
	var opts [][2]string
	for _, w := range m.a.gaz.Wards {
		opts = append(opts, [2]string{w.ID, w.Name.EN})
	}
	return m.show(mWard, "PENDING-RESPONDER Which ward are you in?", opts)
}

// landmarkScreen lists the ward's menu landmarks, as the caller's screen 7.
func (m *menuSession) landmarkScreen() sim.Screen {
	return m.show(mLandmark, "PENDING-RESPONDER Nearest place to you?", menuLandmarks(m.a.gaz, m.ward))
}

func menuLandmarks(gaz location.File, ward string) [][2]string {
	var ls []location.Landmark
	for _, l := range gaz.Landmarks {
		if l.WardID == ward && l.Menu > 0 {
			ls = append(ls, l)
		}
	}
	slices.SortFunc(ls, func(a, b location.Landmark) int { return a.Menu - b.Menu })
	var opts [][2]string
	for _, l := range ls {
		opts = append(opts, [2]string{l.ID, l.Name.EN})
	}
	return opts
}

// destScreen lists the facilities, nearest the patient first.
func (m *menuSession) destScreen() sim.Screen {
	fs := slices.Clone(m.a.gaz.Facilities)
	if at, ok := patientPoint(m.inc, m.a.gaz, m.a.geo); ok {
		slices.SortStableFunc(fs, func(a, b location.Facility) int {
			return cmp.Compare(distanceM(at, a.Point), distanceM(at, b.Point))
		})
	}
	var opts [][2]string
	for _, f := range fs {
		opts = append(opts, [2]string{f.ID, f.Name.EN})
	}
	return m.show(mDest, "PENDING-RESPONDER Which facility?", opts)
}

// finishShare stores the VHT's share, chosen on the menu.
func (m *menuSession) finishShare(ctx context.Context, landmarkID string) sim.Screen {
	var point location.Point
	for _, l := range m.a.gaz.Landmarks {
		if l.ID == landmarkID {
			point = l.Point
		}
	}
	id, err := uuid.Parse(m.inc.ID)
	if err != nil {
		return end("PENDING-RESPONDER Sauti: that incident is no longer open.")
	}
	if ok, err := m.st.accepted(ctx, m.p, id); err != nil || !ok {
		return end("PENDING-RESPONDER Sauti: that incident is no longer open.")
	}
	m.st.coord.put(m.inc.ID, vhtShare{vht: m.p.Handle, facility: m.facility, landmark: landmarkID, point: point, number: m.number, via: viaUSSD, at: time.Now().UTC()})
	lm, ward := landmarkNames(m.a.gaz, landmarkID)
	if to, err := m.st.transporterOn(ctx, id); err == nil && to != "" {
		return end(fmt.Sprintf("PENDING-RESPONDER Sent to %s: you are near %s, %s.", to, lm, ward))
	}
	return end(fmt.Sprintf("PENDING-RESPONDER Saved: near %s, %s. Sauti sends it to the transporter who accepts.", lm, ward))
}

// record records a step through progress.go, as a smartphone does.
func (m *menuSession) record(ctx context.Context, step, facility string) sim.Screen {
	q := progressRequest{Phone: m.p.Handle, Incident: m.inc.ID, Step: step, Facility: facility}
	p, id, err := checkProgress(q, m.st.phones, m.a.gaz)
	if err == nil {
		_, err = m.st.recordProgress(ctx, p, id, q)
	}
	var pe *progressError
	switch {
	case err == nil:
	case errors.As(err, &pe):
		return end("PENDING-RESPONDER " + pe.msg)
	case errors.Is(err, incident.ErrIllegalTransition), errors.Is(err, incident.ErrNotFound):
		return end("PENDING-RESPONDER " + refusal(step))
	default:
		m.a.log.Error("demo: menu step", "step", step, "err", err)
		return end("PENDING-RESPONDER The step could not be recorded. Try again.")
	}
	switch step {
	case stepArrived:
		return end(fmt.Sprintf("PENDING-RESPONDER Arrival recorded for %s.", m.inc.Reference))
	case stepDeparted:
		return end(fmt.Sprintf("PENDING-RESPONDER %s: on the way to the facility.", m.inc.Reference))
	default:
		return end(fmt.Sprintf("PENDING-RESPONDER Handed over at %s. Job complete, thank you.", facilityName(m.a.gaz, facility)))
	}
}
