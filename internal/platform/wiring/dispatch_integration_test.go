//go:build integration

package wiring_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/platform/devkeys"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
	"sautihealth.com/sauti/internal/platform/wiring"
	"sautihealth.com/sauti/internal/responder"
)

// hooked is the dispatch store with a crash hook: afterIssue, if set, runs
// after an alert is recorded, as a worker killed at that point would.
type hooked struct {
	*opstore.Store
	afterIssue func() error
	// refuse, if set, makes the database refuse alerts to these responders
	// (a gate lapsing between the worker's read and its write).
	refuse map[uuid.UUID]bool
}

func (c *hooked) IssueAlert(ctx context.Context, p dispatch.IssueParams) (dispatch.Issued, error) {
	if c.refuse[p.ResponderID] {
		return dispatch.Issued{}, fmt.Errorf("%w: the responder is not eligible now", incident.ErrIllegalTransition)
	}
	is, err := c.Store.IssueAlert(ctx, p)
	if err == nil && c.afterIssue != nil {
		err = c.afterIssue()
	}
	return is, err
}

// recorder is the Deliverer: it records every delivery; fail, if set,
// decides the outcome of one.
type recorder struct {
	mu   sync.Mutex
	sent []dispatch.Delivery
	fail func(dispatch.Delivery) error
}

func (r *recorder) Deliver(_ context.Context, d dispatch.Delivery) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, d)
	if r.fail != nil {
		if err := r.fail(d); err != nil {
			return "", err
		}
	}
	return "sim-" + d.AttemptID.String()[:8], nil
}

func (r *recorder) count(alert uuid.UUID, purpose string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, d := range r.sent {
		if d.AlertID == alert && d.Purpose == purpose {
			n++
		}
	}
	return n
}

type flag struct{ v atomic.Bool }

func (f *flag) DispatchSuspended(context.Context) (bool, error) { return f.v.Load(), nil }

type engineRig struct {
	c          *pgtest.TestCluster
	cfg        config.Bundle
	ids        crypto.Identifiers
	intake     *opstore.Store
	console    *opstore.Store
	store      *hooked
	deliveries *recorder
	suspension *flag
	engine     *dispatch.Engine
	gaz        location.File
	phones     int
}

func newEngineRig(t *testing.T, mutate func(*config.Bundle)) *engineRig {
	t.Helper()
	c := pgtest.Cluster(t)
	loadRepoGazetteer(t, c)
	cfg, err := config.Load(os.DirFS("../../../config"))
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(&cfg)
	}
	dir := t.TempDir() + "/k"
	if err := devkeys.Generate(dir, rand.Reader); err != nil {
		t.Fatal(err)
	}
	keys, err := devkeys.Open(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	pool := func(role string) *opstore.Store {
		p, err := pg.Connect(context.Background(), c.DSN(pg.StoreOperational, role), "test", 4)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return opstore.New(p)
	}
	r := &engineRig{c: c, cfg: cfg, ids: crypto.Identifiers{Env: crypto.NewEnvelope(keys, rand.Reader)},
		intake: pool("op_intake"), console: pool("op_console"), deliveries: &recorder{}, suspension: &flag{}}
	r.store = &hooked{Store: pool("op_dispatch")}
	r.rebuild(t)
	return r
}

// rebuild makes a fresh engine over the same store: a worker restart.
func (r *engineRig) rebuild(t *testing.T) {
	t.Helper()
	e, err := wiring.BuildDispatch(context.Background(), wiring.DispatchOptions{Config: r.cfg, Store: r.store,
		Deliverer: r.deliveries, Suspension: r.suspension})
	if err != nil {
		t.Fatal(err)
	}
	r.engine, r.gaz = e.Engine, e.Gazetteer
}

