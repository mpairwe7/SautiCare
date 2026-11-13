package intake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
)

// Flags written by the sweeper (Build Prompt §10.3).
const (
	FlagAbandoned               = "abandoned"
	FlagAbandonedBeforeCategory = "abandoned_before_category"
)

// Idle is an open incident whose Tool 1 is unfinished (or finished without
// T2), with no event for the idle time and no abandoned flag yet, or flagged
// abandoned but owing the escalation of a resumed and abandoned again call.
type Idle struct {
	ID         uuid.UUID
	Danger     string
	Category   string
	Severity   incident.Severity // "" until classified
	Dispatched bool              // T2 recorded: dispatch is already under way
	Ward       string            // "" until screen 6 is answered
}

// IdleReader lists idle incidents (the operational store adapter).
type IdleReader interface {
	ListIdle(ctx context.Context, idleFor time.Duration) ([]Idle, error)
}

// Appender appends incident events atomically: all or none
// (incident.Service).
type Appender interface {
	AppendBatch(ctx context.Context, ps []incident.AppendParams) ([]incident.Appended, error)
}

// CatchUpper records what an incident's stored answers imply but its log
// lacks (its classification, T2), exactly as a redial would, and reports
// whether dispatch is under way (Service.CatchUp).
type CatchUpper interface {
	CatchUp(ctx context.Context, id uuid.UUID) (dispatched bool, err error)
}

// Sweeper flags abandoned Tool 1 sessions. Every abandoned session is kept
// and flagged; one abandoned before a category is flagged as such. An
// undispatched one is first caught up (a recorded landmark gets T2 as a
// redial would); one still not dispatched is escalated at once, so the
// coordinator calls back (Build Prompt §10.3; Guide §4; ADR-0029 rule 3):
// after a yes at screen 2, after a red classification, or after a ward was
// given (F-04).
type Sweeper struct {
	r       IdleReader
	a       Appender
	c       CatchUpper
	idleFor time.Duration
}

// NewSweeper panics on a non-positive idle time (a configuration error that
// would flag every live session).
func NewSweeper(r IdleReader, a Appender, c CatchUpper, idleFor time.Duration) *Sweeper {
	if idleFor <= 0 {
		panic(fmt.Sprintf("intake: abandon_after %v must be positive", idleFor))
	}
	return &Sweeper{r: r, a: a, c: c, idleFor: idleFor}
}

func system(id uuid.UUID, typ incident.EventType, payload map[string]string, idem string) incident.AppendParams {
	b, _ := json.Marshal(payload) // a map of strings always encodes
	return incident.AppendParams{IncidentID: id, Type: typ, Payload: b, ActorKind: incident.ActorSystem, IdemKey: idem}
}

// RunOnce sweeps once and returns how many incidents it flagged. Each
// incident's flags and escalation are one batch: were the flag stored
// without the escalation, the incident would leave the idle list with the
// coordinator never alerted. An incident that fails is reported and skipped,
// so it never holds up the others behind it (F-10).
func (s *Sweeper) RunOnce(ctx context.Context) (int, error) {
	idle, err := s.r.ListIdle(ctx, s.idleFor)
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, in := range idle {
		if !in.Dispatched {
			// A failed catch-up is reported; the call is then escalated as
			// undispatched (the fail-safe direction).
			if d, err := s.c.CatchUp(ctx, in.ID); err != nil {
				errs = append(errs, fmt.Errorf("incident %s: catch-up: %w", in.ID, err))
			} else {
				in.Dispatched = d
			}
		}
		key := "sweep:" + in.ID.String() + ":"
		batch := []incident.AppendParams{system(in.ID, incident.EvFlagged, map[string]string{"flag": FlagAbandoned}, key+FlagAbandoned)}
		if in.Category == "" {
			batch = append(batch, system(in.ID, incident.EvFlagged, map[string]string{"flag": FlagAbandonedBeforeCategory},
				key+FlagAbandonedBeforeCategory))
		}
		if reason := escalation(in); reason != "" {
			batch = append(batch, system(in.ID, incident.EvEscalated, map[string]string{"reason": reason}, key+"escalate"))
		}
		if _, err := s.a.AppendBatch(ctx, batch); err != nil {
			errs = append(errs, fmt.Errorf("incident %s: %w", in.ID, err))
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// escalation is the reason an abandoned incident goes to a coordinator at
// once, or "" if it does not: not dispatched, and a danger yes, a red
// classification, or a ward given (dispatch never started for a place
// Sauti knows; whether to dispatch at ward level instead is the PI's
// choice, R-9).
func escalation(in Idle) string {
	switch {
	case in.Dispatched:
		return ""
	case in.Danger == "yes":
		return incident.ReasonAbandonedDangerYes
	case in.Severity == incident.SevRed:
		return incident.ReasonAbandonedRedBeforeDispatch
	case in.Ward != "":
		return incident.ReasonDispatchNotStarted
	}
	return ""
}

// Run sweeps every interval until ctx ends (run under leadership).
func (s *Sweeper) Run(ctx context.Context, every time.Duration, onErr func(error)) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := s.RunOnce(ctx); err != nil && onErr != nil {
			onErr(err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}
