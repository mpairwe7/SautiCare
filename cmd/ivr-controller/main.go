// Command ivr-controller runs the toll-free voice line (Build Prompt §10.2)
// through Asterisk ARI: the same Tool 1 questions in recorded Luganda or
// English, answered on the keypad, with the same incident events, triage,
// location, deduplication and resume as USSD. Nothing is recorded. It refuses
// to start if the triage bundle, gazetteer or any screen fails its checks.
//
// Environment:
//
//	SAUTI_ENV             dev | test | production
//	SAUTI_OP_DSN          operational store, role op_ivr
//	SAUTI_ARI_URL         Asterisk HTTP server, never public (default http://127.0.0.1:8088)
//	SAUTI_ARI_USER        ARI user (default sauti)
//	SAUTI_ARI_PASSWORD    ARI password (secret; OpenBao in production)
//	SAUTI_ARI_APP         Stasis application (default sauti-ivr)
//	SAUTI_ARI_ALERT_APP   Stasis application of outbound alert calls (default sauti-alerts)
//	SAUTI_ARI_DIAL        endpoint template for alert calls, with {number} (e.g. PJSIP/{number}@sauti-trunk); unset: no voice alerts
//	SAUTI_ARI_CALLER_ID   the number responders see on an alert call (placeholder until P0)
//	SAUTI_ADMIN_ADDR      health and metrics (default 127.0.0.1:9101)
//	SAUTI_DEVKEYS_DIR     development key directory (refused in production)
//	SAUTI_CONFIG_DIR      configuration directory (config/)
//	SAUTI_RULES_ROOT      directory holding triage-rules/ (default .)
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/platform/ari"
	"sautihealth.com/sauti/internal/platform/devkeys"
	"sautihealth.com/sauti/internal/platform/env"
	"sautihealth.com/sauti/internal/platform/health"
	"sautihealth.com/sauti/internal/platform/ivr"
	"sautihealth.com/sauti/internal/platform/logging"
	"sautihealth.com/sauti/internal/platform/metrics"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/platform/wiring"
)

func main() {
	log := slog.New(logging.NewHandler(os.Stderr, slog.LevelInfo))
	if err := run(log); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("ivr-controller stopped", "err", err)
		os.Exit(1)
	}
}

// eventStream reports whether the ARI event stream is up (ari.Client).
type eventStream interface{ EventsConnected() bool }

// errNoEvents fails readiness while the ARI event stream is down: no call
// can be served, and Asterisk's dialplan sends callers to a coordinator.
var errNoEvents = errors.New("ari event stream disconnected")

// readiness lists what the voice line needs to serve a call: the
// operational store and the ARI event stream (review I4).
// alertCallsLeaderKey is the advisory-lock key of the voice-alert leader
// (outbox-relay 7347002, sweeper 7347003, dispatch-worker 7347004).
const alertCallsLeaderKey = 7347005

func readiness(db health.Check, events eventStream) map[string]health.Check {
	return map[string]health.Check{
		"operational_db": db,
		"ari_events": func(context.Context) error {
			if !events.EventsConnected() {
				return errNoEvents
			}
			return nil
		},
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
		return errors.New("ivr-controller: production key provider (OpenBao) arrives in P0; refusing to start")
	}
	dsn, err := env.Required("SAUTI_OP_DSN")
	if err != nil {
		return err
	}
	password, err := env.Required("SAUTI_ARI_PASSWORD")
	if err != nil {
		return err
	}
	keys, err := devkeys.Open(env.Default("SAUTI_DEVKEYS_DIR", ".devkeys"), senv)
	if err != nil {
		return err
	}
	pool, err := pg.Connect(ctx, dsn, "ivr-controller", 10)
	if err != nil {
		return err
	}
	defer pool.Close()

	doc, sig, err := wiring.ReadTriageBundle(env.Default("SAUTI_RULES_ROOT", "."), cfg.Triage.RulesFile)
	if err != nil {
		return err
	}
	store := opstore.New(pool)
	built, err := wiring.BuildIntake(ctx, wiring.IntakeOptions{Config: cfg, Store: store, Keys: keys, Rand: rand.Reader,
		TriageDoc: doc, TriageSig: sig, Production: senv == "production"})
	if err != nil {
		return err
	}
	client, err := ari.New(ari.Config{
		BaseURL: env.Default("SAUTI_ARI_URL", "http://127.0.0.1:8088"), User: env.Default("SAUTI_ARI_USER", "sauti"),
		Password: password, App: env.Default("SAUTI_ARI_APP", "sauti-ivr"), Timeout: 5 * time.Second,
	})
	if err != nil {
		return err
	}
	ctrl := ivr.New(client, built.Service, log, ivr.Options{NoInput: 15 * time.Second, MaxReprompts: 2, OpTimeout: 3 * time.Second})

	reg := metrics.NewRegistry()
	admin := http.NewServeMux()
	admin.Handle("/livez", health.Handler(map[string]health.Check{}, nil))
	admin.Handle("/readyz", health.Handler(nil, readiness(pool.Ping, client)))
	admin.Handle("/metrics", reg.Handler())
	adminSrv := &http.Server{Addr: env.Default("SAUTI_ADMIN_ADDR", "127.0.0.1:9101"), Handler: admin, ReadHeaderTimeout: 2 * time.Second}

	errc := make(chan error, 3)
	go func() { errc <- adminSrv.ListenAndServe() }()
	go func() { errc <- ctrl.Run(ctx, client) }()

	// Outbound voice alerts (M2 Part B; scenario 9): an alert still pending
	// after voice_fallback_after rings the responder. One leader places the
	// calls; every instance serves the answered calls' events.
	if dial := os.Getenv("SAUTI_ARI_DIAL"); dial != "" {
		alertClient, err := ari.New(ari.Config{
			BaseURL: env.Default("SAUTI_ARI_URL", "http://127.0.0.1:8088"), User: env.Default("SAUTI_ARI_USER", "sauti"),
			Password: password, App: env.Default("SAUTI_ARI_ALERT_APP", "sauti-alerts"), Timeout: 5 * time.Second,
		})
		if err != nil {
			return err
		}
		calls, err := ivr.NewAlertCalls(ivr.AlertCallsConfig{ARI: alertClient, Store: store, Opener: crypto.NewEnvelope(keys, rand.Reader),
			Dial: dial, CallerID: os.Getenv("SAUTI_ARI_CALLER_ID"), After: cfg.Dispatch.VoiceFallbackAfter.Duration, Copy: cfg.Responder,
			AskReasons: cfg.Dispatch.Mode == dispatch.ModeAmended, Log: log})
		if err != nil {
			return err
		}
		go func() { errc <- alertClient.Events(ctx, func(e ari.Event) { calls.Handle(ctx, e) }) }()
		go func() {
			_ = pg.RunAsLeader(ctx, pool, alertCallsLeaderKey, 5*time.Second, func(lctx context.Context) error {
				return calls.Run(lctx, 2*time.Second, func(err error) { log.Error("voice alerts failed", "err", err) })
			})
		}()
	} else {
		log.Warn("SAUTI_ARI_DIAL is not set: no voice alerts (SMS and the USSD menu only)")
	}
	log.Info("ivr-controller running", "env", senv, "config", cfg.Fingerprint, "protocol", cfg.ProtocolStatus,
		"triage", built.Triage.LogicVersion(), "gazetteer", built.Gazetteer.Version)

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, context.Canceled) {
			return err
		}
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_ = adminSrv.Shutdown(sctx)
	return nil
}
