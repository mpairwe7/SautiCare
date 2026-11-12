package intake

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
)

// Flags and escalation reason written by the sweeper (Build Prompt §10.3).
const (
	FlagAbandoned               = "abandoned"
	FlagAbandonedBeforeCategory = "abandoned_before_category"
	ReasonAbandonedDangerYes    = "abandoned_after_danger_yes"
)

// Idle is an unfinished, open incident with no event for the idle time and
// no abandoned flag yet.
type Idle struct {
	ID         uuid.UUID
	Danger     string
	Category   string
	Dispatched bool // T2 recorded: dispatch is already under way
}

// IdleReader lists idle incidents (the operational store adapter).
type IdleReader interface {
	ListIdle(ctx context.Context, idleFor time.Duration) ([]Idle, error)
}

// Appender appends incident events (incident.Service).
type Appender interface {
	Append(ctx context.Context, p incident.AppendParams) (incident.Appended, error)
}

// Sweeper flags abandoned Tool 1 sessions. Every abandoned session is kept
// and flagged; one abandoned before a category is flagged as such; and one
// abandoned after a yes at screen 2 before dispatch began is escalated at
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

func (s *Sweeper) system(ctx context.Context, id uuid.UUID, typ incident.EventType, payload map[string]string, idem string) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = s.a.Append(ctx, incident.AppendParams{IncidentID: id, Type: typ, Payload: b, ActorKind: incident.ActorSystem, IdemKey: idem})
	return err
}

// RunOnce sweeps once and returns how many incidents it flagged.
func (s *Sweeper) RunOnce(ctx context.Context) (int, error) {
	idle, err := s.r.ListIdle(ctx, s.idleFor)
	if err != nil {
		return 0, err
	}
	for _, in := range idle {
		key := "sweep:" + in.ID.String() + ":"
		if err := s.system(ctx, in.ID, incident.EvFlagged, map[string]string{"flag": FlagAbandoned}, key+FlagAbandoned); err != nil {
			return 0, err
		}
		if in.Category == "" {
			if err := s.system(ctx, in.ID, incident.EvFlagged, map[string]string{"flag": FlagAbandonedBeforeCategory},
				key+FlagAbandonedBeforeCategory); err != nil {
				return 0, err
			}
		}
		if in.Danger == "yes" && !in.Dispatched {
			if err := s.system(ctx, in.ID, incident.EvEscalated, map[string]string{"reason": ReasonAbandonedDangerYes},
				key+"escalate"); err != nil {
				return 0, err
			}
		}
	}
	return len(idle), nil
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
