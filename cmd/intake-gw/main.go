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
//	SAUTI_AT_DELIVERY_TOKEN   secret path token of the SMS delivery-report callback (>= 24 characters; unset: not served)
//	SAUTI_USSD_SERVICE_CODES  comma-separated accepted service codes (Tool 1)
//	SAUTI_USSD_RESPONDER_CODES comma-separated service codes of the responder menu
//	SAUTI_DEVKEYS_DIR         development key directory (refused in production)
//	SAUTI_CONFIG_DIR          configuration directory (config/)
//	SAUTI_RULES_ROOT          directory holding triage-rules/ (default .)
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/dispatch"
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
	"sautihealth.com/sauti/internal/respond"
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
	// The responder menu (M2 Part B) answers on its own service code.
	responderCodes := env.List("SAUTI_USSD_RESPONDER_CODES")
	menu, err := respond.New(respond.Config{Store: store, Keys: crypto.Identifiers{Env: crypto.NewEnvelope(keys, rand.Reader)},
		Copy: cfg.Responder, Tool1: cfg.Tool1, Gazetteer: built.Gazetteer, AskReasons: cfg.Dispatch.Mode == dispatch.ModeAmended,
		ScreenLimit: cfg.Intake.DefaultScreenCharLimit})
	if err != nil {
		return err
	}
	codes, err := responderCodeSet(env.List("SAUTI_USSD_SERVICE_CODES"), responderCodes)
	if err != nil {
		return err
	}
	router := ussdRouter{tool1: svc, responders: menu, codes: codes}
	ussd, err := africastalking.NewUSSDHandler(africastalking.USSDConfig{
		Provider: "at", PathToken: token, ServiceCodes: append(env.List("SAUTI_USSD_SERVICE_CODES"), responderCodes...),
		ErrorText: errorText,
	}, router, log)
	if err != nil {
		return err
	}

	callbacks := http.NewServeMux()
	callbacks.Handle("/v1/ussd/", ussd)
	if tok := os.Getenv("SAUTI_AT_DELIVERY_TOKEN"); tok != "" {
		dr, err := africastalking.NewDeliveryReportHandler(africastalking.DeliveryReportConfig{Provider: "at", PathToken: tok},
			deliveryReports{store}, log)
		if err != nil {
			return err
		}
		callbacks.Handle("/v1/sms/delivery/", dr)
	}
	public := &http.Server{
		Addr: env.Default("SAUTI_HTTP_ADDR", ":8080"), Handler: callbacks,
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
	sweeper := intake.NewSweeper(store, built.Incidents, svc, cfg.Intake.AbandonAfter.Duration)
	// The dispatch watchdog (review I8) runs here, apart from dispatch-worker:
	// an incident dispatch has not touched two minutes after T2 is escalated.
	watchdog := intake.NewWatchdog(store, built.Incidents, intake.DispatchWatchdogAfter)
	// Tier 3 (ADR-0029 rule 8): the dead-man gauges, exported from here
	// because intake-gw survives an ivr-controller outage.
	gauges := newEscalationGauges(reg)
	go func() {
		_ = pg.RunAsLeader(ctx, pool, sweeperLeaderKey, 5*time.Second, func(lctx context.Context) error {
			go func() {
				_ = watchdog.Run(lctx, sweepEvery, func(err error) { log.Error("dispatch watchdog failed", "err", err) })
			}()
			go func() {
				t := time.NewTicker(sweepEvery)
				defer t.Stop()
				defer gauges.reset()
				for {
					if err := gauges.update(lctx, store); err != nil && lctx.Err() == nil {
						log.Error("escalation gauges not updated", "err", err)
					}
					select {
					case <-lctx.Done():
						return
					case <-t.C:
					}
				}
			}()
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

// escalationAges reads the dead-man signal (the operational store, op_intake).
type escalationAges interface {
	OldestUnacknowledgedEscalation(ctx context.Context) (seconds float64, incidents int, err error)
}

// escalationGauges are the dead-man signal: how many open incidents hold an
// escalation no coordinator has acknowledged, and the oldest one's age. An
// alert on the age pages the on-call engineer (P0 alerting).
// Only the leader exports them; leader is 1 while its last read succeeded,
// so the alert can tell a fresh value from a stale one (review R4).
type escalationGauges struct{ count, oldest, leader *metrics.Gauge }

func newEscalationGauges(reg *metrics.Registry) escalationGauges {
	return escalationGauges{
		count:  reg.Gauge("sauti_escalations_unacknowledged", "Open incidents with an escalation no coordinator has acknowledged."),
		oldest: reg.Gauge("sauti_escalation_oldest_unacknowledged_seconds", "Age of the oldest unacknowledged escalation (0 when none)."),
		leader: reg.Gauge("sauti_escalation_gauges_leader", "1 while this instance leads and exports the escalation gauges."),
	}
}

func (g escalationGauges) update(ctx context.Context, r escalationAges) error {
	secs, n, err := r.OldestUnacknowledgedEscalation(ctx)
	if err != nil {
		g.leader.Set(0) // nothing fresh is exported until a read succeeds
		return err
	}
	g.count.Set(float64(n))
	g.oldest.Set(secs)
	g.leader.Set(1)
	return nil
}

// reset clears the gauges when the leader term ends.
func (g escalationGauges) reset() {
	g.count.Set(0)
	g.oldest.Set(0)
	g.leader.Set(0)
}

// deliveryRecorder stores delivery reports (the operational store, op_intake).
type deliveryRecorder interface {
	RecordDeliveryReport(ctx context.Context, r opstore.DeliveryReport) error
}

// deliveryReports bridges the provider's delivery-report callback to the
// store: codes only; an unknown message is acknowledged.
type deliveryReports struct{ store deliveryRecorder }

func (d deliveryReports) RecordDeliveryReport(ctx context.Context, r africastalking.DeliveryReport) error {
	err := d.store.RecordDeliveryReport(ctx, opstore.DeliveryReport{ProviderRef: r.MessageID, Status: r.Status,
		FailureReason: r.FailureReason, Final: r.Final})
	if errors.Is(err, opstore.ErrUnknownDelivery) {
		return africastalking.ErrUnknownMessage
	}
	return err
}

// ussdRouter sends callbacks on a responder service code to the responder
// menu and every other accepted code to Tool 1.
type ussdRouter struct {
	tool1, responders africastalking.USSDPort
	codes             map[string]bool
}

func (r ussdRouter) HandleUSSD(ctx context.Context, req intake.USSDRequest) (intake.Screen, error) {
	if r.codes[req.ServiceCode] {
		return r.responders.HandleUSSD(ctx, req)
	}
	return r.tool1.HandleUSSD(ctx, req)
}

// responderCodeSet is the set of responder-menu service codes. A code that is
// also a Tool 1 code is refused: the router would send those callers to the
// responder menu, and nobody could report an incident on it.
func responderCodeSet(tool1, responders []string) (map[string]bool, error) {
	set := map[string]bool{}
	for _, c := range responders {
		if slices.Contains(tool1, c) {
			return nil, fmt.Errorf("USSD service code %s is in both SAUTI_USSD_SERVICE_CODES and SAUTI_USSD_RESPONDER_CODES", c)
		}
		set[c] = true
	}
	return set, nil
}
