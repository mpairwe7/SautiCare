//go:build integration

package opstore_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
	"sautihealth.com/sauti/internal/responder"
)

// CodeRabbit PR #10 review (migration 0023): concurrency at the database
// write boundary. Each test holds a lock in its own transaction, lets the
// call under test reach it, and only then releases it.

// bounded is a test context with a deadline, so a lock that is never
// released fails the test instead of hanging it.
func bounded(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// waitForLockWaits polls until n backends of role wait on a lock; the
// superuser sees every session's wait event.
func waitForLockWaits(ctx context.Context, t *testing.T, c *pgtest.TestCluster, role string, n int) {
	t.Helper()
	super := c.Superuser(t, pg.StoreOperational)
	waiting := 0
	for waiting < n {
		select {
		case <-ctx.Done():
			t.Fatalf("%d of %d %s sessions waiting on a lock: %v", waiting, n, role, ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
		if err := super.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE usename = $1 AND wait_event_type = 'Lock'`, role).Scan(&waiting); err != nil && ctx.Err() == nil {
			t.Fatal(err)
		}
	}
}

// holdResponderRow locks a responder row in a transaction of its own (as a
// concurrent availability change or acceptance would) until release.
func holdResponderRow(ctx context.Context, t *testing.T, c *pgtest.TestCluster, id uuid.UUID) (release func() time.Time) {
	t.Helper()
	tx, err := c.Owner(t, pg.StoreOperational).Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(ctx, "SELECT 1 FROM op.responders WHERE id = $1 FOR UPDATE", id); err != nil {
		t.Fatal(err)
	}
	return func() time.Time {
		var at time.Time
		if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&at); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return at
	}
}

// 4557205856: one responder accepting alerts for two incidents at the same
// instant is assigned to exactly one. The incident locks differ, so the
// acceptances serialise on the responder row before the busy check.
func TestAcceptsSerialisePerResponder(t *testing.T) {
	r := newDispatchRig(t)
	ctx := bounded(t)
	other := dispatchedIncident(t, r.intake).IncidentID
	tr := r.responder(t, responder.RoleTransporter)
	alerts := []uuid.UUID{r.issue(t, r.incident, tr, responder.RoleTransporter).AlertID, r.issue(t, other, tr, responder.RoleTransporter).AlertID}

	release := holdResponderRow(ctx, t, r.c, tr)
	results := make([]dispatch.AcceptOutcome, 2)
	errs := make([]error, 2)
	done := make(chan struct{}, 2)
	for i := range alerts {
		go func() {
			defer func() { done <- struct{}{} }()
			results[i], errs[i] = r.intake.AcceptAlert(ctx, alerts[i], tr)
		}()
	}
	waitForLockWaits(ctx, t, r.c, "op_intake", 2)
	release()
	<-done
	<-done
	var got []dispatch.AcceptResult
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("accept %d: %v", i, errs[i])
		}
		got = append(got, results[i].Result)
	}
	slices.Sort(got)
	if !slices.Equal(got, []dispatch.AcceptResult{dispatch.Accepted, dispatch.AlreadyAssigned}) {
		t.Fatalf("results %v: want one accepted and one already assigned", got)
	}
	var n int
	if err := r.c.Owner(t, pg.StoreOperational).QueryRow(ctx,
		"SELECT count(*) FROM op.assignments WHERE responder_id = $1 AND released_at IS NULL", tr).Scan(&n); err != nil || n != 1 {
		t.Fatalf("open assignments of the responder: %d, %v", n, err)
	}
	r.noDrift(t)
}

// 4557205834: an availability change that waited for the responder's lock
// starts its period when it acquired the lock, not when it was called, so
// the history never shows availability during the wait.
func TestAvailabilityStartsWhenTheLockIsAcquired(t *testing.T) {
	r := newDispatchRig(t)
	ctx := bounded(t)
	vht := r.responder(t, responder.RoleVHT)
	release := holdResponderRow(ctx, t, r.c, vht)
	var p responder.Period
	var err error
	done := make(chan struct{})
	go func() {
		defer close(done)
		p, err = r.intake.SetAvailability(ctx, vht, true, "ussd")
	}()
	waitForLockWaits(ctx, t, r.c, "op_intake", 1)
	time.Sleep(200 * time.Millisecond) // a wait long enough to see
	released := release()
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if p.From.Before(released) || p.Until.Sub(p.From) != responder.AvailabilityTTL {
		t.Fatalf("period %v–%v starts before the lock was released at %v", p.From, p.Until, released)
	}
}

// 4557205847: a forward is never recorded while dispatch is suspended, even
// when the suspension commits while the forward waits — the check takes a
// share lock on the control row, which a suspension holds exclusively. The
// same holds for an alert (review I1).
func TestNoForwardOrAlertOnceASuspensionCommits(t *testing.T) {
	r := newDispatchRig(t)
	ctx := bounded(t)
	gv := gazetteerVersion(t, r.c)
	vht := r.responder(t, responder.RoleVHT)

	other := dispatchedIncident(t, r.intake).IncidentID
	console, err := pgx.Connect(ctx, r.c.DSN(pg.StoreOperational, "op_console"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = console.Close(context.Background()) })
	tx, err := console.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SELECT op.suspend_dispatch('coordinator_decision', $1)", uuid.NewV7()); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	go func() {
		_, err := r.dispatcher.RecordForward(ctx, r.incident, "hc_1", gv, dispatchLogic)
		errs <- err
	}()
	go func() {
		_, err := r.dispatcher.IssueAlert(ctx, dispatch.IssueParams{IncidentID: other, ResponderID: vht, Role: responder.RoleVHT,
			Ring: 1, Wave: 1, LogicVersion: dispatchLogic})
		errs <- err
	}()
	waitForLockWaits(ctx, t, r.c, "op_dispatch", 2)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-errs; !errors.Is(err, incident.ErrIllegalTransition) {
			t.Fatalf("a write that waited for the suspension: %v (want refused)", err)
		}
	}
	snap, err := r.dispatcher.DispatchSnapshot(ctx, r.incident)
	if err != nil || len(snap.Forwards) != 0 {
		t.Fatalf("forwards while suspended: %+v %v", snap.Forwards, err)
	}
	// Resumed, the same forward goes through.
	if _, err := r.console.ResumeDispatch(ctx, "reviewed", uuid.NewV7()); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.dispatcher.RecordForward(ctx, r.incident, "hc_1", gv, dispatchLogic); err != nil || !ok {
		t.Fatalf("forward after resumption: %v %v", ok, err)
	}
}

// 4557205859: the claimed batch comes back most urgent first (red before
// green, then the earliest due), whatever order the UPDATE returns rows in;
// the engine processes it in that order.
func TestClaimReturnsTheMostUrgentFirst(t *testing.T) {
	r := newDispatchRig(t) // r.incident: red, due first
	ctx := bounded(t)
	var want []uuid.UUID
	greens := []uuid.UUID{dispatchedAs(t, r.intake, incident.SevGreen).IncidentID, dispatchedAs(t, r.intake, incident.SevGreen).IncidentID}
	yellow := dispatchedAs(t, r.intake, incident.SevYellow).IncidentID
	red := dispatchedAs(t, r.intake, incident.SevRed).IncidentID
	want = append(want, r.incident, red, yellow, greens[0], greens[1])
	// Rewrite the heap so the least urgent rows come first physically, as
	// churn on dispatch_timers leaves them.
	owner := r.c.Owner(t, pg.StoreOperational)
	if _, err := owner.Exec(ctx, `UPDATE op.dispatch_timers t SET due_at = due_at
		FROM op.incidents i WHERE i.id = t.incident_id AND i.severity <> 'green'`); err != nil {
		t.Fatal(err)
	}
	due, err := r.dispatcher.ClaimDueDispatch(ctx, 50, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var got []uuid.UUID
	for _, d := range due {
		got = append(got, d.IncidentID)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("claimed %v, want %v", got, want)
	}
}

// 4557206046: the database refuses a responder whose stage is not in the
// gazetteer in force, or not in the responder's ward: dispatch could never
// place them, so they could switch on and never be alerted.
func TestRegistrationNeedsAStageOfTheGazetteerInForce(t *testing.T) {
	r := newDispatchRig(t) // loads the test gazetteer: w01_stage in w01, w02_stage in w02
	ctx := bounded(t)
	register := func(ward, stage string) error {
		phone, err := pii.ParsePhone("0772000777")
		if err != nil {
			t.Fatal(err)
		}
		_, err = responder.NewService(r.console, r.ids).Register(ctx, responder.RegisterCmd{Role: responder.RoleVHT,
			Name: pii.NewPersonName("Synthetic"), Phone: phone, Ward: ward, Stage: stage})
		return err
	}
	for _, tc := range []struct{ ward, stage string }{
		{"w01", "w01_stage_zz"}, // no such stage
		{"w01", "w02_stage"},    // a stage of another ward
		{"w09", "w01_stage"},    // no such ward
	} {
		if err := register(tc.ward, tc.stage); !errors.Is(err, incident.ErrIllegalTransition) {
			t.Errorf("ward %s stage %s: %v (want refused)", tc.ward, tc.stage, err)
		}
	}
	if err := register("w02", "w02_stage"); err != nil {
		t.Fatalf("a stage of the gazetteer in force: %v", err)
	}
}
