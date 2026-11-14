package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/platform/demo"
	"sautihealth.com/sauti/internal/platform/devkeys"
	"sautihealth.com/sauti/internal/platform/egress"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/platform/sim"
	"sautihealth.com/sauti/internal/responder"
)

// Service codes: Tool 1 for callers; the responder menu, which the alert SMS
// names (config/gated/sms.yaml).
const (
	callerCode    = "*384*1#"
	responderCode = "*384*2#"
	network       = "64110" // MTN Uganda
	origin        = "sauti-demo"
)

// Synthetic phones, with numbers from the ranges the repository's tests use
// and role labels for names. Ward 1 has three VHTs, so a red call shows the
// parallel alerts and the first acceptance winning; ward 2 has one of each;
// wards 3 and 4 have none, so a call there shows the escalation.
var (
	callers = []phone{
		{Handle: "caller-a", Label: "Caller A", Kind: "caller", e164: "+256772123456"},
		{Handle: "caller-b", Label: "Caller B", Kind: "caller", e164: "+256772000777"},
	}
	seeds = []demo.Responder{
		{Role: responder.RoleVHT, Label: "VHT 1", Phone: "+256772000201", Ward: "w01", Stage: "w01_stage_a"},
		{Role: responder.RoleVHT, Label: "VHT 2", Phone: "+256772000202", Ward: "w01", Stage: "w01_stage_b"},
		{Role: responder.RoleVHT, Label: "VHT 3", Phone: "+256772000203", Ward: "w01", Stage: "w01_stage_a"},
		{Role: responder.RoleTransporter, Label: "Transporter 1", Phone: "+256772000204", Ward: "w01", Stage: "w01_stage_a"},
		{Role: responder.RoleTransporter, Label: "Transporter 2", Phone: "+256772000205", Ward: "w01", Stage: "w01_stage_b"},
		{Role: responder.RoleVHT, Label: "VHT 4", Phone: "+256772000206", Ward: "w02", Stage: "w02_stage_a"},
		{Role: responder.RoleTransporter, Label: "Transporter 3", Phone: "+256772000207", Ward: "w02", Stage: "w02_stage_a"},
	}
)

// phone is one handset on screen. Its number stays in the demo process:
// Number renders masked in JSON.
type phone struct {
	Handle string    `json:"id"`
	Label  string    `json:"label"`
	Kind   string    `json:"kind"` // caller or responder
	Role   string    `json:"role,omitempty"`
	Ward   string    `json:"ward,omitempty"`
	Number pii.Phone `json:"number"`
	e164   string
}

// stack is one run of the system: a private PostgreSQL, the five stores,
// keys and witness in one temporary directory, the fake SMS endpoint and the
// three services. Reset replaces it; stop erases it.
type stack struct {
	dir      string
	pg       *proc
	services []*proc
	sink     *smsSink
	sinkSrv  *http.Server
	cancel   context.CancelFunc // ends delivery reports

	keys       *devkeys.Store
	witnessDir string
	ro         *pgxpool.Pool // op_readonly: the board
	auditRead  *pgxpool.Pool // audit_reader: the verifier
	auditWrite *pgxpool.Pool // audit_appender: on-demand checkpoints, as `sautictl checkpoint`
	pools      []*pgxpool.Pool

	phones []phone
	labels map[string]string // responder ID → label
	routes map[string]*sim.USSD

	mu       sync.Mutex
	sessions map[string]*session
	dials    []time.Time // dial times in the last minute
	audit    sync.Mutex  // one verification at a time
}

type session struct {
	mu   sync.Mutex
	s    *sim.USSDSession
	used time.Time
}

