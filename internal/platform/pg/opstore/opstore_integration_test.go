//go:build integration

package opstore_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

func storeAs(t *testing.T, c *pgtest.TestCluster, role string) *opstore.Store {
	t.Helper()
	pool, err := pg.Connect(context.Background(), c.DSN(pg.StoreOperational, role), "test", 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return opstore.New(pool)
}

func openParams(t *testing.T) incident.OpenParams {
	t.Helper()
	ref, err := incident.NewReference(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return incident.OpenParams{
		Reference:         ref,
		Channel:           incident.ChannelUSSDAT,
		SessionID:         uuid.NewV7(),
		ProviderSessionID: "ATUid_" + uuid.NewV4().String(),
		NetworkCode:       "64110",
		IdemKey:           "ussd:at:start",
		Phone:             crypto.Sealed{KeyID: "dev:k:2", Ciphertext: []byte{1, 2, 3}},
		PhoneMAC:          crypto.MAC{KeyID: "dev:k:2", Sum: make([]byte, 32)},
	}
}

func appendEv(t *testing.T, s *opstore.Store, id uuid.UUID, typ incident.EventType, payload, logic string) incident.Appended {
	t.Helper()
	if payload == "" {
		payload = "{}"
	}
	a, err := s.Append(context.Background(), incident.AppendParams{
		IncidentID: id, Type: typ, Payload: json.RawMessage(payload), ActorKind: incident.ActorSystem,
		IdemKey: string(typ) + ":" + payload, LogicVersion: logic,
	})
	if err != nil {
		t.Fatalf("Append(%s): %v", typ, err)
	}
	return a
}

func dbNow(t *testing.T, c *pgtest.TestCluster) time.Time {
	t.Helper()
	var now time.Time
	if err := c.Owner(t, pg.StoreOperational).QueryRow(context.Background(), "SELECT clock_timestamp()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	return now
}

// T1 is set by the database clock, never by the caller (hard constraint 4).
func TestOpenIncidentSetsT1FromDatabase(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	before := dbNow(t, c)
	o, err := s.OpenIncident(context.Background(), openParams(t))
	if err != nil {
		t.Fatal(err)
	}
	after := dbNow(t, c)
	if !o.Created || o.T1.Before(before) || o.T1.After(after) {
		t.Fatalf("opened = %+v, want T1 within [%v, %v]", o, before, after)
	}
	evs, err := s.Events(context.Background(), o.IncidentID)
	if err != nil || len(evs) != 1 || evs[0].Type != incident.EvSessionStarted || !evs[0].RecordedAt.Equal(o.T1) {
		t.Fatalf("events = %+v, %v", evs, err)
	}
	p, err := s.Projection(context.Background(), o.IncidentID)
	if err != nil || p.State != incident.StateIntake || p.Version != 1 || p.Reference != o.Reference {
		t.Fatalf("projection = %+v, %v", p, err)
	}
}

// A retried first callback must find the same incident, not create another.
func TestOpenIncidentReplayReturnsExisting(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	p := openParams(t)
	first, err := s.OpenIncident(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	p.SessionID = uuid.NewV7()
	p.Reference, _ = incident.NewReference(rand.Reader)
	again, err := s.OpenIncident(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if again.Created || again.IncidentID != first.IncidentID || again.Reference != first.Reference || again.EventID != first.EventID {
		t.Fatalf("replay = %+v, first = %+v", again, first)
	}
}

func TestConcurrentOpenOfSameSessionCreatesOneIncident(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	base := openParams(t)
	var wg sync.WaitGroup
	ids := make([]uuid.UUID, 10)
	errs := make([]error, 10)
	for i := range 10 {
		p := base
		p.SessionID = uuid.NewV7()
		p.Reference, _ = incident.NewReference(rand.Reader)
		wg.Go(func() {
			o, err := s.OpenIncident(context.Background(), p)
			ids[i], errs[i] = o.IncidentID, err
		})
	}
	wg.Wait()
	for i := range 10 {
		if errs[i] != nil {
			t.Fatalf("open %d: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("concurrent opens produced different incidents: %v", ids)
		}
	}
}

func TestAppendIsIdempotentOnKey(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	o, _ := s.OpenIncident(context.Background(), openParams(t))
	a := appendEv(t, s, o.IncidentID, incident.EvScreenAnswered, `{"screen":2,"field":"danger","value":"yes"}`, "")
	b := appendEv(t, s, o.IncidentID, incident.EvScreenAnswered, `{"screen":2,"field":"danger","value":"yes"}`, "")
	if a.Duplicate || !b.Duplicate || a.Event.ID != b.Event.ID || a.Event.Seq != b.Event.Seq {
		t.Fatalf("first = %+v, second = %+v", a, b)
	}
	p, _ := s.Projection(context.Background(), o.IncidentID)
	if p.Version != 2 {
		t.Fatalf("version = %d after duplicate, want 2", p.Version)
	}
}

func appendErr(s *opstore.Store, id uuid.UUID, typ incident.EventType, payload string, expected int64) error {
	_, err := s.Append(context.Background(), incident.AppendParams{
		IncidentID: id, ExpectedVersion: expected, Type: typ, Payload: json.RawMessage(payload),
		ActorKind: incident.ActorSystem, IdemKey: uuid.NewV4().String(),
	})
	return err
}

func TestAppendRejectsVersionConflict(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	o, _ := s.OpenIncident(context.Background(), openParams(t))
	answer := `{"screen":2,"field":"danger","value":"no"}`
	if err := appendErr(s, o.IncidentID, incident.EvScreenAnswered, answer, 5); !errors.Is(err, incident.ErrVersionConflict) {
		t.Fatalf("err = %v, want ErrVersionConflict", err)
	}
	if err := appendErr(s, o.IncidentID, incident.EvScreenAnswered, answer, 1); err != nil {
		t.Fatalf("matching expected version rejected: %v", err)
	}
}

func TestAppendRejectsIllegalTransition(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	o, _ := s.OpenIncident(context.Background(), openParams(t))
	if err := appendErr(s, o.IncidentID, incident.EvDeparted, `{}`, 0); !errors.Is(err, incident.ErrIllegalTransition) {
		t.Fatalf("departed in intake: %v", err)
	}
	if err := appendErr(s, o.IncidentID, incident.EvSessionStarted, `{}`, 0); !errors.Is(err, incident.ErrIllegalTransition) {
		t.Fatalf("second session.started: %v", err)
	}
}

func TestAppendToUnknownIncident(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	if err := appendErr(s, uuid.NewV7(), incident.EvScreenAnswered, `{}`, 0); !errors.Is(err, incident.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// The SQL write path enforces the severity ratchet itself. No lifecycle edge
// allows re-classification yet, so the test adds one as the owner.
func TestAppendRejectsSeverityLowering(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	o, _ := s.OpenIncident(context.Background(), openParams(t))
	appendEv(t, s, o.IncidentID, incident.EvClassified, `{"severity":"red"}`, "tr-test")
	if _, err := c.Owner(t, pg.StoreOperational).Exec(context.Background(),
		"INSERT INTO op.incident_transitions VALUES ('triaged', 'incident.classified', NULL)"); err != nil {
		t.Fatal(err)
	}
	if err := appendErr(s, o.IncidentID, incident.EvClassified, `{"severity":"green"}`, 0); !errors.Is(err, incident.ErrSeverityLowered) {
		t.Fatalf("err = %v, want ErrSeverityLowered", err)
	}
}

func TestReferenceCollisionMapsToErrReferenceTaken(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	p := openParams(t)
	if _, err := s.OpenIncident(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	q := openParams(t)
	q.Reference = p.Reference
	if _, err := s.OpenIncident(context.Background(), q); !errors.Is(err, incident.ErrReferenceTaken) {
		t.Fatalf("err = %v, want ErrReferenceTaken", err)
	}
}

func TestTransitionsTableMatchesGo(t *testing.T) {
	c := pgtest.Cluster(t)
	rows, err := c.Owner(t, pg.StoreOperational).Query(context.Background(),
		"SELECT from_state, event_type, coalesce(to_state, '') FROM op.incident_transitions")
	if err != nil {
		t.Fatal(err)
	}
	var db []string
	for rows.Next() {
		var f, e, to string
		if err := rows.Scan(&f, &e, &to); err != nil {
			t.Fatal(err)
		}
		db = append(db, f+"|"+e+"|"+to)
	}
	var goSide []string
	for _, tr := range incident.Transitions() {
		goSide = append(goSide, string(tr.From)+"|"+string(tr.Event)+"|"+string(tr.To))
	}
	sort.Strings(db)
	sort.Strings(goSide)
	if !slices.Equal(db, goSide) {
		t.Fatalf("op.incident_transitions != incident.Transitions()\nDB: %v\nGo: %v", db, goSide)
	}
}

func pgCode(err error) string {
	var e *pgconn.PgError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// Hard constraint: application roles cannot UPDATE or DELETE event, audit or
// consent rows, and cannot bypass the write functions.
func TestRuntimeRolesCannotWriteDirectly(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, c.DSN(pg.StoreOperational, "op_intake"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, q := range []string{
		"UPDATE op.incident_events SET type = 'x'",
		"DELETE FROM op.incident_events",
		"UPDATE op.incidents SET state = 'closed'",
		"INSERT INTO op.incidents (reference, channel, state, version) VALUES ('1234567894', 'ivr', 'intake', 1)",
		"INSERT INTO op.outbox (topic, payload) VALUES ('audit', '{}')",
		"DELETE FROM op.event_keys",
		"SELECT op.claim_outbox('audit', 10)",
	} {
		if _, err := conn.Exec(ctx, q); pgCode(err) != "42501" {
			t.Errorf("%q as op_intake: err = %v, want 42501", q, err)
		}
	}
}

func TestRelayRoleSeesOnlyOutboxFunctions(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, c.DSN(pg.StoreOperational, "op_relay"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, q := range []string{
		"SELECT * FROM op.incidents",
		"SELECT * FROM op.sessions",
		"SELECT * FROM op.outbox",
		"SELECT op.append_incident_event(gen_random_uuid(), 0, 'screen.answered', '{}', 'system', NULL, 'k', NULL)",
	} {
		if _, err := conn.Exec(ctx, q); pgCode(err) != "42501" {
			t.Errorf("%q as op_relay: err = %v, want 42501", q, err)
		}
	}
}

// The database projection and the pure Go reducer must agree exactly.
func TestProjectionMatchesFold(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	o, _ := s.OpenIncident(ctx, openParams(t))
	id := o.IncidentID
	appendEv(t, s, id, incident.EvScreenAnswered, `{"screen":2,"field":"danger","value":"yes"}`, "")
	appendEv(t, s, id, incident.EvFlagged, `{"flag":"test_call"}`, "")
	appendEv(t, s, id, incident.EvClassified, `{"severity":"red","category":"trauma"}`, "tr-test+sha256:00")
	appendEv(t, s, id, incident.EvDispatchReady, "", "")
	appendEv(t, s, id, incident.EvAlertIssued, `{"role":"vht"}`, "")
	appendEv(t, s, id, incident.EvAlertIssued, `{"role":"transporter"}`, "")
	appendEv(t, s, id, incident.EvAccepted, `{"role":"vht"}`, "")
	appendEv(t, s, id, incident.EvEscalated, `{"reason":"test"}`, "")
	appendEv(t, s, id, incident.EvArrived, "", "")
	appendEv(t, s, id, incident.EvDeparted, "", "")
	appendEv(t, s, id, incident.EvAtFacility, "", "")
	appendEv(t, s, id, incident.EvClosed, `{"reason":"completed"}`, "")

	evs, err := s.Events(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want, err := incident.Fold(evs)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	got, err := s.Projection(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if d := incident.Diff(got, want); d != "" {
		t.Fatalf("DB projection differs from Fold: %s", d)
	}
	if got.State != incident.StateClosed || got.Version != 13 || got.Severity != incident.SevRed || !got.Escalated {
		t.Fatalf("projection = %+v", got)
	}
}

// Dispatch first: a missing monthly partition must never fail a write. The
// row lands in the default partition and the monitoring function reports it.
func TestEventLandsInDefaultPartitionAndIsReported(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := context.Background()
	owner := c.Owner(t, pg.StoreOperational)
	var part string
	if err := owner.QueryRow(ctx, `SELECT 'op.incident_events_y' || to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY') ||
		'm' || to_char(clock_timestamp() AT TIME ZONE 'UTC', 'MM')`).Scan(&part); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, "DROP TABLE "+part); err != nil {
		t.Fatalf("drop %s: %v", part, err)
	}
	s := storeAs(t, c, "op_intake")
	if _, err := s.OpenIncident(ctx, openParams(t)); err != nil {
		t.Fatalf("open with missing partition: %v", err)
	}
	n, err := storeAs(t, c, "op_readonly").DefaultPartitionRows(ctx)
	if err != nil || n != 1 {
		t.Fatalf("DefaultPartitionRows = %d, %v; want 1", n, err)
	}
}

var allowedOutboxKeys = []string{"entry_id", "event_id", "event_type", "incident_id", "logic_version", "occurred_at", "payload_sha256", "staff_id"}

// The audit outbox row carries the allowlisted keys only, and never payload
// content (hard constraint 8).
func TestOutboxPayloadHasOnlyAllowedKeys(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	o, _ := s.OpenIncident(ctx, openParams(t))
	appendEv(t, s, o.IncidentID, incident.EvFlagged, `{"flag":"note","note":"0772123456"}`, "")
	rows, err := c.Owner(t, pg.StoreOperational).Query(ctx, "SELECT id, payload FROM op.outbox ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		var id uuid.UUID
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "772123456") {
			t.Fatalf("outbox payload leaked content: %s", raw)
		}
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		var keys []string
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if !slices.Equal(keys, allowedOutboxKeys) {
			t.Fatalf("outbox keys = %v, want %v", keys, allowedOutboxKeys)
		}
		if m["entry_id"] != id.String() {
			t.Fatalf("entry_id %v != outbox id %v", m["entry_id"], id)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("outbox rows = %d, want 2", n)
	}
}

func TestSecurityDefinerFunctionsPinSearchPath(t *testing.T) {
	c := pgtest.Cluster(t)
	rows, err := c.Owner(t, pg.StoreOperational).Query(context.Background(), `
		SELECT p.proname, coalesce(array_to_string(p.proconfig, ','), '')
		FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = 'op' AND p.prosecdef`)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for rows.Next() {
		var name, cfg string
		if err := rows.Scan(&name, &cfg); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(cfg, "search_path=op, pg_temp") {
			t.Errorf("SECURITY DEFINER op.%s does not pin search_path (config %q)", name, cfg)
		}
		count++
	}
	if count < 5 {
		t.Fatalf("found %d SECURITY DEFINER functions, want at least 5", count)
	}
}

func TestClaimOutboxSkipsLockedAndCompletes(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := context.Background()
	s := storeAs(t, c, "op_intake")
	for range 3 {
		if _, err := s.OpenIncident(ctx, openParams(t)); err != nil {
			t.Fatal(err)
		}
	}
	relay := storeAs(t, c, "op_relay")
	b1, err := relay.Claim(ctx, "audit", 2)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := relay.Claim(ctx, "audit", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(b1.Rows()) != 2 || len(b2.Rows()) != 1 {
		t.Fatalf("claims got %d and %d rows, want 2 and 1 (SKIP LOCKED)", len(b1.Rows()), len(b2.Rows()))
	}
	var done []uuid.UUID
	for _, r := range b1.Rows() {
		done = append(done, r.ID)
	}
	if err := b1.Complete(ctx, done); err != nil {
		t.Fatal(err)
	}
	b2.Abort(ctx)
	b3, err := relay.Claim(ctx, "audit", 10)
	if err != nil {
		t.Fatal(err)
	}
	defer b3.Abort(ctx)
	if len(b3.Rows()) != 1 {
		t.Fatalf("after completing 2 and aborting 1, pending = %d, want 1", len(b3.Rows()))
	}
}

// Review finding I3: an unsalted SHA-256 of a payload holding a phone number
// is reversible by enumerating ~10^9 candidates, and the five-year ledger
// cannot be shredded. Each event carries a random salt kept in the
// (shreddable) operational store; the audit hash is sha256(salt ‖ payload).
func TestAuditPayloadHashIsSalted(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	o, _ := s.OpenIncident(ctx, openParams(t))
	// Identifiers never reach payloads (strict screen.answered), so a flag
	// with a free-form note stands in for a payload worth protecting.
	appendEv(t, s, o.IncidentID, incident.EvFlagged, `{"flag":"note","note":"0772123456"}`, "")
	appendEv(t, s, o.IncidentID, incident.EvFlagged, `{"flag":"note","note":"0772123456","again":1}`, "")
	owner := c.Owner(t, pg.StoreOperational)
	rows, err := owner.Query(ctx, `
		SELECT e.payload::text, e.payload_salt, o.payload->>'payload_sha256'
		FROM op.incident_events e JOIN op.outbox o ON (o.payload->>'event_id')::uuid = e.id
		WHERE e.type = 'incident.flagged' ORDER BY e.seq`)
	if err != nil {
		t.Fatalf("query salted events: %v", err)
	}
	var salts [][]byte
	for rows.Next() {
		var text, hashHex string
		var salt []byte
		if err := rows.Scan(&text, &salt, &hashHex); err != nil {
			t.Fatal(err)
		}
		if len(salt) != 16 {
			t.Fatalf("salt has %d bytes, want 16", len(salt))
		}
		plain := sha256.Sum256([]byte(text))
		salted := sha256.Sum256(append(append([]byte{}, salt...), text...))
		if hashHex == hex.EncodeToString(plain[:]) {
			t.Fatal("audit payload hash is the unsalted, reversible sha256(payload)")
		}
		if hashHex != hex.EncodeToString(salted[:]) {
			t.Fatalf("audit payload hash %s != sha256(salt ‖ payload)", hashHex)
		}
		salts = append(salts, salt)
	}
	if len(salts) != 2 || string(salts[0]) == string(salts[1]) {
		t.Fatalf("salts = %x, want two distinct per-event salts", salts)
	}
}
