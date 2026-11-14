//go:build integration

package opstore_test

import (
	"context"
	"encoding/json"
	"maps"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

var dedupRule = intake.DedupRule{Window: 15 * time.Minute, MaxZoneDistance: 1}

// triaged opens an incident with a category and a classification, ready for T2.
func triaged(t *testing.T, s *opstore.Store, category string) incident.Opened {
	t.Helper()
	o, err := s.OpenIncident(context.Background(), openParams(t))
	if err != nil {
		t.Fatal(err)
	}
	appendEv(t, s, o.IncidentID, incident.EvScreenAnswered, `{"screen":3,"field":"category","value":"`+category+`"}`, "")
	appendEv(t, s, o.IncidentID, incident.EvClassified, `{"severity":"red"}`, "tool1-test+sha256:000000000000")
	return o
}

func dispatchOrMerge(s *opstore.Store, o incident.Opened, category string, col int) (intake.DispatchOutcome, error) {
	payload, _ := json.Marshal(map[string]any{"zone": map[string]int{"col": col, "row": 84}, "location_quality": "landmark"})
	return s.DispatchOrMerge(context.Background(), intake.DispatchRequest{IncidentID: o.IncidentID, Payload: payload,
		IdemKey: "dr:" + o.IncidentID.String(), Category: category, Severity: incident.SevRed, LocationQuality: incident.QualityLandmark,
		Zone: incident.Zone{Col: col, Row: 84}, T1: o.T1, Rule: dedupRule})
}

// classifiedAs opens an incident with a category and the given severity,
// escalated if asked, ready for T2.
func classifiedAs(t *testing.T, s *opstore.Store, category string, sev incident.Severity, escalated bool) incident.Opened {
	t.Helper()
	o, err := s.OpenIncident(context.Background(), openParams(t))
	if err != nil {
		t.Fatal(err)
	}
	appendEv(t, s, o.IncidentID, incident.EvScreenAnswered, `{"screen":3,"field":"category","value":"`+category+`"}`, "")
	appendEv(t, s, o.IncidentID, incident.EvClassified, `{"severity":"`+string(sev)+`"}`, "tool1-test+sha256:000000000000")
	if escalated {
		appendEv(t, s, o.IncidentID, incident.EvEscalated, `{"reason":"triage_rule_error"}`, "")
	}
	return o
}

func dispatchAs(s *opstore.Store, o incident.Opened, category string, sev incident.Severity, escalated bool, quality string, col int) (intake.DispatchOutcome, error) {
	payload, _ := json.Marshal(map[string]any{"zone": map[string]int{"col": col, "row": 84}, "location_quality": quality})
	return s.DispatchOrMerge(context.Background(), intake.DispatchRequest{IncidentID: o.IncidentID, Payload: payload,
		IdemKey: "dr:" + o.IncidentID.String(), Category: category, Severity: sev, Escalated: escalated, LocationQuality: quality,
		Zone: incident.Zone{Col: col, Row: 84}, T1: o.T1, Rule: dedupRule})
}

// C2: a merged call more urgent than its survivor raises the survivor's
// severity (incident.severity_raised, from the merged call) and, if the
// call was escalated, escalates it (merged_call_escalated), in the merge's
// transaction. A less urgent call changes nothing; a retry adds nothing; the
// projections match the events.
func TestMergeCarriesSeverityAndEscalation(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	survivor := classifiedAs(t, s, "trauma", incident.SevGreen, false)
	if out, err := dispatchAs(s, survivor, "trauma", incident.SevGreen, false, incident.QualityLandmark, 902); err != nil || out.Merged {
		t.Fatalf("survivor dispatch: %+v %v", out, err)
	}
	yellow := classifiedAs(t, s, "trauma", incident.SevYellow, false)
	if out, err := dispatchAs(s, yellow, "trauma", incident.SevYellow, false, incident.QualityLandmark, 902); err != nil || !out.Merged {
		t.Fatalf("yellow merge: %+v %v", out, err)
	}
	if p, _ := s.Projection(ctx, survivor.IncidentID); p.Severity != incident.SevYellow || p.Escalated {
		t.Fatalf("after the yellow call: %+v", p)
	}
	red := classifiedAs(t, s, "trauma", incident.SevRed, true)
	appendEv(t, s, red.IncidentID, incident.EvEscalated, `{"reason":"no_eligible_responder","role":"vht"}`, "")
	for range 2 {
		if out, err := dispatchAs(s, red, "trauma", incident.SevRed, true, incident.QualityFuzzy, 903); err != nil || !out.Merged ||
			out.Survivor != survivor.IncidentID {
			t.Fatalf("red merge: %+v %v", out, err)
		}
	}
	green := classifiedAs(t, s, "trauma", incident.SevGreen, false)
	if out, err := dispatchAs(s, green, "trauma", incident.SevGreen, false, incident.QualityLandmark, 902); err != nil || !out.Merged {
		t.Fatalf("green merge: %+v %v", out, err)
	}
	p, _ := s.Projection(ctx, survivor.IncidentID)
	if p.Severity != incident.SevRed || !p.Escalated || p.State != incident.StateDispatching || len(p.Absorbed) != 3 {
		t.Fatalf("survivor: %+v", p)
	}
	evs, _ := s.Events(ctx, survivor.IncidentID)
	var raised []string
	pairs := map[intake.EscalationPair]int{} // each pair the red call held, as it was recorded (PR #14 review)
	for _, e := range evs {
		switch e.Type {
		case incident.EvSeverityRaised:
			raised = append(raised, string(e.Payload))
		case incident.EvEscalated:
			var pl map[string]string
			if err := json.Unmarshal(e.Payload, &pl); err != nil {
				t.Fatalf("escalation payload %s: %v", e.Payload, err)
			}
			// The exact payload, no extra field: a reason alone, or a reason and its role.
			ruleError := maps.Equal(pl, map[string]string{"reason": "triage_rule_error"})
			noVHT := maps.Equal(pl, map[string]string{"reason": "no_eligible_responder", "role": "vht"})
			if !ruleError && !noVHT {
				t.Errorf("escalation payload %s", e.Payload)
			}
			pairs[intake.EscalationPair{Reason: pl["reason"], Role: pl["role"]}]++
		}
	}
	want := map[intake.EscalationPair]int{{Reason: "triage_rule_error"}: 1, {Reason: "no_eligible_responder", Role: "vht"}: 1}
	if len(raised) != 2 || !maps.Equal(pairs, want) {
		t.Fatalf("severity_raised %v, escalation pairs %v, want each of %v once", raised, pairs, want)
	}
	if _, drift, err := opstore.VerifyProjections(ctx, storeAs(t, c, "op_readonly")); err != nil || len(drift) != 0 {
		t.Fatalf("drift %v %v", drift, err)
	}
}

// I1 at store level: two calls located only to their ward, same category,
// same zone, minutes apart, are both dispatched; neither is merged.
func TestWardLevelCallsAreNotMerged(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	a, b := classifiedAs(t, s, "trauma", incident.SevRed, false), classifiedAs(t, s, "trauma", incident.SevRed, false)
	for _, o := range []incident.Opened{a, b} {
		if out, err := dispatchAs(s, o, "trauma", incident.SevRed, false, incident.QualityWard, 902); err != nil || out.Merged {
			t.Fatalf("ward-level dispatch: %+v %v", out, err)
		}
	}
	listed := classifiedAs(t, s, "trauma", incident.SevRed, false)
	if out, err := dispatchAs(s, listed, "trauma", incident.SevRed, false, incident.QualityLandmark, 902); err != nil || out.Merged {
		t.Fatalf("a located call merged into a ward-level one: %+v %v", out, err)
	}
}

// Scenario 4 at store level: the later-dispatched call merges into the
// incident already dispatching, and the survivor takes the earlier T1.
func TestDispatchOrMergeMergesIntoEarliestDispatched(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	early := triaged(t, s, "trauma") // first call
	survivor := triaged(t, s, "trauma")
	other := triaged(t, s, "obstetric")
	if out, err := dispatchOrMerge(s, survivor, "trauma", 902); err != nil || out.Merged {
		t.Fatalf("first dispatch: %+v %v", out, err)
	}
	if out, err := dispatchOrMerge(s, other, "obstetric", 902); err != nil || out.Merged {
		t.Fatalf("other category merged: %+v %v", out, err)
	}
	out, err := dispatchOrMerge(s, early, "trauma", 903)
	if err != nil || !out.Merged || out.Survivor != survivor.IncidentID {
		t.Fatalf("second trauma call: %+v %v", out, err)
	}
	sp, _ := s.Projection(ctx, survivor.IncidentID)
	ep, _ := s.Projection(ctx, early.IncidentID)
	if !sp.T[1].Equal(early.T1) || len(sp.Absorbed) != 1 || ep.State != incident.StateMerged || ep.T[2] == nil ||
		ep.MergedInto == nil || *ep.MergedInto != survivor.IncidentID {
		t.Fatalf("survivor %+v\nmember %+v", sp, ep)
	}
	again, err := dispatchOrMerge(s, early, "trauma", 903)
	if err != nil || !again.Merged || again.Survivor != survivor.IncidentID {
		t.Fatalf("retried dispatch: %+v %v", again, err)
	}
	if _, drift, err := opstore.VerifyProjections(ctx, storeAs(t, c, "op_readonly")); err != nil || len(drift) != 0 {
		t.Fatalf("drift %v %v", drift, err)
	}
}

// Review focus 5: two bystanders completing screen 7 at the same moment give
// exactly one survivor; neither call is lost.
func TestConcurrentCompletionsOneSurvivor(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	for round := range 5 {
		a, b := triaged(t, s, "trauma"), triaged(t, s, "trauma")
		var wg sync.WaitGroup
		outs := make([]intake.DispatchOutcome, 2)
		errs := make([]error, 2)
		for i, o := range []incident.Opened{a, b} {
			wg.Go(func() { outs[i], errs[i] = dispatchOrMerge(s, o, "trauma", 950+round*10) })
		}
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("round %d: %v %v", round, errs[0], errs[1])
		}
		if outs[0].Merged == outs[1].Merged {
			t.Fatalf("round %d: merged = %v and %v, want exactly one survivor", round, outs[0].Merged, outs[1].Merged)
		}
	}
}