// startStack starts a new run. On error everything it started is stopped
// and its directory removed.
func startStack(ctx context.Context, o options, log io.Writer) (st *stack, err error) {
	dir, err := os.MkdirTemp(o.data, "sauti-demo-")
	if err != nil {
		return nil, err
	}
	sctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	st = &stack{dir: dir, cancel: cancel, witnessDir: filepath.Join(dir, "witness"),
		labels: map[string]string{}, routes: map[string]*sim.USSD{}, sessions: map[string]*session{}}
	defer func() {
		if err != nil {
			st.stop()
			st = nil
		}
	}()
	pgp, admin, err := startPostgres(ctx, o.pgBin, dir, log)
	if err != nil {
		return st, err
	}
	st.pg = pgp

	// Stores, roles (a random password for this run), migrations, gazetteer.
	adminConn, err := pgx.Connect(ctx, admin)
	if err != nil {
		return st, err
	}
	stores, err := demo.Provision(ctx, adminConn, admin, "dev", secret(), o.configDir)
	_ = adminConn.Close(ctx)
	if err != nil {
		return st, fmt.Errorf("demo: provision: %w", err)
	}
	keysDir := filepath.Join(dir, "keys")
	if err := devkeys.Generate(keysDir, rand.Reader); err != nil {
		return st, err
	}
	if st.keys, err = devkeys.Open(keysDir, "dev"); err != nil {
		return st, err
	}
	pool := func(s pg.Store, role string, n int32) (*pgxpool.Pool, error) {
		p, err := pg.Connect(ctx, stores.DSN(s, role), "sauti-demo", n)
		if err == nil {
			st.pools = append(st.pools, p)
		}
		return p, err
	}
	console, err := pool(pg.StoreOperational, "op_console", 2)
	if err != nil {
		return st, err
	}
	intake, err := pool(pg.StoreOperational, "op_intake", 2)
	if err != nil {
		return st, err
	}
	if st.ro, err = pool(pg.StoreOperational, "op_readonly", 4); err != nil {
		return st, err
	}
	if st.auditRead, err = pool(pg.StoreAudit, "audit_reader", 2); err != nil {
		return st, err
	}
	if st.auditWrite, err = pool(pg.StoreAudit, "audit_appender", 2); err != nil {
		return st, err
	}
	ids, err := demo.RegisterResponders(ctx, opstore.New(console), opstore.New(intake), st.keys, seeds)
	if err != nil {
		return st, fmt.Errorf("demo: register responders: %w", err)
	}
	st.phones = append(st.phones, callers...)
	for i, d := range seeds {
		st.labels[ids[i].String()] = d.Label
		st.phones = append(st.phones, phone{Handle: handleOf(d.Label), Label: d.Label, Kind: "responder",
			Role: string(d.Role), Ward: d.Ward, e164: d.Phone})
	}
	for i := range st.phones {
		if st.phones[i].Number, err = pii.ParsePhone(st.phones[i].e164); err != nil {
			return st, err
		}
	}

	// Ports, and a fresh token and key for this run.
	sinkLn, picked, err := runListeners(ctx, 4)
	if err != nil {
		return st, err
	}
	ports := map[string]string{"intake": picked[0], "intake-admin": picked[1], "dispatch-admin": picked[2], "relay": picked[3]}
	pathToken, deliveryToken, apiKey := secret(), secret(), secret()
	intakeURL := "http://127.0.0.1:" + ports["intake"]
	local := egress.NewClient([]string{"127.0.0.1"}, 5*time.Second)
	st.sink = &smsSink{apiKey: apiKey, report: deliveryReporter(sctx, local, intakeURL+"/v1/sms/delivery/at/"+deliveryToken)}
	st.sinkSrv = &http.Server{Handler: st.sink, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = st.sinkSrv.Serve(sinkLn) }()
	smsURL := "http://" + sinkLn.Addr().String()
	if err := checkLoopback(smsURL); err != nil {
		return st, err
	}

	common := map[string]string{"SAUTI_ENV": "dev", "SAUTI_CONFIG_DIR": o.configDir, "SAUTI_DEVKEYS_DIR": keysDir}
	envs := map[string]map[string]string{
		"intake-gw": {"SAUTI_OP_DSN": stores.DSN(pg.StoreOperational, "op_intake"),
			"SAUTI_HTTP_ADDR": "127.0.0.1:" + ports["intake"], "SAUTI_ADMIN_ADDR": "127.0.0.1:" + ports["intake-admin"],
			"SAUTI_AT_PATH_TOKEN": pathToken, "SAUTI_AT_DELIVERY_TOKEN": deliveryToken,
			"SAUTI_USSD_SERVICE_CODES": callerCode, "SAUTI_USSD_RESPONDER_CODES": responderCode, "SAUTI_RULES_ROOT": o.rulesRoot},
		"dispatch-worker": {"SAUTI_OP_DSN": stores.DSN(pg.StoreOperational, "op_dispatch"),
			"SAUTI_ADMIN_ADDR": "127.0.0.1:" + ports["dispatch-admin"],
			"SAUTI_AT_API_KEY": apiKey, "SAUTI_AT_USERNAME": "sauti-demo", "SAUTI_AT_SMS_URL": smsURL},
		"outbox-relay": {"SAUTI_OP_DSN": stores.DSN(pg.StoreOperational, "op_relay"),
			"SAUTI_AUDIT_DSN": stores.DSN(pg.StoreAudit, "audit_appender"),
			"SAUTI_HTTP_ADDR": "127.0.0.1:" + ports["relay"], "SAUTI_WITNESS_DIR": st.witnessDir, "SAUTI_ORIGIN": origin},
	}
	health := map[string]string{"intake-gw": ports["intake-admin"], "dispatch-worker": ports["dispatch-admin"], "outbox-relay": ports["relay"]}
	for _, name := range services {
		vars := envs[name]
		for k, v := range common {
			vars[k] = v
		}
		path, err := o.binary(name)
		if err != nil {
			return st, err
		}
		p, err := startProc(name, path, nil, childEnv(vars), log)
		if err != nil {
			return st, err
		}
		st.services = append(st.services, p)
	}
	rctx, rcancel := context.WithTimeout(ctx, 90*time.Second)
	defer rcancel()
	for _, p := range st.services {
		if err := waitReady(rctx, p, "http://127.0.0.1:"+health[p.name]+"/readyz"); err != nil {
			return st, err
		}
	}
	for _, code := range []string{callerCode, responderCode} {
		st.routes[code] = sim.NewUSSD(intakeURL, pathToken, code, network)
	}
	return st, nil
}

