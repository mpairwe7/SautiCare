//go:build integration

package pg_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"sautihealth.com/sauti/internal/audit"
	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/platform/devkeys"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/auditstore"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

type env struct {
	intake *opstore.Store
	relay  *audit.Relay
	ledger *auditstore.Store
	reader *auditstore.Store
	keys   *devkeys.Store
	svc    *incident.Service
}

func newEnv(t *testing.T) (*pgtest.TestCluster, env) {
	t.Helper()
	c := pgtest.Cluster(t)
	ctx := context.Background()
	pool := func(s pg.Store, role string) *pgxpool.Pool {
		p, err := pg.Connect(ctx, c.DSN(s, role), "test", 10)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	dir := t.TempDir()
	if err := devkeys.Generate(dir, rand.Reader); err != nil {
		t.Fatal(err)
	}
	keys, err := devkeys.Open(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	intake := opstore.New(pool(pg.StoreOperational, "op_intake"))
	ledger := auditstore.New(pool(pg.StoreAudit, "audit_appender"))
	e := env{
		intake: intake,
		ledger: ledger,
		reader: auditstore.New(pool(pg.StoreAudit, "audit_reader")),
		relay: audit.NewRelay(opstore.New(pool(pg.StoreOperational, "op_relay")), ledger,
			slog.New(slog.NewTextHandler(io.Discard, nil)), nil, 50),
		keys: keys,
		svc:  incident.NewService(intake, crypto.Identifiers{Env: crypto.NewEnvelope(keys, rand.Reader)}, rand.Reader),
	}
	return c, e
}

func drain(t *testing.T, r *audit.Relay) int {
	t.Helper()
	total := 0
	for {
		n, err := r.RunOnce(context.Background())
		if err != nil {
			t.Fatalf("relay: %v", err)
		}
		if n == 0 {
			return total
		}
		total += n
	}
}

func TestRelayEndToEndAgainstPostgres(t *testing.T) {
	_, e := newEnv(t)
	ctx := context.Background()
	events := 0
	for i := range 3 {
		phone, _ := pii.ParsePhone("0772123456")
		o, err := e.svc.StartSession(ctx, incident.StartSession{Channel: incident.ChannelUSSDAT,
			ProviderSessionID: "ATUid_" + uuid.NewV4().String(), Phone: phone, IdemKey: "start"})
		if err != nil {
			t.Fatal(err)
		}
		events++
		for range i + 1 {
			if _, err := e.svc.Append(ctx, incident.AppendParams{IncidentID: o.IncidentID, Type: incident.EvScreenAnswered,
				Payload: json.RawMessage(`{"screen":2}`), ActorKind: incident.ActorCaller, IdemKey: uuid.NewV4().String()}); err != nil {
				t.Fatal(err)
			}
			events++
		}
	}
	if n := drain(t, e.relay); n != events {
		t.Fatalf("relayed %d, want %d", n, events)
	}
	if n := drain(t, e.relay); n != 0 {
		t.Fatalf("second drain relayed %d", n)
	}
	cp := audit.NewCheckpointer(e.ledger, e.keys.Signer(), devkeys.NewFSWitness(t.TempDir()), "sauti-test", time.Now)
	if _, err := cp.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	rep, err := audit.Verify(ctx, e.reader, e.keys.Signer().PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() || rep.Entries != int64(events) || rep.CheckpointsOK != 1 {
		t.Fatalf("verify report = %+v", rep)
	}
}

// Review focus 3: a phone-like string in an event payload never reaches the
// audit ledger; only its hash does.
func TestAuditEntryNeverCarriesPayloadContent(t *testing.T) {
	c, e := newEnv(t)
	ctx := context.Background()
	phone, _ := pii.ParsePhone("0772123456")
	o, err := e.svc.StartSession(ctx, incident.StartSession{Channel: incident.ChannelUSSDAT,
		ProviderSessionID: "ATUid_x", Phone: phone, IdemKey: "start"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Append(ctx, incident.AppendParams{IncidentID: o.IncidentID, Type: incident.EvScreenAnswered,
		Payload: json.RawMessage(`{"screen":8,"callback":"0772123456"}`), ActorKind: incident.ActorCaller, IdemKey: "s8"}); err != nil {
		t.Fatal(err)
	}
	drain(t, e.relay)
	rows, err := c.Owner(t, pg.StoreAudit).Query(ctx, "SELECT e::text FROM audit.entries e")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(s, "772123456") {
			t.Fatalf("audit row contains the number: %s", s)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("audit rows = %d, want 2", n)
	}
}

// Only one relay instance is active; a standby takes over when it stops.
func TestSingleActiveRelayLeader(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := context.Background()
	p, err := pg.Connect(ctx, c.DSN(pg.StoreOperational, "op_relay"), "test", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var active, maxActive atomic.Int32
	run := func(ctx context.Context) error {
		n := active.Add(1)
		for {
			m := maxActive.Load()
			if n <= m || maxActive.CompareAndSwap(m, n) {
				break
			}
		}
		<-ctx.Done()
		active.Add(-1)
		return nil
	}
	ctx1, stop1 := context.WithCancel(ctx)
	ctx2, stop2 := context.WithCancel(ctx)
	defer stop2()
	done1 := make(chan error, 1)
	done2 := make(chan error, 1)
	go func() { done1 <- pg.RunAsLeader(ctx1, p, 7347999, 100*time.Millisecond, run) }()
	time.Sleep(500 * time.Millisecond)
	go func() { done2 <- pg.RunAsLeader(ctx2, p, 7347999, 100*time.Millisecond, run) }()
	time.Sleep(700 * time.Millisecond)
	if active.Load() != 1 {
		t.Fatalf("active leaders = %d, want 1", active.Load())
	}
	stop1()
	<-done1
	deadline := time.Now().Add(5 * time.Second)
	for active.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if active.Load() != 1 || maxActive.Load() != 1 {
		t.Fatalf("after failover active=%d max=%d, want 1 and 1", active.Load(), maxActive.Load())
	}
	stop2()
	<-done2
}
