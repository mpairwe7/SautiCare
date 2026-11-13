// Command dispatch-worker decides who is alerted and when (Build Prompt
// §10.4; ADR-0024). One leader across all instances claims incidents whose
// dispatch wake has come — most urgent first — reads their facts, runs the
// pure planner, records each action through the dispatch functions of the
// operational store, delivers what is undelivered, and stores the next
// wake. It keeps no dispatch state of its own: a restart or a failover
// resumes from the store, with no lost or duplicate alert (scenario 14).
//
// Environment:
//
//	SAUTI_ENV          dev | test | production
//	SAUTI_OP_DSN       operational store, role op_dispatch
//	SAUTI_ADMIN_ADDR   health and metrics (default 127.0.0.1:9102)
//	SAUTI_CONFIG_DIR   configuration directory (config/)
//	SAUTI_DEVKEYS_DIR  development key directory (refused in production)
//	SAUTI_AT_API_KEY   Africa's Talking API key (secret); unset: simulated delivery (dev/test only)
//	SAUTI_AT_USERNAME  Africa's Talking application username (default sandbox)
//	SAUTI_AT_SMS_URL   messaging base URL (default https://api.sandbox.africastalking.com)
//	SAUTI_AT_SENDER_ID optional alphanumeric sender ID
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/platform/africastalking"
	"sautihealth.com/sauti/internal/platform/channels"
	"sautihealth.com/sauti/internal/platform/devkeys"
	"sautihealth.com/sauti/internal/platform/egress"
	"sautihealth.com/sauti/internal/platform/env"
	"sautihealth.com/sauti/internal/platform/health"
	"sautihealth.com/sauti/internal/platform/logging"
	"sautihealth.com/sauti/internal/platform/metrics"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/platform/wiring"
)

// leaderKey is the advisory-lock key for the dispatch leader (outbox-relay
// holds 7347002, the abandonment sweeper 7347003).
const leaderKey = 7347004

const (
	poll  = time.Second      // how often the leader looks for due incidents
	batch = 50               // incidents claimed per pass
	lease = 30 * time.Second // a claimed incident is retried after this if the pass dies
)

