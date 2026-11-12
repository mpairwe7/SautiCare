// Package demo runs milestone demos end to end against a real PostgreSQL 18
// server in isolated, freshly migrated databases. The M0 demo (spec §12.2)
// is shared by `sautictl demo m0` and tests/e2e. Development only: it
// refuses production through bootstrap and devkeys.
package demo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"sautihealth.com/sauti/db"
	"sautihealth.com/sauti/internal/audit"
	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/platform/africastalking"
	"sautihealth.com/sauti/internal/platform/devkeys"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/auditstore"
	"sautihealth.com/sauti/internal/platform/pg/bootstrap"
	"sautihealth.com/sauti/internal/platform/pg/migrate"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/platform/sim"
)

// Options configures a demo run.
type Options struct {
	AdminDSN string // superuser URL DSN of a development PostgreSQL 18 server
	// RolePassword is the server's established password for the Sauti roles
	// (SAUTI_DEV_DB_PASSWORD on a dev server). Roles are server-global, so the
	// demo must use it and never change it: a different value would break
	// every other user of the same roles on that server.
	RolePassword string
	ConfigDir    string    // path to config/
	Out          io.Writer // step-by-step narration
	Keep         bool      // keep the demo databases afterwards
}

// Report is what the M0 demo proved.
type Report struct {
	Reference          string
	T1                 time.Time
	Events             int
	LedgerEntries      int64
	PIIInLedger        bool
	CheckpointSeq      int64
	WitnessWritten     bool
	LedgerVerified     bool
	ProjectionsChecked int
	ProjectionDrift    int
	TamperDetectedAt   int64
	WitnessCaught      bool // step 7b: rewrite + recomputed chain + deleted checkpoints
	CheckpointRefused  bool // step 7b: the anchored checkpointer refuses to sign
	RetryIdentical     bool
}

const demoPhone = "+256772123456"

var spokenRef = regexp.MustCompile(`[0-9]{3} [0-9]{3} [0-9]{4}`)

type runner struct {
	o        Options
	admin    *url.URL
	password string
	dbs      map[pg.Store]string
	pools    []*pgxpool.Pool
}

func (r *runner) say(format string, args ...any) { fmt.Fprintf(r.o.Out, format+"\n", args...) }

func (r *runner) dsn(s pg.Store, role string) string {
	u := *r.admin
	u.User = url.UserPassword(role, r.password)
	u.Path = "/" + r.dbs[s]
	return u.String()
}

func (r *runner) superDSN(s pg.Store) string {
	u := *r.admin
	u.Path = "/" + r.dbs[s]
	return u.String()
}

func (r *runner) pool(ctx context.Context, s pg.Store, role string) (*pgxpool.Pool, error) {
	p, err := pg.Connect(ctx, r.dsn(s, role), "sauti-demo", 8)
	if err == nil {
		r.pools = append(r.pools, p)
	}
	return p, err
}

