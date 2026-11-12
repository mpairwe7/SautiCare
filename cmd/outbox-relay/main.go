// Command outbox-relay is the only process with audit-store credentials. It
// relays the operational outbox into the hash-chained audit ledger, at least
// once and in order, and signs a checkpoint of the chain head on a fixed
// interval (spec §6.3, ADR-0004, ADR-0005). Exactly one instance is active
// at a time (advisory-lock leadership); standbys stay live and take over.
//
// Environment:
//
//	SAUTI_ENV          dev | test | production
//	SAUTI_OP_DSN       operational store, role op_relay
//	SAUTI_AUDIT_DSN    audit store, role audit_appender
//	SAUTI_DEVKEYS_DIR  development key directory (refused in production)
//	SAUTI_WITNESS_DIR  write-once checkpoint witness directory
//	SAUTI_CONFIG_DIR   configuration directory (config/)
//	SAUTI_HTTP_ADDR    health and metrics listener (default 127.0.0.1:9101)
//	SAUTI_ORIGIN       ledger origin name (default sauti-dispatch)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"sautihealth.com/sauti/internal/audit"
	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/platform/devkeys"
	"sautihealth.com/sauti/internal/platform/env"
	"sautihealth.com/sauti/internal/platform/health"
	"sautihealth.com/sauti/internal/platform/logging"
	"sautihealth.com/sauti/internal/platform/metrics"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/auditstore"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
)

const leaderKey = 7347002

type relayMetrics struct {
	relayed  *metrics.Counter
	lag      *metrics.Gauge
	poison   *metrics.Counter
	checkpts *metrics.Counter
}

func (m relayMetrics) Relayed(n int)       { m.relayed.Add(float64(n)) }
func (m relayMetrics) Lag(d time.Duration) { m.lag.Set(d.Seconds()) }
func (m relayMetrics) Poison()             { m.poison.Add(1) }

func main() {
	log := slog.New(logging.NewHandler(os.Stderr, slog.LevelInfo))
	if err := run(log); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("outbox-relay stopped", "err", err)
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
		return errors.New("outbox-relay: production key provider (OpenBao) arrives in P0; refusing to start")
	}
	opDSN, err := env.Required("SAUTI_OP_DSN")
	if err != nil {
		return err
	}
	auditDSN, err := env.Required("SAUTI_AUDIT_DSN")
	if err != nil {
		return err
	}
	keys, err := devkeys.Open(env.Default("SAUTI_DEVKEYS_DIR", ".devkeys"), senv)
	if err != nil {
		return err
	}
	opPool, err := pg.Connect(ctx, opDSN, "outbox-relay", 4)
	if err != nil {
		return err
	}
	defer opPool.Close()
	auditPool, err := pg.Connect(ctx, auditDSN, "outbox-relay", 4)
	if err != nil {
		return err
	}
	defer auditPool.Close()

	reg := metrics.NewRegistry()
	m := relayMetrics{
		relayed:  reg.Counter("sauti_outbox_relayed_total", "Outbox rows delivered to the audit ledger."),
		lag:      reg.Gauge("sauti_outbox_lag_seconds", "Age of the oldest undelivered audit outbox row."),
		poison:   reg.Counter("sauti_outbox_poison_total", "Outbox rows that violated the audit payload contract."),
		checkpts: reg.Counter("sauti_audit_checkpoints_total", "Signed audit checkpoints written."),
	}
	ledger := auditstore.New(auditPool)
	relay := audit.NewRelay(opstore.New(opPool), ledger, log, m, cfg.Platform.OutboxBatchSize)
	cp := audit.NewCheckpointer(ledger, keys.Signer(), devkeys.NewFSWitness(env.Default("SAUTI_WITNESS_DIR", "var/witness")),
		env.Default("SAUTI_ORIGIN", "sauti-dispatch"), time.Now)

	mux := http.NewServeMux()
	mux.Handle("/livez", health.Handler(map[string]health.Check{}, nil))
	// Readiness: the operational store only. An audit outage must not take
	// the relay out of rotation; the outbox simply grows (rule 3.14).
	mux.Handle("/readyz", health.Handler(nil, map[string]health.Check{"operational_db": opPool.Ping}))
	mux.Handle("/metrics", reg.Handler())
	srv := &http.Server{Addr: env.Default("SAUTI_HTTP_ADDR", "127.0.0.1:9101"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("health listener", "err", err)
			stop()
		}
	}()
	defer func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	log.Info("outbox-relay waiting for leadership", "env", senv, "config", cfg.Fingerprint)
	return pg.RunAsLeader(ctx, opPool, leaderKey, 5*time.Second, func(lctx context.Context) error {
		log.Info("outbox-relay is the active leader")
		go func() {
			t := time.NewTicker(cfg.Platform.CheckpointInterval.Duration)
			defer t.Stop()
			for {
				select {
				case <-lctx.Done():
					return
				case <-t.C:
					if c, err := cp.Checkpoint(lctx); err != nil {
						log.Error("audit checkpoint failed", "err", err)
					} else {
						m.checkpts.Add(1)
						log.Info("audit checkpoint signed", "seq", c.Seq)
					}
				}
			}
		}()
		err := relay.Run(lctx, cfg.Platform.RelayPollInterval.Duration)
		if errors.Is(err, audit.ErrPoisonRow) {
			return fmt.Errorf("outbox-relay halted on a poison row; see docs/runbooks/outbox-poison-row.md: %w", err)
		}
		return err
	})
}
