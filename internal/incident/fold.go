package incident

import (
	"encoding/json"
	"fmt"
	"slices"
	"uuid"
)

// Apply returns the projection after event e. It is pure: the same inputs
// always give the same output. It is the logic of record for the lifecycle;
// the SQL write path applies the same rules and `sautictl verify-projections`
// fails if the database projection ever drifts from Fold of its events.
func Apply(p Projection, e Event) (Projection, error) {
	if p.Version == 0 {
		if e.Type != EvSessionStarted {
			return p, fmt.Errorf("%w: first event must be %s, got %s", ErrIllegalTransition, EvSessionStarted, e.Type)
		}
		return applyStart(e)
	}
	if e.IncidentID != p.ID {
		return p, fmt.Errorf("%w: event %s belongs to another incident", ErrIllegalTransition, e.ID)
	}
	if e.Seq != p.Version+1 {
		return p, fmt.Errorf("%w: seq %d after version %d", ErrSequenceGap, e.Seq, p.Version)
	}
	tr, ok := lookup(p.State, e.Type)
	if !ok {
		return p, fmt.Errorf("%w: %s in state %s", ErrIllegalTransition, e.Type, p.State)
	}

	next := p
	next.Flags = slices.Clone(p.Flags)
	switch e.Type {
	case EvClassified:
		var pl struct {
			Severity Severity `json:"severity"`
		}
		if err := json.Unmarshal(e.Payload, &pl); err != nil || pl.Severity.Rank() == 0 {
			return p, fmt.Errorf("%w: classification needs a valid severity", ErrIllegalTransition)
		}
		if pl.Severity.Rank() < p.Severity.Rank() {
			return p, fmt.Errorf("%w: %s -> %s", ErrSeverityLowered, p.Severity, pl.Severity)
		}
		next.Severity = pl.Severity
		next.LogicVersion = e.LogicVersion
	case EvFlagged:
		var pl struct {
			Flag string `json:"flag"`
		}
		if err := json.Unmarshal(e.Payload, &pl); err != nil || pl.Flag == "" {
			return p, fmt.Errorf("%w: flag event needs a flag", ErrIllegalTransition)
		}
		if !slices.Contains(next.Flags, pl.Flag) {
			next.Flags = append(next.Flags, pl.Flag)
		}
	case EvEscalated:
		next.Escalated = true
	case EvMerged:
		var pl struct {
			Into string `json:"into"`
		}
		if err := json.Unmarshal(e.Payload, &pl); err != nil {
			return p, fmt.Errorf("%w: merge needs a target", ErrIllegalTransition)
		}
		into, err := uuid.Parse(pl.Into)
		if err != nil {
			return p, fmt.Errorf("%w: merge target: %w", ErrIllegalTransition, err)
		}
		if into == p.ID {
			return p, fmt.Errorf("%w: an incident cannot merge into itself", ErrIllegalTransition)
		}
		next.MergedInto = &into
	}
	if i, ok := tIndex[e.Type]; ok && next.T[i] == nil {
		at := e.RecordedAt
		next.T[i] = &at
	}
	if tr.To != "" {
		next.State = tr.To
	}
	next.Version = e.Seq
	return next, nil
}

func applyStart(e Event) (Projection, error) {
	if e.Seq != 1 {
		return Projection{}, fmt.Errorf("%w: first event has seq %d", ErrSequenceGap, e.Seq)
	}
	var pl struct {
		Reference string `json:"reference"`
		Channel   string `json:"channel"`
	}
	if err := json.Unmarshal(e.Payload, &pl); err != nil {
		return Projection{}, fmt.Errorf("%w: session.started payload: %w", ErrIllegalTransition, err)
	}
	ref, err := ParseReference(pl.Reference)
	if err != nil {
		return Projection{}, fmt.Errorf("%w: session.started reference: %w", ErrIllegalTransition, err)
	}
	at := e.RecordedAt
	p := Projection{ID: e.IncidentID, Reference: ref, State: StateIntake, Version: 1, Channel: pl.Channel}
	p.T[1] = &at
	return p, nil
}

// Fold derives the projection from an incident's complete event history.
func Fold(events []Event) (Projection, error) {
	if len(events) == 0 {
		return Projection{}, ErrEmptyHistory
	}
	var p Projection
	for _, e := range events {
		var err error
		if p, err = Apply(p, e); err != nil {
			return p, err
		}
	}
	return p, nil
}
