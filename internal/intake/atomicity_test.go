package intake

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
)

// errInjected stands in for a write that did not complete in time (the
// 800 ms USSD budget ending mid-request).
var errInjected = errors.New("injected: request budget exceeded")

// faultyIncidents fails one write, once: the failAt-th write call (counting
// StartSession, ResumeSession, StoreIdentifier, Append, AppendBatch and
// DispatchOrMerge), or the first write that carries an event of type
// failType. With after set,
// the write takes effect and only its reply is lost (the budget ended during
// the commit); otherwise nothing of it is stored.
type faultyIncidents struct {
	*memIncidents
	mu       sync.Mutex
	calls    int
	failAt   int
	failType incident.EventType
	after    bool
}

func (f *faultyIncidents) trip(types ...incident.EventType) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failAt > 0 && f.calls == f.failAt {
		f.failAt = 0
		return true
	}
	if f.failType != "" && slices.Contains(types, f.failType) {
		f.failType = ""
		return true
	}
	return false
}

func faulty[T any](f *faultyIncidents, write func() (T, error), types ...incident.EventType) (T, error) {
	if !f.trip(types...) {
		return write()
	}
	var zero T
	if f.after {
		if _, err := write(); err != nil {
			return zero, err
		}
	}
	return zero, errInjected
}

func (f *faultyIncidents) StartSession(ctx context.Context, c incident.StartSession) (incident.Opened, error) {
	return faulty(f, func() (incident.Opened, error) { return f.memIncidents.StartSession(ctx, c) })
}

func (f *faultyIncidents) ResumeSession(ctx context.Context, c incident.ResumeSession) (incident.Opened, error) {
	return faulty(f, func() (incident.Opened, error) { return f.memIncidents.ResumeSession(ctx, c) })
}

func (f *faultyIncidents) StoreIdentifier(ctx context.Context, inc uuid.UUID, kind string, plaintext []byte, idem string) (uuid.UUID, error) {
	return faulty(f, func() (uuid.UUID, error) { return f.memIncidents.StoreIdentifier(ctx, inc, kind, plaintext, idem) })
}

func (f *faultyIncidents) Append(ctx context.Context, a incident.AppendParams) (incident.Appended, error) {
	return faulty(f, func() (incident.Appended, error) { return f.memIncidents.Append(ctx, a) }, a.Type)
}

func (f *faultyIncidents) AppendBatch(ctx context.Context, ps []incident.AppendParams) ([]incident.Appended, error) {
	types := make([]incident.EventType, len(ps))
	for i, a := range ps {
		types[i] = a.Type
	}
	return faulty(f, func() ([]incident.Appended, error) { return f.memIncidents.AppendBatch(ctx, ps) }, types...)
}

func (f *faultyIncidents) DispatchOrMerge(ctx context.Context, r DispatchRequest) (DispatchOutcome, error) {
	return faulty(f, func() (DispatchOutcome, error) { return f.memIncidents.DispatchOrMerge(ctx, r) }, incident.EvDispatchReady)
}

func newRigWith(t *testing.T, inc Incidents, mem *memIncidents, cls Classifier, match fakeMatcher, trigger int) *rig {
	t.Helper()
	if cls == nil {
		cls = repoTriage(t)
	}
	m, err := NewMachine(repoBundle(t).Tool1, repoGazetteer(t), trigger)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := inc.(Dispatcher)
	if !ok {
		d = mem
	}
	r := &rig{inc: mem, steps: &memSteps{}, suspension: &suspensionFlag{}}
	svc, err := New(Deps{Incidents: inc, Reader: mem, Steps: r.steps, Keys: fakeKeys{}, Machine: m,
		Triage: cls, Gazetteer: repoGazetteer(t), Matcher: match, ResumeWindow: 10 * time.Minute, MinSimilarity: 0.45,
		Dispatcher: d, Dedup: DedupRule{Window: 15 * time.Minute, MaxZoneDistance: 1}, Suspension: r.suspension})
	if err != nil {
		t.Fatal(err)
	}
	r.svc = svc
	return r
}

// try delivers the next callback like send, but returns the error.
func (c *caller) try(input string) (Screen, error) {
	switch {
	case !c.started:
		c.started = true
	case c.req.Text == "":
		c.req.Text = input
	default:
		c.req.Text += "*" + input
	}
	return c.r.svc.HandleUSSD(context.Background(), c.req)
}