// A coordinator splits a wrong merge, atomically and logged with the staff
// identity: the member is revived to dispatching and the survivor's T1 is
// recomputed. Only the console role may do it; a retry changes nothing.
func TestSplitIncident(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	early, survivor := triaged(t, s, "trauma"), triaged(t, s, "trauma")
	if _, err := dispatchOrMerge(s, survivor, "trauma", 902); err != nil {
		t.Fatal(err)
	}
	if out, err := dispatchOrMerge(s, early, "trauma", 902); err != nil || !out.Merged {
		t.Fatalf("merge: %+v %v", out, err)
	}
	staff := uuid.NewV7()
	if err := s.SplitIncident(ctx, survivor.IncidentID, early.IncidentID, staff); err == nil {
		t.Fatal("op_intake split a merge")
	}
	console := storeAs(t, c, "op_console")
	for range 2 {
		if err := console.SplitIncident(ctx, survivor.IncidentID, early.IncidentID, staff); err != nil {
			t.Fatal(err)
		}
	}
	sp, _ := s.Projection(ctx, survivor.IncidentID)
	ep, _ := s.Projection(ctx, early.IncidentID)
	if !sp.T[1].Equal(survivor.T1) || len(sp.Absorbed) != 0 || ep.State != incident.StateDispatching || ep.MergedInto != nil {
		t.Fatalf("survivor %+v\nmember %+v", sp, ep)
	}
	evs, _ := s.Events(ctx, survivor.IncidentID)
	last := evs[len(evs)-1]
	if last.Type != incident.EvSplit || last.ActorKind != incident.ActorStaff || last.StaffID == nil || *last.StaffID != staff {
		t.Fatalf("split event = %+v", last)
	}
	if err := console.SplitIncident(ctx, survivor.IncidentID, triaged(t, s, "trauma").IncidentID, staff); err == nil {
		t.Fatal("split of a call that was never merged succeeded")
	}
}

