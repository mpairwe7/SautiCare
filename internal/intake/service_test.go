package intake

import (
	"context"
	"crypto/sha256"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/pii"
)

type memSteps struct {
	mu    sync.Mutex
	m     map[string]Step
	order map[string][]Step
}

func key(p, s string, mac crypto.MAC) string { return p + "|" + s + "|" + string(mac.Sum) }

func (m *memSteps) Lookup(_ context.Context, p, s string, macs []crypto.MAC) (Step, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, mac := range macs {
		if st, ok := m.m[key(p, s, mac)]; ok {
			return st, true, nil
		}
	}
	return Step{}, false, nil
}

func (m *memSteps) Latest(_ context.Context, p, s string) (Step, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best Step
	found := false
	for _, st := range m.order[p+"|"+s] {
		if !found || st.ConsumedLen > best.ConsumedLen {
			best, found = st, true
		}
	}
	return best, found, nil
}

func (m *memSteps) Save(_ context.Context, p, s string, st Step) (Screen, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m, m.order = map[string]Step{}, map[string][]Step{}
	}
	if old, ok := m.m[key(p, s, st.MAC)]; ok {
		return old.Screen, nil
	}
	m.m[key(p, s, st.MAC)] = st
	m.order[p+"|"+s] = append(m.order[p+"|"+s], st)
	return st.Screen, nil
}

// fakeKeys is a keyed hash stand-in: deterministic, but never sha256(msg).
type fakeKeys struct{}

func (fakeKeys) StepMAC(_ context.Context, msg []byte) (crypto.MAC, error) {
	sum := sha256.Sum256(append([]byte("test-key|"), msg...))
	return crypto.MAC{KeyID: "k1", Sum: sum[:]}, nil
}

func (k fakeKeys) StepLookupMACs(ctx context.Context, msg []byte) ([]crypto.MAC, error) {
	m, err := k.StepMAC(ctx, msg)
	return []crypto.MAC{m}, err
}

type rig struct {
	svc   *Service
	inc   *memIncidents
	steps *memSteps
}

func newRig(t *testing.T, cls Classifier, match fakeMatcher) *rig {
	t.Helper()
	if cls == nil {
		cls = repoTriage(t)
	}
	r := &rig{inc: newMemIncidents(), steps: &memSteps{}}
	svc, err := New(Deps{Incidents: r.inc, Reader: r.inc, Steps: r.steps, Keys: fakeKeys{}, Machine: newMachine(t),
		Triage: cls, Gazetteer: repoGazetteer(t), Matcher: match, ResumeWindow: 10 * time.Minute, MinSimilarity: 0.45,
		Dispatcher: r.inc, Dedup: DedupRule{Window: 15 * time.Minute, MaxZoneDistance: 1}})
	if err != nil {
		t.Fatal(err)
	}
	r.svc = svc
	return r
}

type caller struct {
	t       *testing.T
	r       *rig
	req     USSDRequest
	started bool
}

func (r *rig) dial(t *testing.T, session, phone string) *caller {
	t.Helper()
	var p pii.Phone
	if phone != "" {
		var err error
		if p, err = pii.ParsePhone(phone); err != nil {
			t.Fatal(err)
		}
	}
	return &caller{t: t, r: r, req: USSDRequest{Provider: "at", SessionID: session, ServiceCode: "*384*1#", NetworkCode: "64110", Phone: p}}
}

// send delivers the next callback the way Africa's Talking does: "" first,
// then every answer so far joined by '*'.
func (c *caller) send(input string) Screen {
	c.t.Helper()
	switch {
	case !c.started:
		c.started = true
	case c.req.Text == "":
		c.req.Text = input
	default:
		c.req.Text += "*" + input
	}
	sc, err := c.r.svc.HandleUSSD(context.Background(), c.req)
	if err != nil {
		c.t.Fatalf("HandleUSSD(%q): %v", c.req.Text, err)
	}
	return sc
}

