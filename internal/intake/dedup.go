package intake

import (
	"context"
	"encoding/json"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
)

// DedupRule merges calls about one emergency (Build Prompt §10.3): the same
// category, zones at most MaxZoneDistance apart (Chebyshev), first calls at
// most Window apart. No call is discarded: a duplicate is merged, logged, and
// can be split by a coordinator.
type DedupRule struct {
	Window          time.Duration
	MaxZoneDistance int
}

// Candidate is an incident already dispatching that a new call may join.
type Candidate struct {
	ID       uuid.UUID
	Category string
	Zone     incident.Zone
	T1, T2   time.Time
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func chebyshev(a, b incident.Zone) int { return max(absInt(a.Col-b.Col), absInt(a.Row-b.Row)) }

// Survivor returns the incident the new call (id, category, zone, its T1)
// should merge into: the matching candidate dispatched earliest (its alerts
// are already out, so the call joins it rather than restarting dispatch),
// ties broken by ID. The survivor's T1 then becomes the earliest call's.
func (r DedupRule) Survivor(id uuid.UUID, category string, zone incident.Zone, t1 time.Time, cands []Candidate) (uuid.UUID, bool) {
	var best *Candidate
	for i := range cands {
		c := &cands[i]
		gap := c.T1.Sub(t1)
		if c.ID == id || c.Category != category || chebyshev(c.Zone, zone) > r.MaxZoneDistance || gap > r.Window || -gap > r.Window {
			continue
		}
		if best == nil || c.T2.Before(best.T2) || (c.T2.Equal(best.T2) && c.ID.Compare(best.ID) < 0) {
			best = c
		}
	}
	if best == nil {
		return uuid.UUID{}, false
	}
	return best.ID, true
}

// DispatchRequest records T2 for an incident and merges it into a survivor
// if the rule finds one, atomically and serialised against concurrent
// completions (the operational store adapter).
type DispatchRequest struct {
	IncidentID uuid.UUID
	Payload    json.RawMessage // the incident.dispatch_ready payload
	IdemKey    string
	Category   string
	Zone       incident.Zone
	T1         time.Time
	Rule       DedupRule
}

// DispatchOutcome reports a merge.
type DispatchOutcome struct {
	Merged   bool
	Survivor uuid.UUID
}

// Dispatcher records dispatch readiness with deduplication.
type Dispatcher interface {
	DispatchOrMerge(ctx context.Context, r DispatchRequest) (DispatchOutcome, error)
}
