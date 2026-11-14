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
//	SAUTI_COORDINATOR_DIAL the coordinator line rung about every escalation (endpoint; placeholder until P0); unset: no escalation calls
//	SAUTI_COORDINATOR_LINES calls the coordinator line takes at once (default 1): never rung twice at once
//	SAUTI_ARI_CALLER_ID also the number both parties of the masked bridge see (the bridge needs it and SAUTI_ARI_DIAL)
//	SAUTI_OUTBOUND_NO_INPUT an answered forwarding or coordinator call with no key for this long is hung up (default 2m; 30s-10m)
//	SAUTI_ADMIN_ADDR      health and metrics (default 127.0.0.1:9101)
//	SAUTI_DEVKEYS_DIR     development key directory (refused in production)
//	SAUTI_CONFIG_DIR      configuration directory (config/)
//	SAUTI_RULES_ROOT      directory holding triage-rules/ (default .)
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
	"strconv"
	"sync"
	"sync/atomic"
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

// alertCallsLeaderKey is the advisory-lock key of the outbound-call leader
// (outbox-relay 7347002, sweeper 7347003, dispatch-worker 7347004).
const alertCallsLeaderKey = 7347005

// readiness lists what the voice line needs to serve a call: the
// operational store and the ARI event stream (review I4).
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
	// A handed-over call's caller ID is masked by the fallback dialplan
	// (SAUTI_CALLER_ID in extensions.conf), never by an ARI request (rule 5).
	reg := metrics.NewRegistry()
	// A hand-over escalation still refused after its retries is lost; the
	// runbook recovers it with sautictl incident escalate-handover.
	lost := reg.Counter("sauti_ivr_handover_escalation_lost_total", "Hand-over escalations still refused after 10 minutes of retries.")
	ctrl := ivr.New(client, built.Service, log, ivr.Options{NoInput: 15 * time.Second, MaxReprompts: 2, MaxNoMatch: 3,
		OpTimeout: 3 * time.Second, PromptMax: 30 * time.Second, App: env.Default("SAUTI_ARI_APP", "sauti-ivr"),
		OnEscalationLost: func() { lost.Add(1) }})

	admin := http.NewServeMux()
	admin.Handle("/livez", health.Handler(map[string]health.Check{}, nil))
	// The outbound leader's stream joins readiness (a no-op on instances that
	// do not lead); the map is complete before the admin server starts.
	ready := readiness(pool.Ping, client)
	outbound := &outboundStatus{}
	ready["outbound_events"] = outboundReady(outbound)
	admin.Handle("/readyz", health.Handler(nil, ready))
	admin.Handle("/metrics", reg.Handler())
	adminSrv := &http.Server{Addr: env.Default("SAUTI_ADMIN_ADDR", "127.0.0.1:9101"), Handler: admin, ReadHeaderTimeout: 2 * time.Second}

	errc := make(chan error, 3)
	go func() { errc <- adminSrv.ListenAndServe() }()
	ctrlDone := make(chan struct{})
	go func() { defer close(ctrlDone); errc <- ctrl.Run(ctx, client) }()

	// Outbound calls (M2 Part B; scenarios 7–9, 11): an alert still pending
	// after voice_fallback_after rings the responder; forwarded health centres
	// are called (both need SAUTI_ARI_DIAL); every escalation rings the
	// coordinator line (SAUTI_COORDINATOR_DIAL), whichever else is set. One
	// leader places the calls and alone holds the outbound application's
	// websocket (outboundTerm).
	dial, line := os.Getenv("SAUTI_ARI_DIAL"), os.Getenv("SAUTI_COORDINATOR_DIAL")
	if dial != "" || line != "" {
		alertClient, err := ari.New(ari.Config{
			BaseURL: env.Default("SAUTI_ARI_URL", "http://127.0.0.1:8088"), User: env.Default("SAUTI_ARI_USER", "sauti"),
			Password: password, App: env.Default("SAUTI_ARI_ALERT_APP", "sauti-alerts"), Timeout: 5 * time.Second,
		})
		if err != nil {
			return err
		}
		callerID := os.Getenv("SAUTI_ARI_CALLER_ID")
		noInput, err := noInputSetting(os.Getenv("SAUTI_OUTBOUND_NO_INPUT"))
		if err != nil {
			return err
		}
		onErr := func(err error) { log.Error("outbound calls failed", "err", err) }
		out := ivr.Outbound{ARI: alertClient}
		var loops []func(context.Context) error
		if dial != "" {
			calls, err := ivr.NewAlertCalls(ivr.AlertCallsConfig{ARI: alertClient, Store: store, Opener: crypto.NewEnvelope(keys, rand.Reader),
				Dial: dial, CallerID: callerID, After: cfg.Dispatch.VoiceFallbackAfter.Duration, Copy: cfg.Responder,
				AskReasons: cfg.Dispatch.Mode == dispatch.ModeAmended, Gazetteer: built.Gazetteer, Log: log})
			if err != nil {
				return err
			}
			// Forwarded health centres are called (press 1 to acknowledge).
			forwards, err := ivr.NewForwardCalls(ivr.ForwardCallsConfig{ARI: alertClient, Store: store, Dial: dial, CallerID: callerID,
				Gazetteer: built.Gazetteer, Log: log, NoInput: noInput})
			if err != nil {
				return err
			}
			out.Alerts, out.Forwards = calls, forwards
			// The masked bridge (Build Prompt §10.5): responders and callers
			// reach each other through Sauti's number; nothing is recorded.
			if callerID != "" {
				if out.Bridge, err = ivr.NewBridge(ivr.BridgeConfig{ARI: alertClient, Store: store,
					Keys: crypto.Identifiers{Env: crypto.NewEnvelope(keys, rand.Reader)}, Opener: crypto.NewEnvelope(keys, rand.Reader),
					Dial: dial, CallerID: callerID, Log: log}); err != nil {
					return err
				}
			} else {
				log.Warn("SAUTI_ARI_CALLER_ID is not set: no masked bridge (the other party would see no Sauti number)")
			}
			loops = append(loops,
				func(lctx context.Context) error { return calls.Run(lctx, 2*time.Second, onErr) },
				func(lctx context.Context) error { return forwards.Run(lctx, 2*time.Second, onErr) })
		} else {
			log.Warn("SAUTI_ARI_DIAL is not set: no voice alerts or forwarding calls (SMS and the USSD menu only)")
		}
		// Every escalation rings the coordinator line until acknowledged
		// (rule 3.14).
		if line != "" {
			lines := 1
			if v := os.Getenv("SAUTI_COORDINATOR_LINES"); v != "" {
				if lines, err = strconv.Atoi(v); err != nil || lines < 1 || lines > 50 {
					return fmt.Errorf("SAUTI_COORDINATOR_LINES must be 1-50, got %q", v)
				}
			}
			// Tier 2 (ADR-0029 rule 8): the backup line rings as well after
			// escalation.backup_after_calls unanswered rings. UX-7 refuses
			// production without it.
			backup := os.Getenv("SAUTI_COORDINATOR_BACKUP_DIAL")
			if backup == "" {
				log.Warn("SAUTI_COORDINATOR_BACKUP_DIAL is not set: an unanswered escalation rings only the coordinator line")
			}
			if out.Coordinator, err = ivr.NewCoordinatorCalls(ivr.CoordinatorCallsConfig{ARI: alertClient, Store: store, Endpoint: line,
				CallerID: callerID, Lines: lines, Log: log, NoInput: noInput,
				BackupEndpoint: backup, BackupAfterCalls: cfg.Dispatch.Escalation.BackupAfterCalls}); err != nil {
				return err
			}
			loops = append(loops, func(lctx context.Context) error { return out.Coordinator.Run(lctx, 5*time.Second, onErr) })
		} else {
			log.Warn("SAUTI_COORDINATOR_DIAL is not set: escalations are not rung to coordinators")
		}
		outbound.connected = alertClient.EventsConnected // before the leader runs: its atomic store publishes it
		go func() {
			_ = pg.RunAsLeader(ctx, pool, alertCallsLeaderKey, 5*time.Second, outboundTerm(alertClient.Events, outbound, out, loops...))
		}()
	} else {
		log.Warn("SAUTI_ARI_DIAL and SAUTI_COORDINATOR_DIAL are not set: no outbound calls")
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
	// Live calls are handed to a coordinator before the process exits.
	select {
	case <-ctrlDone:
	case <-sctx.Done():
		// Escalations still being retried are reported lost before the exit
		// (the runbook recovers them from the log's call IDs).
		ctrl.FlushLost()
	}
	_ = adminSrv.Shutdown(sctx)
	return nil
}