func (c *caller) incidentID() uuid.UUID {
	c.t.Helper()
	st, ok, _ := c.r.steps.Latest(context.Background(), c.req.Provider, c.req.SessionID)
	if !ok {
		c.t.Fatal("no step")
	}
	return st.IncidentID
}

func (c *caller) projection() incident.Projection {
	c.t.Helper()
	p, err := c.r.inc.Projection(context.Background(), c.incidentID())
	if err != nil {
		c.t.Fatal(err)
	}
	return p
}

// Scenario 3 (Build Prompt §16): poisoning with screen 5 = no is yellow, not
// green. The full walk also sets T2 after screen 7, before screen 8.
func TestUSSDFullWalkScenario3(t *testing.T) {
	r := newRig(t, nil, nil)
	c := r.dial(t, "S1", "0772123456")
	if sc := c.send(""); sc.End || !strings.Contains(sc.Text, "English") {
		t.Fatalf("first screen = %+v", sc)
	}
	for _, in := range []string{"1", "2", "4", "3", "2", "1"} {
		if sc := c.send(in); sc.End {
			t.Fatalf("ended early at %q: %+v", in, sc)
		}
	}
	callback := c.send("2") // screen 7: second landmark of the ward
	p := c.projection()
	if p.Severity != incident.SevYellow || p.State != incident.StateDispatching || p.T[2] == nil ||
		p.Zone == nil || p.LocationQuality != incident.QualityLandmark {
		t.Fatalf("after screen 7: %+v", p)
	}
	if !strings.Contains(callback.Text, "1 This number") {
		t.Fatalf("callback screen = %q", callback.Text)
	}
	done := c.send("1")
	if !done.End || !strings.Contains(done.Text, "HELP IS COMING") || !strings.Contains(done.Text, p.Reference.Spoken()) {
		t.Fatalf("closing screen = %+v", done)
	}
	var classified *incident.Event
	for _, e := range r.inc.allEvents() {
		if e.Type == incident.EvClassified {
			classified = &e
		}
	}
	if classified == nil || !strings.HasPrefix(classified.LogicVersion, "tool1-placeholder-0.1+sha256:") {
		t.Fatalf("classification event = %+v", classified)
	}
	if got := c.projection(); got.LastScreen != 8 {
		t.Fatalf("LastScreen = %d", got.LastScreen)
	}
}

func TestUSSDRetryWritesOneEvent(t *testing.T) {
	r := newRig(t, nil, nil)
	c := r.dial(t, "S1", "0772123456")
	c.send("")
	first := c.send("1")
	n := len(r.inc.allEvents())
	again, err := r.svc.HandleUSSD(context.Background(), c.req)
	if err != nil || again != first || len(r.inc.allEvents()) != n {
		t.Fatalf("retry: %+v (%v), events %d → %d", again, err, n, len(r.inc.allEvents()))
	}
}

// Scenario 5 (USSD part): the session drops at screen 6; a redial from the
// same number within the window resumes at screen 6, same incident and T1.
func TestUSSDResumesWithinWindow(t *testing.T) {
	r := newRig(t, nil, nil)
	c := r.dial(t, "S1", "0772123456")
	c.send("")
	for _, in := range []string{"1", "2", "1", "3", "2"} {
		c.send(in)
	}
	before := c.projection()
	r.inc.now = r.inc.now.Add(5 * time.Minute)
	d := r.dial(t, "S2", "+256772123456")
	ward := d.send("")
	if !strings.Contains(ward.Text, "Test Ward 1") || d.incidentID() != c.incidentID() {
		t.Fatalf("redial screen = %q (incident %s vs %s)", ward.Text, d.incidentID(), c.incidentID())
	}
	d.send("1")
	d.send("1")
	done := d.send("1")
	p := d.projection()
	if !done.End || p.LastScreen != 8 || !p.T[1].Equal(*before.T[1]) || p.T[2] == nil {
		t.Fatalf("after resume: %+v / %+v", done, p)
	}
	resumed := 0
	for _, e := range r.inc.allEvents() {
		if e.Type == incident.EvSessionResumed {
			resumed++
		}
	}
	if resumed != 1 {
		t.Fatalf("session.resumed events = %d", resumed)
	}
}

