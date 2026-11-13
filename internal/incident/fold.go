package incident

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"
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
	case EvScreenAnswered:
		a, err := ParseScreenAnswer(e.Payload)
		if err != nil {
			return p, err
		}
		if err := next.set(a); err != nil {
			return p, err
		}
	case EvSessionResumed:
		if err := parseResumed(e.Payload); err != nil {
			return p, err
		}
	case EvDispatchReady:
		loc, err := ParseDispatchLocation(e.Payload)
		if err != nil {
			return p, err
		}
		if loc.Zone != nil {
			z := *loc.Zone
			next.Zone, next.LocationQuality = &z, loc.Quality
		}
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
		if _, _, err := ParseEscalated(e.Payload); err != nil {
			return p, err
		}
		next.Escalated = true
	case EvAlertIssued, EvAccepted, EvAlertDeclined, EvAlertNoResponse, EvAlertStoodDown:
		if _, err := ParseAlertFact(e.Type, e.Payload); err != nil {
			return p, err
		}
	case EvForwarded, EvForwardAcknowledged, EvForwardTimedOut:
		if _, err := ParseFacility(e.Payload); err != nil {
			return p, err
		}
	case EvCallerNotified:
		if _, _, err := ParseCallerNotified(e.Payload); err != nil {
			return p, err
		}
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
	case EvMergeAbsorbed:
		from, t1, err := parseAbsorbed(e.Payload)
		if err != nil {
			return p, err
		}
		if _, dup := p.Absorbed[from]; dup || from == p.ID {
			return p, fmt.Errorf("%w: merge_absorbed: %s already absorbed or is this incident", ErrIllegalTransition, from)
		}
		next.Absorbed = maps.Clone(p.Absorbed)
		if next.Absorbed == nil {
			next.Absorbed = map[uuid.UUID]time.Time{}
		}
		next.Absorbed[from] = t1
		next.T[1] = earliestT1(next.OwnT1, next.Absorbed)
	case EvSplit:
		if e.ActorKind != ActorStaff {
			return p, fmt.Errorf("%w: only a coordinator splits a merge", ErrIllegalTransition)
		}
		from, err := parseFrom(e.Payload)
		if err != nil {
			return p, err
		}
		if _, ok := p.Absorbed[from]; !ok {
			return p, fmt.Errorf("%w: split: %s was not merged into this incident", ErrIllegalTransition, from)
		}
		next.Absorbed = maps.Clone(p.Absorbed)
		delete(next.Absorbed, from)
		next.T[1] = earliestT1(next.OwnT1, next.Absorbed)
	case EvSeverityRaised:
		sev, from, err := parseSeverityRaised(e.Payload)
		if err != nil {
			return p, err
		}
		if from == p.ID {
			return p, fmt.Errorf("%w: severity_raised: an incident cannot raise itself", ErrIllegalTransition)
		}
		// Only a call merged into this incident raises it. The fold sees the
		// absorbed set; the database also checks the call's own row (merged
		// into this incident, with exactly this severity), which Apply,
		// seeing one incident, cannot.
		if _, ok := p.Absorbed[from]; !ok {
			return p, fmt.Errorf("%w: severity_raised: %s was not merged into this incident", ErrIllegalTransition, from)
		}
		if sev.Rank() <= p.Severity.Rank() {
			return p, fmt.Errorf("%w: severity_raised must raise the severity (%s -> %s)", ErrIllegalTransition, p.Severity, sev)
		}
		next.Severity = sev
	case EvUnmerged:
		from, err := parseFrom(e.Payload)
		if err != nil {
			return p, err
		}
		if p.MergedInto == nil || *p.MergedInto != from || p.T[2] == nil {
			return p, fmt.Errorf("%w: unmerged needs its survivor and a T2 (merged at dispatch)", ErrIllegalTransition)
		}
		next.MergedInto = nil
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
	p := Projection{ID: e.IncidentID, Reference: ref, State: StateIntake, Version: 1, Channel: pl.Channel, OwnT1: at}
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
