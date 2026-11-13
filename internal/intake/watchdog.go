package intake

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
)

// ReasonDispatchStalled escalates an incident that dispatch has not acted on:
// T2 is recorded but, after the watchdog time, there is no alert and no
// dispatch escalation — dispatch-worker is down or stuck (review I8).
const ReasonDispatchStalled = "dispatch_stalled"

// DispatchWatchdogAfter is how long after T2 an untouched incident is
// escalated: longer than any healthy pass takes, shorter than the 3-minute
// red escalation (decision 2).
const DispatchWatchdogAfter = 2 * time.Minute

// StalledReader lists dispatched incidents with no alert and no dispatch
// escalation, T2 older than after (the operational store adapter).
type StalledReader interface {
	ListStalledDispatch(ctx context.Context, after time.Duration) ([]uuid.UUID, error)
}

// EventAppender appends one incident event (incident.Service).
type EventAppender interface {
	Append(ctx context.Context, p incident.AppendParams) (incident.Appended, error)
}

// Watchdog escalates stalled dispatch. It runs in intake-gw, a process apart
// from dispatch-worker, so a dead or stuck worker still reaches a person.
type Watchdog struct {
	r     StalledReader
	a     EventAppender
	after time.Duration
}

// NewWatchdog panics on a non-positive time (a configuration error).
func NewWatchdog(r StalledReader, a EventAppender, after time.Duration) *Watchdog {
	if after <= 0 {
		panic(fmt.Sprintf("intake: watchdog time %v must be positive", after))
	}
	return &Watchdog{r: r, a: a, after: after}
}

// RunOnce escalates every stalled incident once and returns how many.
func (w *Watchdog) RunOnce(ctx context.Context) (int, error) {
	ids, err := w.r.ListStalledDispatch(ctx, w.after)
	if err != nil {
		return 0, err
	}
	payload, err := json.Marshal(map[string]string{"reason": ReasonDispatchStalled})
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if _, err := w.a.Append(ctx, incident.AppendParams{IncidentID: id, Type: incident.EvEscalated, Payload: payload,
			ActorKind: incident.ActorSystem, IdemKey: incident.EscalationKey(ReasonDispatchStalled, "")}); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

// Run checks every interval until ctx ends (run under leadership).
func (w *Watchdog) Run(ctx context.Context, every time.Duration, onErr func(error)) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := w.RunOnce(ctx); err != nil && onErr != nil {
			onErr(err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}