func TestUSSDDoesNotResumeAfterWindow(t *testing.T) {
	r := newRig(t, nil, nil)
	c := r.dial(t, "S1", "0772123456")
	c.send("")
	c.send("1")
	r.inc.now = r.inc.now.Add(11 * time.Minute)
	d := r.dial(t, "S2", "0772123456")
	if sc := d.send(""); !strings.Contains(sc.Text, "English") || d.incidentID() == c.incidentID() {
		t.Fatalf("redial after the window resumed: %q", sc.Text)
	}
}

func TestUSSDWithheldNumberNeverResumes(t *testing.T) {
	r := newRig(t, nil, nil)
	c := r.dial(t, "S1", "")
	c.send("")
	c.send("1")
	d := r.dial(t, "S2", "")
	d.send("")
	if d.incidentID() == c.incidentID() {
		t.Fatal("a withheld number resumed another incident")
	}
}

// Scenario 6: "Somewhere else" gives 999 or 112; the record is kept, flagged
// out_of_area and closed.
func TestOutOfAreaClosesAndFlags(t *testing.T) {
	r := newRig(t, nil, nil)
	c := r.dial(t, "S1", "0772123456")
	c.send("")
	for _, in := range []string{"1", "1", "1", "3", "1"} {
		c.send(in)
	}
	end := c.send("5") // 4 wards, then Somewhere else
	p := c.projection()
	if !end.End || !strings.Contains(end.Text, "999") || !strings.Contains(end.Text, "112") ||
		p.State != incident.StateClosed || !slices.Contains(p.Flags, "out_of_area") || p.T[2] != nil {
		t.Fatalf("screen %+v, projection %+v", end, p)
	}
}

func TestTriageRuleErrorIsRedAndEscalated(t *testing.T) {
	r := newRig(t, fixedTriage{Severity: "red", RuleID: "rule_error", LogicVersion: "x+sha256:000000000000", RuleError: true}, nil)
	c := r.dial(t, "S1", "0772123456")
	c.send("")
	for _, in := range []string{"1", "2", "1", "3", "2"} {
		c.send(in)
	}
	p := c.projection()
	if p.Severity != incident.SevRed || !p.Escalated || !slices.Contains(p.Flags, "triage_rule_error") {
		t.Fatalf("projection = %+v", p)
	}
}

// Hard constraints 5 and 8: a typed callback number is sealed, referenced by
// ID, and appears in no event payload, step key or stored screen.
func TestCallbackNumberNeverInPayloadOrSteps(t *testing.T) {
	r := newRig(t, nil, nil)
	c := r.dial(t, "S1", "0772123456")
	c.send("")
	for _, in := range []string{"1", "2", "1", "3", "2", "1", "1"} {
		c.send(in)
	}
	if done := c.send("0772 999 888"); !done.End {
		t.Fatalf("typed number not accepted: %+v", done)
	}
	var answer8 incident.ScreenAnswer
	for _, e := range r.inc.allEvents() {
		for _, leak := range []string{"772999888", "0772 999 888"} {
			if strings.Contains(string(e.Payload), leak) {
				t.Fatalf("event %s payload carries the number: %s", e.Type, e.Payload)
			}
		}
		if a, err := incident.ParseScreenAnswer(e.Payload); err == nil && a.Screen == 8 {
			answer8 = a
		}
	}
	if answer8.Value != incident.CallbackProvided || answer8.IdentifierID == nil || r.inc.idents[*answer8.IdentifierID] != "+256772999888" {
		t.Fatalf("screen 8 answer = %+v, sealed = %v", answer8, r.inc.idents)
	}
	for k, st := range r.steps.m {
		if strings.Contains(st.Screen.Text, "772999888") || strings.Contains(k, string(plainSum(c.req.Text))) {
			t.Fatal("a stored step carries the number or its plain hash")
		}
	}
}