// noInputSetting reads SAUTI_OUTBOUND_NO_INPUT: how long an answered
// forwarding or coordinator call may go without a key before Sauti hangs up,
// freeing the line (unset: ivr.DefaultNoInput). Bounded so a prompt is
// always heard in full and a line is never held for long.
func noInputSetting(v string) (time.Duration, error) {
	if v == "" {
		return ivr.DefaultNoInput, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 30*time.Second || d > 10*time.Minute {
		return 0, fmt.Errorf("SAUTI_OUTBOUND_NO_INPUT must be a duration from 30s to 10m, got %q", v)
	}
	return d, nil
}

// outboundPoll is how often the outbound leader checks its event stream.
var outboundPoll = 500 * time.Millisecond

// outboundStatus is whether this instance holds the outbound leadership, and
// its event stream's state (set before the leader runs).
type outboundStatus struct {
	leading   atomic.Bool
	connected func() bool
}

// errOutboundEvents fails readiness on the outbound leader while its event
// stream is down: no outbound call is placed until it reconnects.
var errOutboundEvents = errors.New("outbound ari event stream disconnected on the leader")

// outboundReady is the outbound leader's readiness: its event stream is up
// (instances that do not lead have nothing to check).
func outboundReady(st *outboundStatus) health.Check {
	return func(context.Context) error {
		if st.leading.Load() && !st.connected() {
			return errOutboundEvents
		}
		return nil
	}
}

// outboundTerm is the outbound side's leadership term. The leader alone takes
// the outbound application's events, because Asterisk gives an application one
// websocket and a newer connection takes it over (ApplicationReplaced): the
// calls the leader places and the events of their answers then meet in one
// process, which the coordinator line's busy tracking relies on. Calls are
// placed only while that stream is connected — an answer Sauti cannot hear
// would be a one-shot attempt wasted — so the loops stop when it drops and
// start again when it is back. The subscription ends with the term, and the
// term returns only once it has (a later term in this process must not run
// beside a websocket still closing).
func outboundTerm(events func(context.Context, func(ari.Event)) error, st *outboundStatus, out ivr.Outbound,
	loops ...func(context.Context) error) func(context.Context) error {
	connected := st.connected
	return func(lctx context.Context) error {
		st.leading.Store(true)
		defer st.leading.Store(false)
		// On every exit from the term, once the subscription and the loops
		// have stopped (defers run last-in first-out), the calls' timers
		// stop: the next leader owns the calls' timing.
		defer func() {
			if out.Alerts != nil {
				out.Alerts.DisarmAll()
			}
		}()
		var sub sync.WaitGroup
		defer sub.Wait()
		sub.Go(func() { _ = events(lctx, func(e ari.Event) { out.Handle(lctx, e) }) })
		t := time.NewTicker(outboundPoll)
		defer t.Stop()
		for {
			for !connected() {
				select {
				case <-lctx.Done():
					return lctx.Err()
				case <-t.C:
				}
			}
			rctx, stop := context.WithCancel(lctx)
			var wg sync.WaitGroup
			for _, l := range loops {
				wg.Go(func() { _ = l(rctx) })
			}
			for connected() && lctx.Err() == nil {
				select {
				case <-lctx.Done():
				case <-t.C:
				}
			}
			stop()
			wg.Wait()
			if err := lctx.Err(); err != nil {
				return err
			}
		}
	}
}
