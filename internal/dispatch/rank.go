// Package dispatch decides who is alerted for an incident and when (Build
// Prompt §10.4): eligibility, ranking, search rings, the alert cascade,
// forwarding and escalation. Every decision is a pure function of the
// recorded state and the time, so it is deterministic (rule 3.3) and every
// timer is testable; cmd/dispatch-worker executes the decisions (ADR-0024).
package dispatch

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"time"

	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/responder"
)

// History is a responder's recent dispatch record.
type History struct {
	Alerted, Accepted int
	LastDispatched    *time.Time
}

// acceptRate is smoothed so a new responder neither tops nor trails the
// ranking on no evidence: (accepted + 1) / (alerted + 2).
func (h History) acceptRate() float64 {
	return float64(h.Accepted+1) / float64(h.Alerted+2)
}

// Candidate is an eligible responder for one incident.
type Candidate struct {
	Responder responder.Responder
	Cell      location.Cell // the responder's stage
	Distance  int           // Chebyshev distance from the incident cell
	History   History
}

// Eligible returns the responders of role who pass the gates, have an open
// availability period at now, and wait at a stage within ring k of the
// incident. The incident is its zone, or every zone of its ward when only
// the ward is known; a responder's distance is to the nearest of them.
func Eligible(rs []responder.Responder, stageCells map[string]location.Cell, role responder.Role, centres []location.Cell, k int, now time.Time) []Candidate {
	var out []Candidate
	for _, r := range rs {
		cell, ok := stageCells[r.Stage]
		if r.Role != role || !ok || r.Gates(now) != nil || !r.AvailableAt(now) || len(centres) == 0 {
			continue
		}
		d := centres[0].Chebyshev(cell)
		for _, c := range centres[1:] {
			d = min(d, c.Chebyshev(cell))
		}
		if d <= k {
			out = append(out, Candidate{Responder: r, Cell: cell, Distance: d})
		}
	}
	return out
}

// Ranking criteria (decision 4, open: the order is configuration, and its
// version is stamped on every dispatch).
const (
	ByZoneDistance            = "zone_distance"
	ByResponseHistory         = "response_history"
	ByLeastRecentlyDispatched = "least_recently_dispatched"
)

// DefaultOrder is the safe default of decision 4.
var DefaultOrder = []string{ByZoneDistance, ByResponseHistory, ByLeastRecentlyDispatched}

// Ranker orders candidates.
type Ranker struct {
	order   []string
	version string
}

// NewRanker validates the configured order.
func NewRanker(order []string, version string) (Ranker, error) {
	if len(order) == 0 || version == "" {
		return Ranker{}, errors.New("dispatch: ranking order and version are required")
	}
	for i, c := range order {
		if c != ByZoneDistance && c != ByResponseHistory && c != ByLeastRecentlyDispatched || slices.Contains(order[:i], c) {
			return Ranker{}, fmt.Errorf("dispatch: ranking criterion %q unknown or repeated", c)
		}
	}
	return Ranker{order: slices.Clone(order), version: version}, nil
}

// Version is the ranking parameter version.
func (r Ranker) Version() string { return r.version }

func compareBy(c string, a, b Candidate) int {
	switch c {
	case ByZoneDistance:
		return cmp.Compare(a.Distance, b.Distance)
	case ByResponseHistory:
		return -cmp.Compare(a.History.acceptRate(), b.History.acceptRate())
	case ByLeastRecentlyDispatched:
		switch la, lb := a.History.LastDispatched, b.History.LastDispatched; {
		case la == nil && lb == nil:
			return 0
		case la == nil:
			return -1 // never dispatched comes first
		case lb == nil:
			return 1
		default:
			return la.Compare(*lb)
		}
	}
	return 0
}

// Rank returns the candidates best first, in the configured order with the
// responder ID as the final tie-break, so the order is total.
func (r Ranker) Rank(cs []Candidate) []Candidate {
	out := slices.Clone(cs)
	slices.SortStableFunc(out, func(a, b Candidate) int {
		for _, c := range r.order {
			if d := compareBy(c, a, b); d != 0 {
				return d
			}
		}
		return a.Responder.ID.Compare(b.Responder.ID)
	})
	return out
}
