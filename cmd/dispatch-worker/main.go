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
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/dispatch"
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
	deliver, err := deliverer(senv, log)
	if err != nil {
		return err
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
	built, err := wiring.BuildDispatch(ctx, wiring.DispatchOptions{Config: cfg, Store: store, Deliverer: deliver,
		Suspension: store})
	if err != nil {
		return err
	}

	reg := metrics.NewRegistry()
	admin := http.NewServeMux()
	admin.Handle("/livez", health.Handler(map[string]health.Check{}, nil))
	admin.Handle("/readyz", health.Handler(nil, map[string]health.Check{"operational_db": pool.Ping}))
	admin.Handle("/metrics", reg.Handler())
	adminSrv := &http.Server{Addr: env.Default("SAUTI_ADMIN_ADDR", "127.0.0.1:9102"), Handler: admin, ReadHeaderTimeout: 2 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- adminSrv.ListenAndServe() }()

	log.Info("dispatch-worker starting", "env", senv, "config", cfg.Fingerprint, "mode", built.Params.Mode,
		"dispatch_logic", built.LogicVersion, "gazetteer", built.Gazetteer.Version)
	onErr := func(id uuid.UUID, err error) {
		if errors.Is(err, dispatch.ErrStoppingRuleBreached) {
			// O6: an alarm for the coordinator and the PI; resuming is a
			// logged staff decision (sautictl dispatch resume, or the console).
			log.Error("DISPATCH SUSPENDED by the stopping rule; responders are not being alerted", "err", err)
			return
		}
		log.Error("dispatch pass failed", "incident", id, "err", err)
	}
	go func() {
		errc <- pg.RunAsLeader(ctx, pool, leaderKey, 5*time.Second, func(lctx context.Context) error {
			return built.Engine.Run(lctx, poll, batch, lease, onErr)
		})
	}()

	select {
	case <-ctx.Done():
	case err := <-errc:
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, context.Canceled) {
			return err
		}
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = adminSrv.Shutdown(sctx)
	return nil
}

// deliverer picks how responders are reached. The channel adapters arrive
// in Part B; until then development and tests simulate delivery (logging
// identifiers only), and production refuses to start.
func deliverer(senv string, log *slog.Logger) (dispatch.Deliverer, error) {
	if senv == "production" {
		return nil, errors.New("dispatch-worker: no responder channel yet (SMS adapter arrives in M2 Part B); refusing to start")
	}
	return simDeliverer{log: log}, nil
}

// simDeliverer logs each delivery (identifiers only, never a number).
type simDeliverer struct{ log *slog.Logger }

func (s simDeliverer) Deliver(_ context.Context, d dispatch.Delivery) (string, error) {
	s.log.Info("simulated delivery", "purpose", d.Purpose, "channel", d.Channel, "incident", d.IncidentID,
		"alert", d.AlertID, "responder", d.ResponderID, "role", d.Role)
	return "sim-" + d.AttemptID.String()[:8], nil
}