func main() {
	log := slog.New(logging.NewHandler(os.Stderr, slog.LevelInfo))
	if err := run(log); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("dispatch-worker stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	senv, err := env.Environment()
	if err != nil {
		return err
	}
	cfg, err := config.Load(os.DirFS(env.Default("SAUTI_CONFIG_DIR", "config")))
	if err != nil {
		return err
	}
	if senv == "production" {
		if err := cfg.ProductionReady(); err != nil {
			return err
		}
	}
	dsn, err := env.Required("SAUTI_OP_DSN")
	if err != nil {
		return err
	}
	pool, err := pg.Connect(ctx, dsn, "dispatch-worker", 10)
	if err != nil {
		return err
	}
	defer pool.Close()
	store := opstore.New(pool)
	msg, err := pickMessenger(senv, os.Getenv("SAUTI_AT_API_KEY"), func() (messenger, error) {
		return smsChannel(ctx, cfg, store, senv)
	}, log)
	if err != nil {
		return err
	}
	built, err := wiring.BuildDispatch(ctx, wiring.DispatchOptions{Config: cfg, Store: store, Deliverer: msg, Notifier: msg,
		Suspension: store, Production: senv == "production"})
	if err != nil {
		return err
	}

	// Review I8: a stalled leader is not ready, and the metrics show how late
	// dispatch is, so an alert reaches a person; intake-gw's watchdog
	// escalates untouched incidents independently of this process.
	var leading atomic.Bool
	reg := metrics.NewRegistry()
	passFailures := reg.Counter("sauti_dispatch_pass_failures_total", "Dispatch passes that failed and were escalated.")
	lastPassAge := reg.Gauge("sauti_dispatch_last_pass_age_seconds", "Seconds since the leader's last successful pass (0 on a standby).")
	overdue := reg.Gauge("sauti_dispatch_oldest_overdue_seconds", "How late the most overdue dispatch wake is.")
	suspendedGauge := reg.Gauge("sauti_dispatch_suspended", "1 while dispatch is suspended (O6).")
	admin := http.NewServeMux()
	admin.Handle("/livez", health.Handler(map[string]health.Check{}, nil))
	admin.Handle("/readyz", health.Handler(nil, map[string]health.Check{
		"operational_db": pool.Ping,
		"dispatch_loop": func(context.Context) error {
			return dispatchLoopReady(leading.Load(), built.Engine.LastPass(), time.Now())
		},
	}))
	admin.Handle("/metrics", reg.Handler())
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			if d, err := store.OldestOverdueDispatch(ctx); err == nil {
				overdue.Set(d.Seconds())
			}
			if on, err := store.DispatchSuspended(ctx); err == nil {
				suspendedGauge.Set(map[bool]float64{true: 1}[on])
			}
			if last := built.Engine.LastPass(); leading.Load() && !last.IsZero() {
				lastPassAge.Set(time.Since(last).Seconds())
			} else {
				lastPassAge.Set(0)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	adminSrv := adminServer(env.Default("SAUTI_ADMIN_ADDR", "127.0.0.1:9102"), admin)
	adminErr := make(chan error, 1)
	go func() { adminErr <- adminSrv.ListenAndServe() }()

	log.Info("dispatch-worker starting", "env", senv, "config", cfg.Fingerprint, "mode", built.Params.Mode,
		"dispatch_logic", built.LogicVersion, "gazetteer", built.Gazetteer.Version)
	onErr := func(id uuid.UUID, err error) {
		if errors.Is(err, dispatch.ErrStoppingRuleBreached) {
			// O6: an alarm for the coordinator and the PI; resuming is a
			// logged staff decision (sautictl dispatch resume, or the console).
			log.Error("DISPATCH SUSPENDED by the stopping rule; responders are not being alerted", "err", err)
			return
		}
		passFailures.Add(1)
		log.Error("dispatch pass failed", "incident", id, "err", err)
	}
	leaderErr := make(chan error, 1)
	go func() {
		leaderErr <- pg.RunAsLeader(ctx, pool, leaderKey, 5*time.Second, func(lctx context.Context) error {
			leading.Store(true)
			defer leading.Store(false)
			// The availability-expiry reminders (review focus 4) run with the
			// leader, each period once.
			go func() {
				_ = dispatch.Reminders{Store: store, Sender: msg}.Run(lctx, time.Minute,
					func(err error) { log.Error("availability reminder failed", "err", err) })
			}()
			return built.Engine.Run(lctx, poll, batch, lease, onErr)
		})
	}()

	err = await(ctx, stop, adminErr, leaderErr)
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = adminSrv.Shutdown(sctx)
	return err
}

// await waits for a signal, or the first failure of the admin listener or
// the leader. On every path it cancels the leader (stop) and waits for
// RunAsLeader to return before run's deferred pool.Close: the leader holds
// its lock connection until its context ends, and pool.Close waits for every
// connection, so returning first would hang the worker with the lock held
// (CodeRabbit PR #10). RunAsLeader's own calls are bounded, so the wait is.
func await(ctx context.Context, stop context.CancelFunc, adminErr, leaderErr <-chan error) error {
	var err error
	leaderDone := false
	select {
	case <-ctx.Done():
	case err = <-adminErr:
	case err = <-leaderErr:
		leaderDone = true
	}
	stop()
	if !leaderDone {
		// Cancelled, the leader returns context.Canceled; anything else is
		// reported unless an earlier failure is.
		if lerr := <-leaderErr; err == nil && !errors.Is(lerr, context.Canceled) {
			err = lerr
		}
	}
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// adminServer serves health and metrics. Every phase of a connection is
// bounded, so a client cannot hold one open wherever SAUTI_ADMIN_ADDR
// exposes the listener; writes allow for the 2 s health checks.
func adminServer(addr string, h http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
}

// messenger is what dispatch-worker sends: alerts and stand-downs,
// caller notices, availability reminders.
type messenger interface {
	dispatch.Deliverer
	dispatch.Notifier
	dispatch.ReminderSender
}

// pickMessenger chooses the channel. Production refuses to start until the
// key provider exists (P0: the worker opens responders' and callers'
// numbers). With Africa's Talking credentials the worker sends real SMS
// (the sandbox by default); without them it simulates delivery, logging
// identifiers only (development and tests).
func pickMessenger(senv, apiKey string, real func() (messenger, error), log *slog.Logger) (messenger, error) {
	if senv == "production" {
		return nil, errors.New("dispatch-worker: production key provider (OpenBao) arrives in P0; refusing to start")
	}
	if apiKey != "" {
		return real()
	}
	return simMessenger{log: log}, nil
}

// smsChannel is the Africa's Talking SMS channel over the egress client.
func smsChannel(ctx context.Context, cfg config.Bundle, store *opstore.Store, senv string) (messenger, error) {
	keys, err := devkeys.Open(env.Default("SAUTI_DEVKEYS_DIR", ".devkeys"), senv)
	if err != nil {
		return nil, err
	}
	gaz, err := store.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	base := env.Default("SAUTI_AT_SMS_URL", "https://api.sandbox.africastalking.com")
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	client, err := africastalking.NewSMSClient(africastalking.SMSConfig{BaseURL: base, Username: env.Default("SAUTI_AT_USERNAME", "sandbox"),
		APIKey: os.Getenv("SAUTI_AT_API_KEY"), SenderID: os.Getenv("SAUTI_AT_SENDER_ID"),
		HTTP: egress.NewClient([]string{u.Hostname()}, 10*time.Second)})
	if err != nil {
		return nil, err
	}
	// Decisions 1, 2 and 6 sit behind one amendment: location in SMS follows
	// the dispatch mode.
	return channels.NewSMS(channels.SMSConfig{Directory: store, Opener: crypto.NewEnvelope(keys, rand.Reader), Sender: client,
		Templates: cfg.SMS, Tool1: cfg.Tool1, Gazetteer: gaz, Amended: cfg.Dispatch.Mode == dispatch.ModeAmended})
}

// simMessenger logs each message (identifiers only, never a number).
type simMessenger struct{ log *slog.Logger }

func (s simMessenger) Deliver(_ context.Context, d dispatch.Delivery) (string, error) {
	s.log.Info("simulated delivery", "purpose", d.Purpose, "channel", d.Channel, "incident", d.IncidentID,
		"alert", d.AlertID, "responder", d.ResponderID, "role", d.Role)
	return "sim-" + d.AttemptID.String()[:8], nil
}

func (s simMessenger) Notify(_ context.Context, n dispatch.Notice) (string, error) {
	s.log.Info("simulated caller notice", "kind", n.Kind, "incident", n.IncidentID)
	return "sim-" + n.AttemptID.String()[:8], nil
}

func (s simMessenger) NotifyFacility(_ context.Context, incidentID uuid.UUID, facility string) (string, error) {
	s.log.Info("simulated facility SMS", "incident", incidentID, "facility", facility)
	return "sim-facility", nil
}

func (s simMessenger) Remind(_ context.Context, r dispatch.Reminder) (string, error) {
	s.log.Info("simulated availability reminder", "responder", r.ResponderID, "until", r.Until)
	return "sim-reminder", nil
}

// stallLimit is how old the leader's last successful pass may be before it
// reports not ready (review I8).
const stallLimit = 2 * time.Minute

// dispatchLoopReady fails for a leader whose loop has not completed a pass
// within stallLimit; a standby is ready (it can take over).
func dispatchLoopReady(leading bool, last, now time.Time) error {
	if !leading {
		return nil
	}
	if last.IsZero() || now.Sub(last) > stallLimit {
		return errors.New("dispatch loop stalled: no successful pass within the limit")
	}
	return nil
}