// age moves every recorded time of the incident d into the past, as if d
// had passed: the planner compares the database clock with these times.
// Events and projection move together, so the projections still verify.
func (r *engineRig) age(t *testing.T, id uuid.UUID, d time.Duration) {
	t.Helper()
	owner := r.c.Owner(t, pg.StoreOperational)
	for _, q := range []string{
		`UPDATE op.incidents SET created_at = created_at - $2::interval, t1 = t1 - $2::interval, t2 = t2 - $2::interval, t3 = t3 - $2::interval, t4 = t4 - $2::interval,
		        t5 = t5 - $2::interval, t6 = t6 - $2::interval, t7 = t7 - $2::interval WHERE id = $1`,
		"UPDATE op.incident_events SET recorded_at = recorded_at - $2::interval WHERE incident_id = $1",
		"UPDATE op.event_keys SET recorded_at = recorded_at - $2::interval WHERE incident_id = $1",
		"UPDATE op.alerts SET sent_at = sent_at - $2::interval, responded_at = responded_at - $2::interval, closed_at = closed_at - $2::interval WHERE incident_id = $1",
		"UPDATE op.assignments SET accepted_at = accepted_at - $2::interval WHERE incident_id = $1",
		"UPDATE op.forwards SET sent_at = sent_at - $2::interval, acknowledged_at = acknowledged_at - $2::interval, timed_out_at = timed_out_at - $2::interval WHERE incident_id = $1",
		"UPDATE op.caller_notices SET created_at = created_at - $2::interval WHERE incident_id = $1",
	} {
		if _, err := owner.Exec(context.Background(), q, id, d); err != nil {
			t.Fatal(err)
		}
	}
}