// runListeners binds the fake SMS endpoint's listener first and keeps it
// open while it picks n service ports, so no service is given the sink's port.
func runListeners(ctx context.Context, n int) (net.Listener, []string, error) {
	sink, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	ports, err := freePorts(n)
	if err != nil {
		_ = sink.Close()
		return nil, nil, err
	}
	return sink, ports, nil
}

func handleOf(label string) string { return strings.ReplaceAll(strings.ToLower(label), " ", "-") }

// stop stops the services, the fake SMS endpoint and PostgreSQL, and erases
// the run's directory: data, keys and witness.
func (st *stack) stop() {
	if st == nil {
		return
	}
	var wg sync.WaitGroup
	for _, p := range st.services {
		wg.Add(1)
		go func() { defer wg.Done(); p.stop(syscall.SIGTERM, 15*time.Second) }()
	}
	wg.Wait()
	st.cancel()
	if st.sinkSrv != nil {
		_ = st.sinkSrv.Close()
	}
	for _, p := range st.pools {
		p.Close()
	}
	st.pg.stop(syscall.SIGINT, 20*time.Second)
	_ = os.RemoveAll(st.dir) //nolint:gosec // G703: the run's own MkdirTemp directory
}

// down names any service that has exited.
func (st *stack) down() []string {
	var out []string
	for _, p := range st.services {
		if !p.running() {
			out = append(out, p.name)
		}
	}
	return out
}

func (st *stack) phone(handle string) (phone, bool) {
	for _, p := range st.phones {
		if p.Handle == handle {
			return p, true
		}
	}
	return phone{}, false
}

// Limits on a public demo: open sessions, and dials per minute.
const (
	maxSessions   = 200
	maxDialsMin   = 60
	sessionMaxAge = 3 * time.Minute // a network USSD session ends within about 180 s
)

var errBusy = errors.New("the demo is busy; try again in a minute")

// dial opens a USSD session from a phone on a service code and returns its
// token. Sessions idle past sessionMaxAge are dropped first.
func (st *stack) dial(p phone, route *sim.USSD) (string, *session, error) {
	now := time.Now()
	st.mu.Lock()
	defer st.mu.Unlock()
	for k, s := range st.sessions {
		if now.Sub(s.used) > sessionMaxAge {
			delete(st.sessions, k)
		}
	}
	recent := st.dials[:0]
	for _, t := range st.dials {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	st.dials = recent
	if len(st.sessions) >= maxSessions || len(st.dials) >= maxDialsMin {
		return "", nil, errBusy
	}
	st.dials = append(st.dials, now)
	tok := secret()
	s := &session{s: route.Dial(p.e164), used: now}
	st.sessions[tok] = s
	return tok, s, nil
}

func (st *stack) session(tok string) *session {
	st.mu.Lock()
	defer st.mu.Unlock()
	s := st.sessions[tok]
	if s != nil {
		s.used = time.Now()
	}
	return s
}

func (st *stack) hangUp(tok string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.sessions, tok)
}

// send submits one input on a session (one at a time per session).
func (s *session) send(input string) (sim.Screen, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.s.Send(input)
}
