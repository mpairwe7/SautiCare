package intake

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
)

// Flags and escalation reasons written by the sweeper (Build Prompt §10.3).
const (
	FlagAbandoned                    = "abandoned"
	FlagAbandonedBeforeCategory      = "abandoned_before_category"
	ReasonAbandonedDangerYes         = "abandoned_after_danger_yes"
	ReasonAbandonedRedBeforeDispatch = "abandoned_red_before_dispatch"
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

// Sweeper flags abandoned Tool 1 sessions. Every abandoned session is kept
// and flagged; one abandoned before a category is flagged as such; and one
// abandoned before dispatch began after a yes at screen 2, or after it was
// classified red (a yes at screen 5 whatever screen 2 said), is escalated at
// once, so the coordinator calls back (Build Prompt §10.3; Guide §4). The
// escalation is delivered by the Escalate port (M2).
type Sweeper struct {
	r       IdleReader
	a       Appender
	idleFor time.Duration
}

// NewSweeper panics on a non-positive idle time (a configuration error that
// would flag every live session).
func NewSweeper(r IdleReader, a Appender, idleFor time.Duration) *Sweeper {
	if idleFor <= 0 {
		panic(fmt.Sprintf("intake: abandon_after %v must be positive", idleFor))
	}
	return &Sweeper{r: r, a: a, idleFor: idleFor}
}

func system(id uuid.UUID, typ incident.EventType, payload map[string]string, idem string) incident.AppendParams {
	b, _ := json.Marshal(payload) // a map of strings always encodes
	return incident.AppendParams{IncidentID: id, Type: typ, Payload: b, ActorKind: incident.ActorSystem, IdemKey: idem}
}

// RunOnce sweeps once and returns how many incidents it flagged. Each
// incident's flags and escalation are one batch: were the flag stored
// without the escalation, the incident would leave the idle list with the
// coordinator never alerted.
func (s *Sweeper) RunOnce(ctx context.Context) (int, error) {
	idle, err := s.r.ListIdle(ctx, s.idleFor)
	if err != nil {
		return 0, err
	}
	for _, in := range idle {
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
			return 0, err
		}
	}
	return len(idle), nil
}

// escalation is the reason an abandoned incident goes to a coordinator at
// once, or "" if it does not: not dispatched, and either a danger yes or a
// red classification.
func escalation(in Idle) string {
	switch {
	case in.Dispatched:
		return ""
	case in.Danger == "yes":
		return ReasonAbandonedDangerYes
	case in.Severity == incident.SevRed:
		return ReasonAbandonedRedBeforeDispatch
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