// Every dispatch decision reads the candidates under the dedup lock, so the
// read must not scan every retained incident: the planner can answer the
// body of op.dedup_candidates from the partial index of incidents already
// dispatching (incidents_dedup).
func TestDedupCandidatesUseAnIndex(t *testing.T) {
	c := pgtest.Cluster(t)
	conn := c.Owner(t, pg.StoreOperational)
	ctx := context.Background()
	var body string
	if err := conn.QueryRow(ctx, `SELECT prosrc FROM pg_proc
		WHERE oid = 'op.dedup_candidates(text, int, int, int, timestamptz, int, uuid)'::regprocedure`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	args := map[string]string{"p_category": "'trauma'", "p_col": "902", "p_row": "84", "p_dist": "1",
		"p_t1": "now()", "p_window_seconds": "900", "p_id": "'" + uuid.NewV7().String() + "'::uuid"}
	q := regexp.MustCompile(`\bp_\w+\b`).ReplaceAllStringFunc(body, func(m string) string {
		v, ok := args[m]
		if !ok {
			t.Fatalf("no literal for %s", m)
		}
		return v
	})
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, "EXPLAIN "+q)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if text := strings.Join(plan, "\n"); !strings.Contains(text, "incidents_dedup") || !strings.Contains(text, "LockRows") {
		t.Fatalf("op.dedup_candidates does not lock rows read from incidents_dedup:\n%s", text)
	}
}

