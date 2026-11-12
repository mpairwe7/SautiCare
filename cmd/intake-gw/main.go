// Command intake-gw answers USSD callbacks from both routes (Africa's
// Talking; the dedicated UCC code from M7). In M0 it opens the incident on
// the first screen — reference and database-set T1 — and replies in test
// mode; the Tool 1 state machine and triage arrive in M1.
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
//	SAUTI_DEFAULT_LANG        en | lg (until screen 1 exists in M1)
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
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/platform/africastalking"
	"sautihealth.com/sauti/internal/platform/devkeys"
	"sautihealth.com/sauti/internal/platform/env"
	"sautihealth.com/sauti/internal/platform/health"
	"sautihealth.com/sauti/internal/platform/logging"
	"sautihealth.com/sauti/internal/platform/metrics"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
)

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
	ids := crypto.Identifiers{Env: crypto.NewEnvelope(keys, rand.Reader)}
	incidents := incident.NewService(store, ids, rand.Reader)
	lang := env.Default("SAUTI_DEFAULT_LANG", "en")
	svc := intake.NewService(incidents, store, ids, cfg.Screens, lang)
	errorText := cfg.Screens.InternalError[lang]
	if errorText == "" {
		errorText = cfg.Screens.InternalError["en"]
	}
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
	log.Info("intake-gw listening", "env", senv, "config", cfg.Fingerprint, "protocol", cfg.ProtocolStatus)

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