// walk drives one caller through Tool 1 by answering whatever screen is
// shown (answers, by prompt kind). A failed callback is recovered as the
// network or the caller would: redial false retries the same callback (the
// provider resending it), redial true dials a new session from the same
// number (the caller after the error screen). Every screen is checked: the
// caller is never told help is coming while the incident has no T2.
func walk(t *testing.T, r *rig, phone string, answers map[Kind]string, redial bool) *caller {
	t.Helper()
	ctx := context.Background()
	c := r.dial(t, "S1", phone)
	sessions := 1
	check := func(c *caller, sc Screen) {
		t.Helper()
		if strings.Contains(sc.Text, "HELP IS COMING") {
			if p := c.projection(); p.T[2] == nil {
				t.Fatalf("told help is coming, but the incident has no T2 (state %s)", p.State)
			}
		}
	}
	for range 40 {
		input := ""
		if st, ok, _ := r.steps.Latest(ctx, c.req.Provider, c.req.SessionID); ok {
			if st.Screen.End {
				return c
			}
			input = answers[st.Kind]
		}
		sc, err := c.try(input)
		if err == nil {
			check(c, sc)
			continue
		}
		if !errors.Is(err, errInjected) {
			t.Fatalf("callback %q: %v", c.req.Text, err)
		}
		if !redial {
			if sc, err = r.svc.HandleUSSD(ctx, c.req); err != nil {
				t.Fatalf("retried callback %q: %v", c.req.Text, err)
			}
			check(c, sc)
			continue
		}
		sessions++
		c = r.dial(t, "S"+strconv.Itoa(sessions), phone)
	}
	t.Fatal("Tool 1 did not finish in 40 callbacks")
	return nil
}

var (
	walkListed = map[Kind]string{KindLanguage: "1", KindDanger: "2", KindCategory: "1", KindAgeBand: "3",
		KindSpecificDanger: "2", KindWard: "1", KindLandmark: "1", KindCallback: "1"}
	walkFreeText = map[Kind]string{KindLanguage: "1", KindDanger: "2", KindCategory: "1", KindAgeBand: "3",
		KindSpecificDanger: "2", KindWard: "1", KindLandmark: "6", KindLandmarkText: "mkt near taxi", KindCallback: "1"}
	ruleError = fixedTriage{Severity: "red", RuleID: "rule_error", LogicVersion: "x+sha256:000000000000", RuleError: true}
)

// finished checks the end state of a walked incident: classified, T2
// recorded, and a rule error's flag and escalation present.
func finished(t *testing.T, c *caller, cls Classifier) {
	t.Helper()
	p := c.projection()
	if p.Severity == "" || p.T[2] == nil || p.LastScreen != 8 ||
		(p.State != incident.StateDispatching && p.State != incident.StateMerged) {
		t.Fatalf("finished incident: severity %q, T2 %v, state %s, last screen %d", p.Severity, p.T[2], p.State, p.LastScreen)
	}
	// A redial after a lost reply to the last answer starts a second
	// incident, which deduplication merges into the first (dispatching).
	if p.MergedInto != nil {
		if s, err := c.r.inc.Projection(context.Background(), *p.MergedInto); err != nil || s.State != incident.StateDispatching {
			t.Fatalf("merged into %s in state %s (%v)", p.MergedInto, s.State, err)
		}
	}
	if _, ok := cls.(fixedTriage); ok && (!p.Escalated || !slices.Contains(p.Flags, FlagTriageRuleError)) {
		t.Fatalf("rule error without its flag or escalation: %+v", p)
	}
	// No other incident of the walk (one a redial could not resume) is left
	// with Tool 1 finished but no T2; and however many sessions retried,
	// each consequence was recorded once.
	for id, evs := range c.r.inc.events {
		if q, err := incident.Fold(evs); err != nil || (q.LastScreen == 8 && q.T[2] == nil) {
			t.Fatalf("incident %s finished Tool 1 without T2 (%v)", id, err)
		}
		// An escalation counts once per reason: a survivor escalated itself
		// also takes the merged call's unacknowledged escalation (F-06).
		n, escalations := map[incident.EventType]int{}, map[string]int{}
		for _, e := range evs {
			n[e.Type]++
			if e.Type == incident.EvEscalated {
				reason, _, _ := incident.ParseEscalated(e.Payload)
				escalations[reason]++
			}
		}
		if n[incident.EvClassified] > 1 || n[incident.EvDispatchReady] > 1 || slices.ContainsFunc(slices.Collect(maps.Values(escalations)), func(c int) bool { return c > 1 }) {
			t.Fatalf("incident %s recorded a consequence twice: %v", id, n)
		}
	}
}