type dispatchResult struct {
	out intake.DispatchOutcome
	err error
}

// dispatchBlocked runs DispatchOrMerge for o while another transaction
// holds a lock it needs, then runs release (which commits that
// transaction) once the call is waiting on the lock, and returns the result.
func dispatchBlocked(t *testing.T, c *pgtest.TestCluster, s *opstore.Store, o incident.Opened, rule intake.DedupRule, release func()) dispatchResult {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"zone": map[string]int{"col": 902, "row": 84}, "location_quality": incident.QualityLandmark})
	done := make(chan dispatchResult, 1)
	go func() {
		out, err := s.DispatchOrMerge(context.Background(), intake.DispatchRequest{IncidentID: o.IncidentID, Payload: payload,
			IdemKey: "dr:" + o.IncidentID.String(), Category: "trauma", Severity: incident.SevRed, LocationQuality: incident.QualityLandmark,
			Zone: incident.Zone{Col: 902, Row: 84}, T1: o.T1, Rule: rule})
		done <- dispatchResult{out, err}
	}()
	su := c.Superuser(t, pg.StoreOperational)
	deadline := time.Now().Add(30 * time.Second)
	for {
		var waiting int
		if err := su.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case r := <-done:
			t.Fatalf("the dispatch finished without waiting for the concurrent transaction: %+v %v", r.out, r.err)
		case <-time.After(10 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("the dispatch never waited for the concurrent transaction")
		}
	}
	release()
	select {
	case r := <-done:
		return r
	case <-time.After(30 * time.Second):
		t.Fatal("the dispatch did not finish after the concurrent transaction committed")
	}
	return dispatchResult{}
}

// beginAs opens a transaction as role on the operational store.
func beginAs(t *testing.T, c *pgtest.TestCluster, role string) pgx.Tx {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), c.DSN(pg.StoreOperational, role))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	tx, err := conn.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