// Screen 7 option 6: the typed text is kept verbatim (a '*' included),
// sealed, and fuzzy-matched within the ward.
func TestStarInFreeTextIsKept(t *testing.T) {
	r := newRig(t, nil, fakeMatcher{"mkt near taxi": {LandmarkID: "w01_market", Similarity: 0.8, Found: true}})
	c := r.dial(t, "S1", "0772123456")
	c.send("")
	for _, in := range []string{"1", "2", "1", "3", "2", "1"} {
		c.send(in)
	}
	if sc := c.send("6"); sc.End || !strings.Contains(sc.Text, "20") {
		t.Fatalf("free-text prompt = %+v", sc)
	}
	c.send("Mkt*near taxi")
	p := c.projection()
	g := repoGazetteer(t)
	l, _ := g.Landmark("w01_market")
	if p.Landmark != incident.LandmarkOther || p.LocationQuality != incident.QualityFuzzy || p.Zone == nil ||
		*p.Zone != (incident.Zone{Col: l.Cell.Col, Row: l.Cell.Row}) {
		t.Fatalf("projection = %+v", p)
	}
	found := false
	for _, v := range r.inc.idents {
		found = found || v == "Mkt*near taxi"
	}
	if !found {
		t.Fatalf("free text not sealed verbatim: %v", r.inc.idents)
	}
}

func TestLowConfidenceFreeTextUsesWardAndFlags(t *testing.T) {
	r := newRig(t, nil, fakeMatcher{"somewhere odd": {LandmarkID: "w01_market", Similarity: 0.2, Found: true}})
	c := r.dial(t, "S1", "0772123456")
	c.send("")
	for _, in := range []string{"1", "2", "1", "3", "2", "1", "6"} {
		c.send(in)
	}
	c.send("Somewhere odd")
	p := c.projection()
	w, _ := repoGazetteer(t).Ward("w01")
	if p.LocationQuality != incident.QualityWard || !slices.Contains(p.Flags, "needs_location_callback") ||
		*p.Zone != (incident.Zone{Col: w.Centre.Col, Row: w.Centre.Row}) || p.State != incident.StateDispatching {
		t.Fatalf("projection = %+v", p)
	}
}

func TestInvalidInputReprompts(t *testing.T) {
	r := newRig(t, nil, nil)
	c := r.dial(t, "S1", "0772123456")
	c.send("")
	c.send("1")
	n := len(r.inc.allEvents())
	sc := c.send("9")
	if sc.End || !strings.Contains(sc.Text, "Please choose") || len(r.inc.allEvents()) != n {
		t.Fatalf("invalid input: %+v, events %d → %d", sc, n, len(r.inc.allEvents()))
	}
	c.send("1")
	if p := c.projection(); p.Danger != "yes" {
		t.Fatalf("answer after a re-prompt: %+v", p)
	}
}

// A callback whose text does not extend the session's previous input is not
// trusted as an answer.
func TestTextNotExtendingPreviousStepIsRefused(t *testing.T) {
	r := newRig(t, nil, nil)
	c := r.dial(t, "S1", "0772123456")
	c.send("")
	c.send("1")
	c.send("2")
	c.req.Text = "2*1*1"
	if _, err := r.svc.HandleUSSD(context.Background(), c.req); err == nil {
		t.Fatal("a rewritten input history was accepted")
	}
}

func TestUCCRouteMapsToItsChannel(t *testing.T) {
	r := newRig(t, nil, nil)
	c := r.dial(t, "S1", "0772123456")
	c.req.Provider = "ucc"
	c.send("")
	if p := c.projection(); p.Channel != string(incident.ChannelUSSDUCC) {
		t.Fatalf("channel = %s", p.Channel)
	}
	c.req.Provider = "smoke"
	c.req.SessionID = "S9"
	if _, err := r.svc.HandleUSSD(context.Background(), c.req); err == nil {
		t.Fatal("unknown provider accepted")
	}
}

