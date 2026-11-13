package intake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
)

// ReasonDispatchStalled escalates an incident that dispatch has not acted on:
// T2 is recorded but, after the watchdog time, there is no alert and no
// dispatch escalation — dispatch-worker is down or stuck (review I8).
const ReasonDispatchStalled = incident.ReasonDispatchStalled

// DispatchWatchdogAfter is how long after T2 an untouched incident is
// escalated: longer than any healthy pass takes, shorter than the 3-minute
// red escalation (decision 2).
const DispatchWatchdogAfter = 2 * time.Minute

// StalledReader lists dispatched incidents with no alert and no dispatch
// escalation, T2 older than after, and dispatching incidents whose dispatch
// timer is overdue by after and unleased (the operational store adapter).
type StalledReader interface {
	ListStalledDispatch(ctx context.Context, after time.Duration) ([]uuid.UUID, error)
	ListOverdueDispatch(ctx context.Context, after time.Duration) ([]Overdue, error)
}

// Overdue is a dispatching incident whose dispatch timer was due at DueAt
// and has not been taken: dispatch-worker died or is stuck after T3 (F-05).
type Overdue struct {
	ID    uuid.UUID
	DueAt time.Time
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

// RunOnce escalates every stalled incident once (an overdue timer once per
// wake) and returns how many it escalated. An incident that fails is
// reported and skipped, so it never holds up the others (F-10).
func (w *Watchdog) RunOnce(ctx context.Context) (int, error) {
	ids, err := w.r.ListStalledDispatch(ctx, w.after)
	if err != nil {
		return 0, err
	}
	overdue, err := w.r.ListOverdueDispatch(ctx, w.after)
	if err != nil {
		return 0, err
	}
	payload, err := json.Marshal(map[string]string{"reason": ReasonDispatchStalled})
	if err != nil {
		return 0, err
	}
	keys := make(map[uuid.UUID]string, len(ids))
	order := slices.Clone(ids)
	for _, id := range ids {
		keys[id] = incident.EscalationKey(ReasonDispatchStalled, "")
	}
	for _, o := range overdue {
		if _, seen := keys[o.ID]; !seen {
			order = append(order, o.ID)
			keys[o.ID] = incident.EscalationKey(ReasonDispatchStalled, strconv.FormatInt(o.DueAt.Unix(), 10))
		}
	}
	n := 0
	var errs []error
	for _, id := range order {
		if _, err := w.a.Append(ctx, incident.AppendParams{IncidentID: id, Type: incident.EvEscalated, Payload: payload,
			ActorKind: incident.ActorSystem, IdemKey: keys[id]}); err != nil {
			errs = append(errs, fmt.Errorf("incident %s: %w", id, err))
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
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
