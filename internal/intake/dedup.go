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

// Candidate is an incident already dispatching that a new call may join, or
// the new call itself (T2 zero).
type Candidate struct {
	ID              uuid.UUID
	Category        string
	Zone            incident.Zone
	LocationQuality string // landmark, fuzzy or ward (dispatch_ready)
	Severity        incident.Severity
	Escalated       bool
	T1, T2          time.Time
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func chebyshev(a, b incident.Zone) int { return max(absInt(a.Col-b.Col), absInt(a.Row-b.Row)) }

// located reports whether a zone is the caller's place (a listed landmark or
// a matched typed one), not a ward's centre standing in for an unknown place.
func located(quality string) bool {
	return quality == incident.QualityLandmark || quality == incident.QualityFuzzy
}

// Survivor returns the incident the new call should merge into: the
// matching candidate dispatched earliest (its alerts are already out, so the
// call joins it rather than restarting dispatch), ties broken by ID. The
// survivor's T1 then becomes the earliest call's.
//
// A call located only to its ward (IVR "another landmark", an unmatched
// typed landmark) sits at the ward's centre whatever its real place, so two
// of them in one ward are at distance 0 and may still be unrelated. Such a
// call is never merged and no call is merged into it: both are dispatched
// (fail safe; a coordinator can see them side by side).
func (r DedupRule) Survivor(call Candidate, cands []Candidate) (Candidate, bool) {
	if !located(call.LocationQuality) {
		return Candidate{}, false
	}
	var best *Candidate
	for i := range cands {
		c := &cands[i]
		gap := c.T1.Sub(call.T1)
		if c.ID == call.ID || c.Category != call.Category || !located(c.LocationQuality) ||
			chebyshev(c.Zone, call.Zone) > r.MaxZoneDistance || gap > r.Window || -gap > r.Window {
			continue
		}
		if best == nil || c.T2.Before(best.T2) || (c.T2.Equal(best.T2) && c.ID.Compare(best.ID) < 0) {
			best = c
		}
	}
	if best == nil {
		return Candidate{}, false
	}
	return *best, true
}

// DispatchRequest records T2 for an incident and merges it into a survivor
// if the rule finds one, atomically and serialised against concurrent
// completions (the operational store adapter). Severity and Escalated are
// the call's own, carried to the survivor on a merge.
type DispatchRequest struct {
	IncidentID      uuid.UUID
	Payload         json.RawMessage // the incident.dispatch_ready payload
	IdemKey         string
	Category        string
	Severity        incident.Severity
	Escalated       bool
	LocationQuality string
	Zone            incident.Zone
	T1              time.Time
	Rule            DedupRule
}

// Call is the request's call as a dedup candidate.
func (r DispatchRequest) Call() Candidate {
	return Candidate{ID: r.IncidentID, Category: r.Category, Zone: r.Zone, LocationQuality: r.LocationQuality,
		Severity: r.Severity, Escalated: r.Escalated, T1: r.T1}
}

// MergeConsequences are the events a merge adds to the survivor beyond
// merge_absorbed, so that absorbing a call never makes an emergency look
// less urgent (hard constraint 3; Build Prompt §14): the call's severity if
// it ranks higher (incident.severity_raised), and an escalation if the call
// holds one no coordinator has acknowledged, whether or not the survivor was
// ever escalated (F-06): a merge never silences an alert nobody heard.
// Payloads only; the store adds keys.
type MergeConsequences struct {
	Raise    json.RawMessage  // severity_raised payload, or nil
	Escalate []EscalationPair // escalations to record on the survivor, as recorded on the call
}

// EscalationPair is an escalation's reason and role ("" for none).
type EscalationPair struct{ Reason, Role string }

// Payload is the pair's incident.escalated payload.
func (p EscalationPair) Payload() (json.RawMessage, error) {
	if p.Role == "" {
		return json.Marshal(map[string]string{"reason": p.Reason})
	}
	return json.Marshal(map[string]string{"reason": p.Reason, "role": p.Role})
}

// MergeEscalationKey is the idempotency key of the survivor's copy of one of
// the absorbed call's escalations: one per merge and pair.
func MergeEscalationKey(from uuid.UUID, p EscalationPair) string {
	role := p.Role
	if role == "" {
		role = "-"
	}
	return "dedup:escalate:" + from.String() + ":" + p.Reason + ":" + role
}

// Consequences computes what absorbing call adds to survivor. unacked are
// the call's escalations no coordinator has acknowledged, one per (reason,
// role) (the store reads them in the merge's transaction): each is recorded
// on the survivor as it was, since a merged incident is never rung.
func Consequences(call, survivor Candidate, unacked []EscalationPair) (MergeConsequences, error) {
	var out MergeConsequences
	var err error
	if call.Severity.Rank() > survivor.Severity.Rank() {
		if out.Raise, err = json.Marshal(map[string]string{"severity": string(call.Severity), "from": call.ID.String()}); err != nil {
			return out, err
		}
	}
	out.Escalate = unacked
	return out, nil
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
