package opstore

import (
	"context"
	"fmt"

	"sautihealth.com/sauti/internal/incident"
)

// VerifyProjections replays every incident's events through the pure
// reducer and compares the result with the database projection (spec §6.2).
// It returns how many incidents were checked and one line per drift.
func VerifyProjections(ctx context.Context, s *Store) (int, []string, error) {
	ids, err := s.ListIncidentIDs(ctx)
	if err != nil {
		return 0, nil, err
	}
	var drift []string
	for _, id := range ids {
		events, err := s.Events(ctx, id)
		if err != nil {
			return 0, nil, err
		}
		want, err := incident.Fold(events)
		if err != nil {
			drift = append(drift, fmt.Sprintf("%s: events do not fold: %v", id, err))
			continue
		}
		got, err := s.Projection(ctx, id)
		if err != nil {
			return 0, nil, err
		}
		if d := incident.Diff(got, want); d != "" {
			drift = append(drift, fmt.Sprintf("%s: %s", id, d))
		}
	}
	return len(ids), drift, nil
}