// An incident whose screen 5 answer was stored without its classification
// (before answers and their consequences were one batch) is classified the
// moment its caller redials, before any further question; a rule error
// brings its flag and escalation with it.
func TestResumeClassifiesAnIncidentThatLacksIt(t *testing.T) {
	r := newRig(t, ruleError, nil)
	c := r.dial(t, "S1", "0772123456")
	c.send("")
	id := c.incidentID()
	r.inc.mu.Lock()
	for i, a := range []string{
		`{"screen":1,"field":"language","value":"en"}`, `{"screen":2,"field":"danger","value":"yes"}`,
		`{"screen":3,"field":"category","value":"trauma"}`, `{"screen":4,"field":"age_band","value":"age_18_59"}`,
		`{"screen":5,"field":"specific_danger","value":"yes"}`,
	} {
		if _, err := r.inc.appendLocked(id, incident.EvScreenAnswered, []byte(a), "legacy:"+strconv.Itoa(i), ""); err != nil {
			t.Fatal(err)
		}
	}
	r.inc.mu.Unlock()
	d := r.dial(t, "S2", "0772123456")
	if sc := d.send(""); !strings.Contains(sc.Text, "Test Ward 1") {
		t.Fatalf("redial screen = %q", sc.Text)
	}
	if p := d.projection(); p.Severity != incident.SevRed || p.State != incident.StateTriaged || !p.Escalated ||
		!slices.Contains(p.Flags, FlagTriageRuleError) {
		t.Fatalf("after the redial: %+v", p)
	}
}

// A typed landmark is sealed and cannot be read back: when T2 is recorded on
// a redial, the incident is located at ward level and flagged for a
// location call-back (fail safe), never left undispatched.
func TestRedialAfterLostT2WithFreeTextUsesTheWard(t *testing.T) {
	mem := newMemIncidents()
	match := fakeMatcher{"mkt near taxi": {LandmarkID: "w01_market", Similarity: 0.8, Found: true}}
	r := newRigWith(t, &faultyIncidents{memIncidents: mem, failType: incident.EvDispatchReady}, mem, nil, match, 7)
	c := walk(t, r, "0772123456", walkFreeText, true)
	finished(t, c, nil)
	w, _ := repoGazetteer(t).Ward("w01")
	if p := c.projection(); p.LocationQuality != incident.QualityWard || !slices.Contains(p.Flags, FlagNeedsLocationCallback) ||
		*p.Zone != (incident.Zone{Col: w.Centre.Col, Row: w.Centre.Row}) {
		t.Fatalf("projection = %+v", p)
	}
}

// CodeRabbit PR #6: with dispatch_trigger_screen 8, the typed landmark is
// matched at screen 7, where it is typed, and the match (a landmark ID,
// never the text) rides in the USSD step to screen 8, through an invalid
// answer there, so T2 locates the incident at the matched landmark as
// trigger 7 does.
func TestTypedLandmarkLocatesAtTriggerScreen8(t *testing.T) {
	match := fakeMatcher{"mkt near taxi": {LandmarkID: "w01_market", Similarity: 0.8, Found: true}}
	mem := newMemIncidents()
	r := newRigWith(t, mem, mem, nil, match, 8)
	c := r.dial(t, "S1", "0772123456")
	c.send("")
	for _, in := range []string{"1", "2", "1", "3", "2", "1", "6", "Mkt near taxi"} {
		c.send(in)
	}
	if p := c.projection(); p.T[2] != nil {
		t.Fatalf("T2 before the trigger screen: %+v", p)
	}
	c.send("12")
	if sc := c.send("1"); !strings.Contains(sc.Text, "HELP IS COMING") {
		t.Fatalf("closing screen = %q", sc.Text)
	}
	l, _ := repoGazetteer(t).Landmark("w01_market")
	if p := c.projection(); p.LocationQuality != incident.QualityFuzzy || slices.Contains(p.Flags, FlagNeedsLocationCallback) ||
		p.Zone == nil || *p.Zone != (incident.Zone{Col: l.Cell.Col, Row: l.Cell.Row}) {
		t.Fatalf("projection = %+v", p)
	}
}