// Step messages are length-prefixed, so no choice of provider, session and
// text can collide with another.
func TestStepMessageIsUnambiguous(t *testing.T) {
	if string(StepMessage("at", "s1*2", "3")) == string(StepMessage("at", "s1", "2*3")) ||
		string(StepMessage("a", "t", "")) == string(StepMessage("at", "", "")) {
		t.Fatal("step messages collide")
	}
}

// racySteps misses the first lookup, as when a concurrent duplicate callback
// saves its step between this callback's Lookup and Latest.
type racySteps struct {
	*memSteps
	missed bool
}

func (r *racySteps) Lookup(ctx context.Context, p, s string, macs []crypto.MAC) (Step, bool, error) {
	if !r.missed {
		r.missed = true
		return Step{}, false, nil
	}
	return r.memSteps.Lookup(ctx, p, s, macs)
}

// Review focus 1: a duplicate callback whose twin stored the step after this
// one's lookup must replay that step, not be refused as a rewritten history.
func TestDuplicateCallbackSavedMidwayReplays(t *testing.T) {
	r := newRig(t, nil, nil)
	c := r.dial(t, "S1", "0772123456")
	first := c.send("")
	racy := &racySteps{memSteps: r.steps}
	svc, err := New(Deps{Incidents: r.inc, Reader: r.inc, Steps: racy, Keys: fakeKeys{}, Machine: newMachine(t),
		Triage: repoTriage(t), Gazetteer: repoGazetteer(t), Matcher: fakeMatcher{}, ResumeWindow: 10 * time.Minute, MinSimilarity: 0.45,
		Dispatcher: r.inc, Dedup: DedupRule{Window: 15 * time.Minute, MaxZoneDistance: 1}})
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.HandleUSSD(context.Background(), c.req)
	if err != nil || again != first {
		t.Fatalf("duplicate first callback: %+v, %v (want the stored screen)", again, err)
	}
	second := c.send("1")
	racy.missed = false
	dup, err := svc.HandleUSSD(context.Background(), c.req)
	if err != nil || dup != second {
		t.Fatalf("duplicate later callback: %+v, %v (want the stored screen)", dup, err)
	}
}

// Scenario 4 at service level: a second bystander's call about the same
// crash merges into the incident already dispatching at T2; the second
// caller still finishes Tool 1 on their own record; the survivor's T1 is the
// earliest call.
func TestSecondBystanderMergedAtT2(t *testing.T) {
	r := newRig(t, nil, nil)
	a := r.dial(t, "S1", "0772123456") // first to dial, slower to finish
	a.send("")
	for _, in := range []string{"1", "1", "1", "3", "2", "1"} {
		a.send(in)
	}
	r.inc.now = r.inc.now.Add(time.Minute)
	b := r.dial(t, "S2", "0772000111")
	b.send("")
	for _, in := range []string{"1", "1", "1", "3", "2", "1", "2", "1"} {
		b.send(in)
	}
	r.inc.now = r.inc.now.Add(time.Minute)
	a.send("1") // screen 7: a's incident reaches T2 and merges into b's
	done := a.send("1")
	pa, pb := a.projection(), b.projection()
	if pa.State != incident.StateMerged || pa.MergedInto == nil || *pa.MergedInto != pb.ID || pa.LastScreen != 8 || !done.End {
		t.Fatalf("member %+v (closing %+v)", pa, done)
	}
	if pb.State != incident.StateDispatching || !pb.T[1].Equal(pa.OwnT1) || len(pb.Absorbed) != 1 {
		t.Fatalf("survivor %+v, want T1 of the first call %v", pb, pa.OwnT1)
	}
}
