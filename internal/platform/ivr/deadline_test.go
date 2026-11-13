package ivr

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"sautihealth.com/sauti/internal/dispatch"
)

// The hanging stores never answer their first query until its context ends
// (a database that stopped responding).
type (
	hangAlerts struct{ AlertStore }
	hangFwd    struct{ ForwardStore }
	hangCoord  struct{ CoordinatorStore }
)

func hang(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }

func (hangAlerts) DueVoiceAlerts(ctx context.Context, _ time.Duration, _ int) ([]dispatch.VoiceAlert, error) {
	return nil, hang(ctx)
}
func (hangFwd) DueForwardCalls(ctx context.Context, _ int) ([]dispatch.ForwardCall, error) {
	return nil, hang(ctx)
}
func (hangCoord) ClaimCoordinatorCalls(ctx context.Context, _ time.Duration, _ int) ([]dispatch.CoordinatorCall, error) {
	return nil, hang(ctx)
}

// CodeRabbit PR #11 (4558030067, 4558030082): every worker store call has a
// deadline of its own — a database that hangs costs one operation timeout,
// not the leader's term.
func TestOutboundWorkersBoundEachStoreCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := &alertARI{}
		alerts := &AlertCalls{c: AlertCallsConfig{ARI: a, Store: hangAlerts{}, OpTimeout: 3 * time.Second}}
		fwd := &ForwardCalls{c: ForwardCallsConfig{ARI: a, Store: hangFwd{}, OpTimeout: 3 * time.Second}}
		coord := &CoordinatorCalls{c: CoordinatorCallsConfig{ARI: a, Store: hangCoord{}, OpTimeout: 3 * time.Second, Lines: 1, Now: time.Now}, busy: map[string]time.Time{}}
		for name, run := range map[string]func(context.Context) (int, error){"alerts": alerts.RunOnce, "forwards": fwd.RunOnce, "coordinator": coord.RunOnce} {
			start := time.Now()
			_, err := run(t.Context()) // the leader's context has no deadline
			if took := time.Since(start); took != 3*time.Second || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%s: returned after %v with %v, want the 3 s operation timeout", name, took, err)
			}
		}
	})
}