// The closing screen is refused, not shown, for a finished Tool 1 without
// T2 in the area.
func TestCompleteWithoutT2IsAnError(t *testing.T) {
	r := newRig(t, nil, nil)
	p := incident.Projection{Reference: "1234567894", State: incident.StateTriaged, Version: 9, Severity: incident.SevRed,
		Language: "en", Danger: "yes", Category: "trauma", AgeBand: "age_18_59", SpecificDanger: "yes", Ward: "w01",
		Landmark: "w01_market", LastScreen: 8}
	if _, err := r.svc.next(context.Background(), p, true); !errors.Is(err, ErrNotDispatched) {
		t.Fatalf("next without T2: %v", err)
	}
	now := time.Now()
	p.T[2] = &now
	if pr, err := r.svc.next(context.Background(), p, true); err != nil || pr.Kind != KindComplete {
		t.Fatalf("next with T2: %+v %v", pr, err)
	}
}

// C1(a), review reproduction: screen 7's answer is stored but T2 is not;
// the caller redials and finishes. They must never hear that help is coming
// while the incident has no T2: the redial records T2 first.
func TestRedialAfterLostT2Dispatches(t *testing.T) {
	mem := newMemIncidents()
	r := newRigWith(t, &faultyIncidents{memIncidents: mem, failType: incident.EvDispatchReady}, mem, nil, nil, 7)
	finished(t, walk(t, r, "0772123456", walkListed, true), nil)
}

// C1(b), review reproduction: screen 5's answer is stored but the
// classification is not; the redial resumes after screen 5, so the
// classification must still be recorded and the incident dispatched.
func TestRedialAfterLostClassificationClassifies(t *testing.T) {
	mem := newMemIncidents()
	r := newRigWith(t, &faultyIncidents{memIncidents: mem, failType: incident.EvClassified, after: false}, mem, nil, nil, 7)
	finished(t, walk(t, r, "0772123456", walkListed, true), nil)
}

// C1(c), review reproduction: a rule error classifies red, and the flag and
// escalation that make it a human's job must not be lost to a failure
// between the writes: a retry of the same callback records them.
func TestRuleErrorFlagAndEscalationSurviveAFailure(t *testing.T) {
	mem := newMemIncidents()
	r := newRigWith(t, &faultyIncidents{memIncidents: mem, failType: incident.EvFlagged}, mem, ruleError, nil, 7)
	finished(t, walk(t, r, "0772123456", walkListed, false), ruleError)
}

// Every write of a Tool 1 walk, failed once (before or after it takes
// effect), then recovered by a retried callback or a redial: the incident
// always ends classified and dispatched, a rule error always flagged and
// escalated, and no screen says help is coming without T2. Run for both
// trigger screens, a listed and a typed landmark, and a rule error.
func TestEveryWriteFailureConverges(t *testing.T) {
	match := fakeMatcher{"mkt near taxi": {LandmarkID: "w01_market", Similarity: 0.8, Found: true}}
	type variant struct {
		name    string
		trigger int
		answers map[Kind]string
		cls     Classifier
	}
	var variants []variant
	for _, trigger := range []int{7, 8} {
		variants = append(variants,
			variant{"listed", trigger, walkListed, nil},
			variant{"free_text", trigger, walkFreeText, nil},
			variant{"rule_error", trigger, walkListed, ruleError})
	}
	for _, v := range variants {
		cls := v.cls
		if cls == nil {
			cls = repoTriage(t)
		}
		clean := &faultyIncidents{memIncidents: newMemIncidents()}
		walk(t, newRigWith(t, clean, clean.memIncidents, cls, match, v.trigger), "0772123456", v.answers, false)
		writes := clean.calls
		if writes < 10 {
			t.Fatalf("%s: a clean walk made only %d writes", v.name, writes)
		}
		for n := 1; n <= writes; n++ {
			for _, after := range []bool{false, true} {
				for _, redial := range []bool{false, true} {
					t.Run(fmt.Sprintf("trigger%d/%s/write%d/after=%v/redial=%v", v.trigger, v.name, n, after, redial), func(t *testing.T) {
						f := &faultyIncidents{memIncidents: newMemIncidents(), failAt: n, after: after}
						r := newRigWith(t, f, f.memIncidents, cls, match, v.trigger)
						finished(t, walk(t, r, "0772123456", v.answers, redial), v.cls)
					})
				}
			}
		}
	}
}
