// Command intake-gw answers USSD callbacks from both routes (Africa's
// Talking; the dedicated UCC code from M7) and runs Data Collection Tool 1:
// the incident and its reference exist from the first callback, answers are
// persisted as they are given, severity comes from the signed triage table,
// the location is resolved to a 500 m zone at T2, and a redial within the
// resume window continues the same incident. It refuses to start if the
// triage bundle, gazetteer or any screen fails its checks.
//
// Environment:
//
//	SAUTI_ENV                 dev | test | production
//	SAUTI_OP_DSN              operational store, role op_intake
//	SAUTI_HTTP_ADDR           callback listener (default :8080, behind HAProxy)
//	SAUTI_ADMIN_ADDR          health and metrics (default 127.0.0.1:9100)
//	SAUTI_AT_PATH_TOKEN       secret callback path token (>= 24 characters)
//	SAUTI_USSD_SERVICE_CODES  comma-separated accepted service codes
//	SAUTI_DEVKEYS_DIR         development key directory (refused in production)
//	SAUTI_CONFIG_DIR          configuration directory (config/)
//	SAUTI_RULES_ROOT          directory holding triage-rules/ (default .)
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
	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/platform/africastalking"
	"sautihealth.com/sauti/internal/platform/devkeys"
	"sautihealth.com/sauti/internal/platform/env"
	"sautihealth.com/sauti/internal/platform/health"
	"sautihealth.com/sauti/internal/platform/logging"
	"sautihealth.com/sauti/internal/platform/metrics"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/platform/wiring"
)

// sweeperLeaderKey is the advisory-lock key for the abandonment sweeper
// (outbox-relay holds 7347002).
const sweeperLeaderKey = 7347003

// sweepEvery is how often the leader looks for abandoned sessions.
const sweepEvery = 15 * time.Second

func main() {
	log := slog.New(logging.NewHandler(os.Stderr, slog.LevelInfo))
	if err := run(log); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("intake-gw stopped", "err", err)
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
		return errors.New("intake-gw: production key provider (OpenBao) arrives in P0; refusing to start")
	}
	dsn, err := env.Required("SAUTI_OP_DSN")
	if err != nil {
		return err
	}
	token, err := env.Required("SAUTI_AT_PATH_TOKEN")
	if err != nil {
		return err
	}
	keys, err := devkeys.Open(env.Default("SAUTI_DEVKEYS_DIR", ".devkeys"), senv)
	if err != nil {
		return err
	}
	pool, err := pg.Connect(ctx, dsn, "intake-gw", 20)
	if err != nil {
		return err
	}
	defer pool.Close()

	store := opstore.New(pool)
	doc, sig, err := wiring.ReadTriageBundle(env.Default("SAUTI_RULES_ROOT", "."), cfg.Triage.RulesFile)
	if err != nil {
		return err
	}
	// Verifies the signed triage bundle, the gazetteer in force, every screen
	// against the operator limits, and the catalogue/triage domain match.
	built, err := wiring.BuildIntake(ctx, wiring.IntakeOptions{Config: cfg, Store: store, Keys: keys, Rand: rand.Reader,
		TriageDoc: doc, TriageSig: sig, Production: senv == "production"})
	if err != nil {
		return err
	}
	svc := built.Service
	// The error screen comes before the caller's language is known.
	errorText := cfg.Screens.InternalError["en"]
	ussd, err := africastalking.NewUSSDHandler(africastalking.USSDConfig{
		Provider: "at", PathToken: token, ServiceCodes: env.List("SAUTI_USSD_SERVICE_CODES"),
		ErrorText: errorText,
	}, svc, log)
	if err != nil {
		return err
	}

	public := &http.Server{
		Addr: env.Default("SAUTI_HTTP_ADDR", ":8080"), Handler: ussd,
		ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second,
	}
	reg := metrics.NewRegistry()
	admin := http.NewServeMux()
	admin.Handle("/livez", health.Handler(map[string]health.Check{}, nil))
	admin.Handle("/readyz", health.Handler(nil, map[string]health.Check{"operational_db": pool.Ping}))
	admin.Handle("/metrics", reg.Handler())
	adminSrv := &http.Server{Addr: env.Default("SAUTI_ADMIN_ADDR", "127.0.0.1:9100"), Handler: admin, ReadHeaderTimeout: 2 * time.Second}

	errc := make(chan error, 2)
	go func() { errc <- public.ListenAndServe() }()
	go func() { errc <- adminSrv.ListenAndServe() }()
	// One abandonment sweeper across all intake-gw instances (leadership by
	// advisory lock); it never blocks the request path.
	sweeper := intake.NewSweeper(store, built.Incidents, cfg.Intake.AbandonAfter.Duration)
	go func() {
		_ = pg.RunAsLeader(ctx, pool, sweeperLeaderKey, 5*time.Second, func(lctx context.Context) error {
			return sweeper.Run(lctx, sweepEvery, func(err error) { log.Error("abandonment sweep failed", "err", err) })
		})
	}()
	log.Info("intake-gw listening", "env", senv, "config", cfg.Fingerprint, "protocol", cfg.ProtocolStatus,
		"triage", built.Triage.LogicVersion(), "gazetteer", built.Gazetteer.Version)

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_ = public.Shutdown(sctx)
	_ = adminSrv.Shutdown(sctx)
	return nil
}