// superuser connects to the operational database as the cluster's superuser
// (only it may terminate other roles' backends).
func (r *engineRig) superuser(t *testing.T) *pgx.Conn {
	t.Helper()
	u, err := url.Parse(pgtest.AdminURL(t))
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + r.c.Database(pg.StoreOperational)
	conn, err := pgx.Connect(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func (r *engineRig) responder(t *testing.T, role responder.Role) uuid.UUID {
	t.Helper()
	r.phones++
	phone, err := pii.ParsePhone(fmt.Sprintf("+2567720003%02d", r.phones))
	if err != nil {
		t.Fatal(err)
	}
	cert, ins := time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour)
	reg, err := responder.NewService(r.console, r.ids).Register(context.Background(), responder.RegisterCmd{Role: role,
		Name: pii.NewPersonName("Synthetic"), Phone: phone, Ward: "w01", Stage: "w01_stage_a", CertifiedAt: &cert, InsuredUntil: &ins, KitOK: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.intake.SetAvailability(context.Background(), reg.ID, true, "ussd"); err != nil {
		t.Fatal(err)
	}
	return reg.ID
}

// incident opens an incident of severity located at the stage's zone and
// records T2.
func (r *engineRig) incident(t *testing.T, sev incident.Severity) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	ref, _ := incident.NewReference(rand.Reader)
	o, err := r.intake.OpenIncident(ctx, incident.OpenParams{Reference: ref, Channel: incident.ChannelUSSDAT, SessionID: uuid.NewV7(),
		ProviderSessionID: "ATUid_" + uuid.NewV4().String(), NetworkCode: "64110", IdemKey: "ussd:at:start",
		Phone: crypto.Sealed{KeyID: "dev:k:2", Ciphertext: []byte{1}}, PhoneMAC: crypto.MAC{KeyID: "dev:k:2", Sum: make([]byte, 32)}})
	if err != nil {
		t.Fatal(err)
	}
	stage, _ := r.gaz.Stage("w01_stage_a")
	for _, e := range []incident.AppendParams{
		{Type: incident.EvClassified, Payload: []byte(`{"severity":"` + string(sev) + `"}`), LogicVersion: "tool1-test+sha256:000000000000"},
		{Type: incident.EvDispatchReady, Payload: fmt.Appendf(nil, `{"zone":{"col":%d,"row":%d},"location_quality":"landmark"}`, stage.Cell.Col, stage.Cell.Row)},
	} {
		e.IncidentID, e.ActorKind, e.IdemKey = o.IncidentID, incident.ActorSystem, "t:"+string(e.Type)
		if _, err := r.intake.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	return o.IncidentID
}

// process runs the engine once for the incident, at its current timer
// generation, as a claim would.
func (r *engineRig) process(t *testing.T, id uuid.UUID) error {
	t.Helper()
	var gen int64
	if err := r.c.Owner(t, pg.StoreOperational).QueryRow(context.Background(),
		"SELECT coalesce((SELECT gen FROM op.dispatch_timers WHERE incident_id = $1), 0)", id).Scan(&gen); err != nil {
		t.Fatal(err)
	}
	return r.engine.Process(context.Background(), dispatch.Due{IncidentID: id, Gen: gen})
}

func (r *engineRig) mustProcess(t *testing.T, id uuid.UUID) dispatch.Snapshot {
	t.Helper()
	if err := r.process(t, id); err != nil {
		t.Fatal(err)
	}
	snap, err := r.store.DispatchSnapshot(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func alertsOf(s dispatch.Snapshot, role responder.Role) []dispatch.Alert {
	return slices.DeleteFunc(slices.Clone(s.Alerts), func(a dispatch.Alert) bool { return a.Role != role })
}

func (r *engineRig) noDrift(t *testing.T) {
	t.Helper()
	ro, err := pg.Connect(context.Background(), r.c.DSN(pg.StoreOperational, "op_readonly"), "test", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, drift, err := opstore.VerifyProjections(context.Background(), opstore.New(ro)); err != nil || len(drift) != 0 {
		t.Fatalf("projection drift %v %v", drift, err)
	}
}

// Scenario 2 through the engine and the real store: the VHT cascade reaches
// depth 3, one at a time, 90 s apart; the transporter accepts at once; each
// alert is delivered exactly once; the caller is told help is on the way.
func TestEngineYellowCascadeDepthThree(t *testing.T) {
	r := newEngineRig(t, nil)
	ctx := context.Background()
	vhts := []uuid.UUID{r.responder(t, responder.RoleVHT), r.responder(t, responder.RoleVHT), r.responder(t, responder.RoleVHT)}
	tr := r.responder(t, responder.RoleTransporter)
	id := r.incident(t, incident.SevYellow)

	snap := r.mustProcess(t, id)
	if len(alertsOf(snap, responder.RoleVHT)) != 1 || len(alertsOf(snap, responder.RoleTransporter)) != 1 {
		t.Fatalf("at T2: %+v", snap.Alerts)
	}
	ta := alertsOf(snap, responder.RoleTransporter)[0]
	if res, err := r.intake.AcceptAlert(ctx, ta.ID, tr); err != nil || res.Result != dispatch.Accepted {
		t.Fatalf("transporter accepts: %v %v", res, err)
	}
	for depth := 2; depth <= 3; depth++ {
		r.age(t, id, 90*time.Second)
		snap = r.mustProcess(t, id)
		if got := alertsOf(snap, responder.RoleVHT); len(got) != depth || got[depth-2].Outcome != dispatch.OutcomeNoResponse {
			t.Fatalf("depth %d: %+v", depth, got)
		}
	}
	last := alertsOf(snap, responder.RoleVHT)[2]
	if !slices.Contains(vhts, last.ResponderID) {
		t.Fatal("alerted someone outside the pool")
	}
	if res, err := r.intake.AcceptAlert(ctx, last.ID, last.ResponderID); err != nil || res.Result != dispatch.Accepted {
		t.Fatalf("third VHT accepts: %v %v", res, err)
	}
	snap = r.mustProcess(t, id)
	if snap.State != incident.StateAssigned || len(snap.Assigned) != 2 || !slices.Contains(snap.CallerNotified, dispatch.CallerHelperOnTheWay) {
		t.Fatalf("after both accept: %+v", snap)
	}
	for _, a := range snap.Alerts {
		if n := r.deliveries.count(a.ID, dispatch.PurposeAlert); n != 1 {
			t.Fatalf("alert %v delivered %d times", a.ID, n)
		}
	}
	if len(snap.Escalations) != 0 {
		t.Fatalf("escalated: %v", snap.Escalations)
	}
	r.noDrift(t)
}

// Review focus 1: the worker crashes after recording an alert and before
// delivering it — a restarted worker delivers it once and alerts nobody
// twice. A crash after claiming the attempt never sends it again.
func TestEngineCrashBetweenRecordAndDelivery(t *testing.T) {
	r := newEngineRig(t, nil)
	r.responder(t, responder.RoleVHT)
	id := r.incident(t, incident.SevGreen)
	crash := errors.New("worker killed")
	r.store.afterIssue = func() error { return crash }
	if err := r.process(t, id); !errors.Is(err, crash) {
		t.Fatalf("crash: %v", err)
	}
	r.store.afterIssue = nil
	r.rebuild(t)
	snap := r.mustProcess(t, id)
	vht := alertsOf(snap, responder.RoleVHT)
	if len(vht) != 1 || r.deliveries.count(vht[0].ID, dispatch.PurposeAlert) != 1 {
		t.Fatalf("after restart: alerts %+v, deliveries %d", vht, r.deliveries.count(vht[0].ID, dispatch.PurposeAlert))
	}
	r.mustProcess(t, id)
	if n := r.deliveries.count(vht[0].ID, dispatch.PurposeAlert); n != 1 {
		t.Fatalf("delivered %d times after a second pass", n)
	}
}

// Scenario 14: the primary fails over mid-cascade (every op_dispatch
// connection is killed while a delivery is in flight). The attempt's result
// is unknown, so it is never re-sent; the cascade goes on to the next rung
// after the timeout; nobody is alerted twice and nothing is lost.
func TestEngineFailoverMidCascade(t *testing.T) {
	r := newEngineRig(t, nil)
	r.responder(t, responder.RoleVHT)
	r.responder(t, responder.RoleVHT)
	id := r.incident(t, incident.SevYellow)
	super := r.superuser(t)
	killed := false
	r.deliveries.fail = func(dispatch.Delivery) error {
		if killed {
			return nil
		}
		killed = true
		if _, err := super.Exec(context.Background(),
			"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = 'op_dispatch' AND pid <> pg_backend_pid()"); err != nil {
			t.Error(err)
		}
		return errors.New("connection to the provider lost with the database")
	}
	for range 3 { // the pool reconnects; a failed pass is retried, as the lease would
		if err := r.process(t, id); err == nil {
			break
		}
	}
	snap := r.mustProcess(t, id)
	first := alertsOf(snap, responder.RoleVHT)
	if len(first) != 1 || r.deliveries.count(first[0].ID, dispatch.PurposeAlert) != 1 {
		t.Fatalf("after failover: alerts %+v", first)
	}
	r.age(t, id, 90*time.Second)
	snap = r.mustProcess(t, id)
	vht := alertsOf(snap, responder.RoleVHT)
	if len(vht) != 2 || vht[0].Outcome != dispatch.OutcomeNoResponse {
		t.Fatalf("next rung after the timeout: %+v", vht)
	}
	for _, a := range vht {
		if n := r.deliveries.count(a.ID, dispatch.PurposeAlert); n != 1 {
			t.Fatalf("alert %v delivered %d times", a.ID, n)
		}
	}
	r.noDrift(t)
}

// Review focus 5: suspension switched on mid-cascade — no new alert, the
// incident is escalated once; resumption continues the cascade.
func TestEngineSuspendedMidCascade(t *testing.T) {
	r := newEngineRig(t, nil)
	r.responder(t, responder.RoleVHT)
	r.responder(t, responder.RoleVHT)
	r.responder(t, responder.RoleTransporter)
	id := r.incident(t, incident.SevYellow)
	r.mustProcess(t, id)
	r.suspension.v.Store(true)
	r.age(t, id, 90*time.Second)
	r.mustProcess(t, id)
	snap := r.mustProcess(t, id) // a second pass while suspended escalates nothing more
	if len(alertsOf(snap, responder.RoleVHT)) != 1 || !slices.Equal(snap.Escalations, []string{incident.EscalationKey(dispatch.ReasonSuspended, "")}) {
		t.Fatalf("suspended: alerts %+v escalations %v", snap.Alerts, snap.Escalations)
	}
	r.suspension.v.Store(false)
	snap = r.mustProcess(t, id)
	if len(alertsOf(snap, responder.RoleVHT)) != 2 {
		t.Fatalf("resumed: %+v", snap.Alerts)
	}
}

// Review focus 3: nobody eligible at all — the coordinator is alerted at
// once, and the wake keeps looking for someone switching on.
func TestEngineNoEligibleEscalatesAtOnce(t *testing.T) {
	r := newEngineRig(t, nil)
	id := r.incident(t, incident.SevRed)
	snap := r.mustProcess(t, id)
	if !dispatch.HasEscalation(snap.Escalations, dispatch.ReasonExhausted) {
		t.Fatalf("escalations %v", snap.Escalations)
	}
	var due time.Time
	if err := r.c.Owner(t, pg.StoreOperational).QueryRow(context.Background(),
		"SELECT due_at FROM op.dispatch_timers WHERE incident_id = $1", id).Scan(&due); err != nil || due.After(time.Now().Add(2*time.Minute)) {
		t.Fatalf("next wake %v, %v: want within a ring interval", due, err)
	}
	r.responder(t, responder.RoleVHT)
	r.age(t, id, time.Minute)
	if snap = r.mustProcess(t, id); len(alertsOf(snap, responder.RoleVHT)) != 1 {
		t.Fatalf("a responder switching on was not alerted: %+v", snap.Alerts)
	}
}

// A claimed batch is processed most urgent first through RunOnce, and an
// incident the worker cannot process is escalated, never dropped.
func TestEngineRunOnceEscalatesAnUnprocessableIncident(t *testing.T) {
	r := newEngineRig(t, nil)
	id := r.incident(t, incident.SevRed)
	broken := errors.New("broken store")
	r.store.afterIssue = func() error { return broken }
	r.responder(t, responder.RoleVHT)
	var failed []uuid.UUID
	n, err := r.engine.RunOnce(context.Background(), 10, time.Minute, func(i uuid.UUID, err error) { failed = append(failed, i) })
	if err != nil || n != 1 || !slices.Contains(failed, id) {
		t.Fatalf("RunOnce: %d %v failed=%v", n, err, failed)
	}
	r.store.afterIssue = nil
	snap, _ := r.store.DispatchSnapshot(context.Background(), id)
	if !dispatch.HasEscalation(snap.Escalations, dispatch.ReasonDispatchError) {
		t.Fatalf("escalations %v", snap.Escalations)
	}
}

// Review focus 4: a responder who lapses between the worker's read and its
// write is refused by the database; the engine leaves them out and alerts
// the next, never stalling.
func TestEngineMovesOnWhenTheDatabaseRefusesAResponder(t *testing.T) {
	r := newEngineRig(t, nil)
	a, b := r.responder(t, responder.RoleVHT), r.responder(t, responder.RoleVHT)
	r.store.refuse = map[uuid.UUID]bool{a: true}
	id := r.incident(t, incident.SevGreen)
	snap := r.mustProcess(t, id)
	vht := alertsOf(snap, responder.RoleVHT)
	if len(vht) != 1 || vht[0].ResponderID != b {
		t.Fatalf("alerts %+v, want the second responder", vht)
	}
}

// Each attempt settles as what the provider said: sent, failed (rejected:
// nothing went out) or unknown (no answer: never re-sent).
func TestEngineSettlesAttempts(t *testing.T) {
	r := newEngineRig(t, nil)
	sent, rejected, unknown := r.responder(t, responder.RoleVHT), r.responder(t, responder.RoleVHT), r.responder(t, responder.RoleVHT)
	r.deliveries.fail = func(d dispatch.Delivery) error {
		switch d.ResponderID {
		case rejected:
			return dispatch.ErrRejected
		case unknown:
			return errors.New("provider timed out")
		}
		return nil
	}
	id := r.incident(t, incident.SevRed)
	r.mustProcess(t, id)
	r.mustProcess(t, id)
	want := map[uuid.UUID]string{sent: "sent", rejected: "failed", unknown: "unknown"}
	rows, err := r.c.Owner(t, pg.StoreOperational).Query(context.Background(),
		`SELECT a.responder_id, x.status, count(*) OVER (PARTITION BY x.alert_id) FROM op.alert_attempts x
		   JOIN op.alerts a ON a.id = x.alert_id WHERE a.incident_id = $1 AND a.role = 'vht'`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var who uuid.UUID
		var status string
		var attempts int
		if err := rows.Scan(&who, &status, &attempts); err != nil {
			t.Fatal(err)
		}
		if want[who] != status || attempts != 1 {
			t.Errorf("responder %v: %s ×%d, want %s ×1", who, status, attempts, want[who])
		}
		n++
	}
	if n != 3 {
		t.Fatalf("%d attempts, want 3", n)
	}
}

// Review I1: a suspension committed between recording an alert and
// delivering it stops the delivery (the alert lapses), even though the
// worker read "active" at the start of its pass.
func TestEngineDoesNotDeliverAfterADatabaseSuspension(t *testing.T) {
	r := newEngineRig(t, nil)
	r.responder(t, responder.RoleVHT)
	id := r.incident(t, incident.SevGreen)
	r.store.afterIssue = func() error {
		r.store.afterIssue = nil
		_, err := r.console.SuspendDispatch(context.Background(), "coordinator_decision", uuid.NewV7())
		return err
	}
	snap := r.mustProcess(t, id)
	vht := alertsOf(snap, responder.RoleVHT)
	if len(vht) != 1 || r.deliveries.count(vht[0].ID, dispatch.PurposeAlert) != 0 {
		t.Fatalf("alerts %+v delivered %d times during the suspension", vht, r.deliveries.count(vht[0].ID, dispatch.PurposeAlert))
	}
}

// Review I7: an incident closed with a responder stood down still gets the
// stand-down delivered (the closure wakes dispatch), once; then dispatch
// parks it.
func TestEngineDeliversStandDownsAfterClosure(t *testing.T) {
	r := newEngineRig(t, nil)
	r.responder(t, responder.RoleVHT)
	id := r.incident(t, incident.SevGreen)
	vht := alertsOf(r.mustProcess(t, id), responder.RoleVHT)
	if len(vht) != 1 {
		t.Fatalf("alerts %+v", vht)
	}
	if _, err := r.store.CloseAlert(context.Background(), vht[0].ID, dispatch.OutcomeStoodDown); err != nil {
		t.Fatal(err)
	}
	if _, err := r.intake.Append(context.Background(), incident.AppendParams{IncidentID: id, Type: incident.EvClosed,
		Payload: []byte(`{"reason":"completed"}`), ActorKind: incident.ActorSystem, IdemKey: "close"}); err != nil {
		t.Fatal(err)
	}
	r.mustProcess(t, id)
	r.mustProcess(t, id)
	if n := r.deliveries.count(vht[0].ID, dispatch.PurposeStandDown); n != 1 {
		t.Fatalf("stand-down delivered %d times", n)
	}
	var parked bool
	if err := r.c.Owner(t, pg.StoreOperational).QueryRow(context.Background(),
		"SELECT due_at = 'infinity' FROM op.dispatch_timers WHERE incident_id = $1", id).Scan(&parked); err != nil || !parked {
		t.Fatalf("not parked after closure: %v %v", parked, err)
	}
}