// The survivor closes between the candidate read and the merge. The dispatch
// must not fail (rolling T2 back and leaving the call undispatched): the
// candidates are read FOR SHARE, so the read waits for the closure and then
// sees the survivor closed, and the call is dispatched on its own.
func TestDispatchOrMergeWhileTheSurvivorCloses(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	survivor, call := triaged(t, s, "trauma"), triaged(t, s, "trauma")
	if out, err := dispatchOrMerge(s, survivor, "trauma", 902); err != nil || out.Merged {
		t.Fatalf("survivor dispatch: %+v %v", out, err)
	}
	closing := beginAs(t, c, "op_intake")
	if _, err := closing.Exec(ctx, `SELECT * FROM op.append_incident_event($1::uuid, 0, 'incident.closed', '{"reason":"resolved"}',
		'system', NULL, 'close-race', NULL)`, survivor.IncidentID.String()); err != nil {
		t.Fatal(err)
	}
	r := dispatchBlocked(t, c, s, call, dedupRule, func() {
		if err := closing.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	})
	if r.err != nil || r.out.Merged {
		t.Fatalf("dispatch while the survivor closed: %+v %v", r.out, r.err)
	}
	if p, err := s.Projection(ctx, call.IncidentID); err != nil || p.State != incident.StateDispatching || p.T[2] == nil || p.MergedInto != nil {
		t.Fatalf("the call after the dispatch: %+v %v", p, err)
	}
}

// A coordinator splits a call off the survivor between the candidate read
// and the merge, moving the survivor's T1 later, outside the window. The new
// call must not merge on the T1 the survivor had before the split: the
// candidates are read FOR SHARE, so the read waits for the split and the
// rule sees the survivor's current T1.
func TestDispatchOrMergeWhileASplitMovesT1(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	wrong, call := triaged(t, s, "trauma"), triaged(t, s, "trauma") // wrongly merged; the new call moments later
	time.Sleep(400 * time.Millisecond)
	survivor := triaged(t, s, "trauma")
	rule := intake.DedupRule{Window: survivor.T1.Sub(call.T1) / 2, MaxZoneDistance: 1}
	if gap := call.T1.Sub(wrong.T1); gap >= rule.Window {
		t.Fatalf("setup: the first calls are %v apart, window %v", gap, rule.Window)
	}
	payload := func(col int) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"zone": map[string]int{"col": col, "row": 84}, "location_quality": incident.QualityLandmark})
		return b
	}
	for _, d := range []struct {
		o   incident.Opened
		col int
	}{{survivor, 902}, {wrong, 300}} { // the wrong call is elsewhere: the merge was a mistake
		if out, err := s.DispatchOrMerge(ctx, intake.DispatchRequest{IncidentID: d.o.IncidentID, Payload: payload(d.col),
			IdemKey: "dr:" + d.o.IncidentID.String(), Category: "trauma", Severity: incident.SevRed, LocationQuality: incident.QualityLandmark,
			Zone: incident.Zone{Col: d.col, Row: 84}, T1: d.o.T1, Rule: rule}); err != nil || out.Merged {
			t.Fatalf("dispatch: %+v %v", out, err)
		}
	}
	absorb(t, s, survivor, wrong) // the survivor's T1 is now the wrong call's
	splitting := beginAs(t, c, "op_console")
	if _, err := splitting.Exec(ctx, "SELECT op.split_incident($1::uuid, $2::uuid, $3::uuid)",
		survivor.IncidentID.String(), wrong.IncidentID.String(), uuid.NewV7().String()); err != nil {
		t.Fatal(err)
	}
	r := dispatchBlocked(t, c, s, call, rule, func() {
		if err := splitting.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	})
	if r.err != nil || r.out.Merged {
		t.Fatalf("the call merged on the survivor's T1 from before the split: %+v %v", r.out, r.err)
	}
	if _, drift, err := opstore.VerifyProjections(ctx, storeAs(t, c, "op_readonly")); err != nil || len(drift) != 0 {
		t.Fatalf("drift %v %v", drift, err)
	}
}