// RunM0 executes the M0 demo and returns its report. Any unmet expectation
// is returned as an error naming the step.
func RunM0(ctx context.Context, o Options) (rep Report, err error) {
	if o.Out == nil {
		o.Out = io.Discard
	}
	admin, err := url.Parse(o.AdminDSN)
	if err != nil || admin.Scheme == "" {
		return rep, errors.New("demo: admin DSN must be a postgres:// URL")
	}
	if o.RolePassword == "" {
		return rep, errors.New("demo: the server's role password is required (SAUTI_DEV_DB_PASSWORD)")
	}
	r := &runner{o: o, admin: admin, password: o.RolePassword}
	defer func() {
		for _, p := range r.pools {
			p.Close()
		}
	}()

	// Fresh, isolated stores.
	adminConn, err := pgx.Connect(ctx, o.AdminDSN)
	if err != nil {
		return rep, fmt.Errorf("demo: connect admin: %w", err)
	}
	defer func() { _ = adminConn.Close(context.WithoutCancel(ctx)) }()
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	r.dbs, err = bootstrap.Run(ctx, adminConn, bootstrap.Options{Suffix: "_demo" + hex.EncodeToString(suffix), Password: r.password, Env: "dev"})
	if err != nil {
		return rep, err
	}
	if !o.Keep {
		defer func() {
			for _, p := range r.pools {
				p.Close()
			}
			r.pools = nil
			for _, name := range r.dbs {
				_, _ = adminConn.Exec(context.WithoutCancel(ctx), "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
			}
		}()
	}
	for _, s := range pg.Stores() {
		fsys, err := db.Migrations(string(s))
		if err != nil {
			return rep, err
		}
		c, err := pgx.Connect(ctx, r.dsn(s, s.Owner()))
		if err != nil {
			return rep, err
		}
		_, err = migrate.Run(ctx, c, string(s), fsys)
		_ = c.Close(ctx)
		if err != nil {
			return rep, err
		}
	}
	r.say("✓ setup: five stores bootstrapped and migrated (%s)", r.dbs[pg.StoreOperational])

	work, err := os.MkdirTemp("", "sauti-demo-")
	if err != nil {
		return rep, err
	}
	defer func() { _ = os.RemoveAll(work) }()
	if err := devkeys.Generate(filepath.Join(work, "keys"), rand.Reader); err != nil {
		return rep, err
	}
	keys, err := devkeys.Open(filepath.Join(work, "keys"), "dev")
	if err != nil {
		return rep, err
	}
	cfg, err := config.Load(os.DirFS(o.ConfigDir))
	if err != nil {
		return rep, err
	}

	intakePool, err := r.pool(ctx, pg.StoreOperational, "op_intake")
	if err != nil {
		return rep, err
	}
	relayPool, err := r.pool(ctx, pg.StoreOperational, "op_relay")
	if err != nil {
		return rep, err
	}
	roPool, err := r.pool(ctx, pg.StoreOperational, "op_readonly")
	if err != nil {
		return rep, err
	}
	appenderPool, err := r.pool(ctx, pg.StoreAudit, "audit_appender")
	if err != nil {
		return rep, err
	}
	readerPool, err := r.pool(ctx, pg.StoreAudit, "audit_reader")
	if err != nil {
		return rep, err
	}

	ops := opstore.New(intakePool)
	incidents := incident.NewService(ops, crypto.Identifiers{Env: crypto.NewEnvelope(keys, rand.Reader)}, rand.Reader)
	ledger := auditstore.New(appenderPool)
	reader := auditstore.New(readerPool)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	relay := audit.NewRelay(opstore.New(relayPool), ledger, quiet, nil, 100)

	tok := make([]byte, 16)
	_, _ = rand.Read(tok)
	token := hex.EncodeToString(tok)
	handler, err := africastalking.NewUSSDHandler(africastalking.USSDConfig{
		Provider: "at", PathToken: token, ServiceCodes: []string{"*384*1#"}, ErrorText: cfg.Screens.InternalError["en"],
	}, intake.NewService(incidents, ops, cfg.Screens, "en"), quiet)
	if err != nil {
		return rep, err
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Step 1: dial.
	session := sim.NewUSSD(srv.URL, token, "*384*1#", "64110").Dial(demoPhone)
	first, err := session.Send("")
	if err != nil {
		return rep, fmt.Errorf("step 1: dial: %w", err)
	}
	ref, err := incident.ParseReference(spokenRef.FindString(first.Text))
	if err != nil {
		return rep, fmt.Errorf("step 1: reply %q carries no valid reference: %w", first.Text, err)
	}
	ids, err := opstore.New(roPool).ListIncidentIDs(ctx)
	if err != nil || len(ids) != 1 {
		return rep, fmt.Errorf("step 1: expected one incident, got %d: %w", len(ids), err)
	}
	id := ids[0]
	proj, err := opstore.New(roPool).Projection(ctx, id)
	if err != nil || proj.T[1] == nil || proj.Reference != ref {
		return rep, fmt.Errorf("step 1: projection %+v: %w", proj, err)
	}
	rep.Reference, rep.T1 = ref.Spoken(), *proj.T[1]
	r.say("✓ step 1: caller dialled; incident %s opened, T1 = %s (database clock)", ref.Spoken(), rep.T1.Format(time.RFC3339Nano))

	// Step 2: fixture events through the single write path.
	logic := "tool1-pending+" + cfg.Fingerprint
	fixture := []struct {
		typ          incident.EventType
		payload      string
		logicVersion string
	}{
		{incident.EvScreenAnswered, `{"screen":2,"answer":"1"}`, ""},
		{incident.EvScreenAnswered, `{"screen":3,"answer":"1"}`, ""},
		{incident.EvClassified, `{"severity":"red","category":"trauma"}`, logic},
		{incident.EvScreenAnswered, `{"screen":7,"answer":"2"}`, ""},
		{incident.EvDispatchReady, `{"trigger_screen":7}`, ""},
		{incident.EvScreenAnswered, `{"screen":8,"answer":"0772123456"}`, ""},
		{incident.EvAlertIssued, `{"role":"vht","ring":1}`, ""},
		{incident.EvAlertIssued, `{"role":"transporter","ring":1}`, ""},
		{incident.EvAccepted, `{"role":"vht"}`, ""},
		{incident.EvAccepted, `{"role":"transporter"}`, ""},
		{incident.EvArrived, `{"role":"vht"}`, ""},
		{incident.EvDeparted, `{}`, ""},
		{incident.EvAtFacility, `{"facility":"kawempe-nrh"}`, ""},
		{incident.EvClosed, `{"reason":"completed"}`, ""},
	}
	for i, f := range fixture {
		if _, err := incidents.Append(ctx, incident.AppendParams{
			IncidentID: id, Type: f.typ, Payload: json.RawMessage(f.payload), ActorKind: incident.ActorSystem,
			IdemKey: fmt.Sprintf("demo:%d", i), LogicVersion: f.logicVersion,
		}); err != nil {
			return rep, fmt.Errorf("step 2: append %s: %w", f.typ, err)
		}
	}
	events, err := ops.Events(ctx, id)
	if err != nil {
		return rep, err
	}
	rep.Events = len(events)
	r.say("✓ step 2: %d events appended through op.append_incident_event (T2..T7 set by the database)", rep.Events)

	// Step 3: relay.
	relayed := 0
	for {
		n, err := relay.RunOnce(ctx)
		if err != nil {
			return rep, fmt.Errorf("step 3: relay: %w", err)
		}
		if n == 0 {
			break
		}
		relayed += n
	}
	r.say("✓ step 3: outbox-relay delivered %d rows to the audit ledger", relayed)

	// Step 4: ledger contents.
	err = reader.Range(ctx, 1, func(audit.Entry) error { rep.LedgerEntries++; return nil })
	if err != nil {
		return rep, err
	}
	if rep.LedgerEntries != int64(rep.Events) {
		return rep, fmt.Errorf("step 4: ledger has %d entries for %d events", rep.LedgerEntries, rep.Events)
	}
	super, err := pgx.Connect(ctx, r.superDSN(pg.StoreAudit))
	if err != nil {
		return rep, err
	}
	defer func() { _ = super.Close(context.WithoutCancel(ctx)) }()
	var leaks int
	if err := super.QueryRow(ctx, "SELECT count(*) FROM audit.entries e WHERE e::text LIKE '%772123456%'").Scan(&leaks); err != nil {
		return rep, err
	}
	rep.PIIInLedger = leaks > 0
	if rep.PIIInLedger {
		return rep, errors.New("step 4: a phone number reached the audit ledger")
	}
	r.say("✓ step 4: %d ledger entries, one per event; no phone number in any entry", rep.LedgerEntries)

	// Step 5: checkpoint.
	witnessDir := filepath.Join(work, "witness")
	witness := devkeys.NewFSWitness(witnessDir)
	cp, err := audit.NewCheckpointer(ledger, keys.Signer(), witness, "sauti-demo", time.Now).Checkpoint(ctx)
	if err != nil {
		return rep, fmt.Errorf("step 5: %w", err)
	}
	witnessed := func() ([]audit.Checkpoint, error) {
		notes, err := witness.ReadAll()
		if err != nil {
			return nil, err
		}
		var out []audit.Checkpoint
		for _, n := range notes {
			c, err := audit.ParseWitnessNote(n)
			if err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		return out, nil
	}
	rep.CheckpointSeq = cp.Seq
	files, _ := os.ReadDir(witnessDir)
	rep.WitnessWritten = len(files) == 1
	r.say("✓ step 5: checkpoint at seq %d signed with ML-DSA-65 (%d-byte signature) and witnessed", cp.Seq, len(cp.Signature))

	// Step 6: verification.
	ws, err := witnessed()
	if err != nil {
		return rep, err
	}
	vr, err := audit.Verify(ctx, reader, keys.Signer().PublicKey(), ws)
	if err != nil {
		return rep, err
	}
	rep.LedgerVerified = vr.OK()
	checked, drift, err := opstore.VerifyProjections(ctx, opstore.New(roPool))
	if err != nil {
		return rep, err
	}
	rep.ProjectionsChecked, rep.ProjectionDrift = checked, len(drift)
	if !rep.LedgerVerified || len(drift) > 0 {
		return rep, fmt.Errorf("step 6: ledger ok=%v (%s), projection drift %v", vr.OK(), vr.Problem, drift)
	}
	r.say("✓ step 6: verify-ledger OK (%d entries, %d checkpoint, %d witness); verify-projections OK (%d incident)", vr.Entries, vr.CheckpointsOK, vr.WitnessesOK, checked)

	// Step 7: tamper as a malicious administrator.
	if _, err := super.Exec(ctx, `ALTER TABLE audit.entries DISABLE TRIGGER entries_append_only;
		UPDATE audit.entries SET hash = set_byte(hash, 0, get_byte(hash, 0) # 1) WHERE seq = 3;
		ALTER TABLE audit.entries ENABLE TRIGGER entries_append_only;`); err != nil {
		return rep, fmt.Errorf("step 7: tamper: %w", err)
	}
	tv, err := audit.Verify(ctx, reader, keys.Signer().PublicKey(), ws)
	if err != nil {
		return rep, err
	}
	rep.TamperDetectedAt = tv.FirstBadSeq
	if tv.OK() {
		return rep, errors.New("step 7: tampering was not detected")
	}
	r.say("✓ step 7: one byte of entry 3 altered as superuser → verify-ledger fails at seq %d (%s)", tv.FirstBadSeq, tv.Problem)

	// Step 7b: the competent attacker (review finding C1) rewrites entry 3,
	// recomputes every later hash with the ledger's own canonical function and
	// deletes the database checkpoints. The chain alone now looks intact; the
	// write-once witness and the anchored checkpointer must still catch it.
	if _, err := super.Exec(ctx, RewriteAndRecomputeSQL); err != nil {
		return rep, fmt.Errorf("step 7b: attack: %w", err)
	}
	dbOnly, err := audit.Verify(ctx, reader, keys.Signer().PublicKey(), nil)
	if err != nil {
		return rep, err
	}
	wv, err := audit.Verify(ctx, reader, keys.Signer().PublicKey(), ws)
	if err != nil {
		return rep, err
	}
	rep.WitnessCaught = dbOnly.OK() && !wv.OK() && wv.WitnessesBad > 0
	_, cerr := audit.NewCheckpointer(ledger, keys.Signer(), witness, "sauti-demo", time.Now).WithAnchor(cp).Checkpoint(ctx)
	rep.CheckpointRefused = errors.Is(cerr, audit.ErrChainDiverged)
	if !rep.WitnessCaught || !rep.CheckpointRefused {
		return rep, fmt.Errorf("step 7b: rewrite not caught (db-only ok=%v, witness ok=%v, checkpoint err=%w)", dbOnly.OK(), wv.OK(), cerr)
	}
	r.say("✓ step 7b: entry 3 rewritten, chain recomputed, checkpoints deleted → chain-only check is fooled, but the witness fails it (%s) and the checkpointer refuses to sign", wv.Problem)

	// Step 8: aggregator retry.
	again, err := session.Retry()
	if err != nil {
		return rep, fmt.Errorf("step 8: retry: %w", err)
	}
	after, err := ops.Events(ctx, id)
	if err != nil {
		return rep, err
	}
	ids2, _ := opstore.New(roPool).ListIncidentIDs(ctx)
	rep.RetryIdentical = again == first && len(after) == rep.Events && len(ids2) == 1 && ids2[0] == id
	if !rep.RetryIdentical {
		return rep, errors.New("step 8: retried callback changed state or reply")
	}
	r.say("✓ step 8: retried callback returned the identical screen; no new incident or event")
	return rep, nil
}

// RewriteAndRecomputeSQL is the competent-attacker scenario of review
// finding C1, run as a superuser on the audit store: rewrite entry 3,
// recompute every later hash with the ledger's own canonical function, and
// delete every database checkpoint. Shared with the integration test.
const RewriteAndRecomputeSQL = `
ALTER TABLE audit.entries DISABLE TRIGGER entries_append_only;
ALTER TABLE audit.checkpoints DISABLE TRIGGER checkpoints_append_only;
UPDATE audit.entries SET event_type = 'incident.closed' WHERE seq = 3;
DO $$
DECLARE r record; prev bytea;
BEGIN
  SELECT hash INTO prev FROM audit.entries WHERE seq = 2;
  FOR r IN SELECT * FROM audit.entries WHERE seq >= 3 ORDER BY seq LOOP
    UPDATE audit.entries
       SET prev_hash = prev,
           hash = sha256(prev || audit.canonical(r.seq, r.entry_id, r.occurred_at, r.event_type,
                                                 r.incident_id, r.staff_id, r.logic_version, r.payload_sha256))
     WHERE seq = r.seq
    RETURNING hash INTO prev;
  END LOOP;
END $$;
DELETE FROM audit.checkpoints;
ALTER TABLE audit.entries ENABLE TRIGGER entries_append_only;
ALTER TABLE audit.checkpoints ENABLE TRIGGER checkpoints_append_only;`
