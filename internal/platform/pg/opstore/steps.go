package opstore

import (
	"context"
	"fmt"
	"uuid"

	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/platform/pg/opstore/opdb"
)

// Lookup implements intake.StepStore.
func (s *Store) Lookup(ctx context.Context, provider, sessionID string, inputSHA [32]byte) (intake.Screen, bool, error) {
	rows, err := s.q.LookupUSSDStep(ctx, opdb.LookupUSSDStepParams{
		Provider: provider, ProviderSessionID: sessionID, InputSha256: inputSHA[:],
	})
	if err != nil {
		return intake.Screen{}, false, fmt.Errorf("opstore: ussd step lookup: %w", err)
	}
	if len(rows) == 0 {
		return intake.Screen{}, false, nil
	}
	return intake.Screen{Text: rows[0].Response, End: rows[0].EndSession}, true, nil
}

// Save implements intake.StepStore: the first stored reply wins, and the
// stored reply is returned, so concurrent duplicates answer identically.
func (s *Store) Save(ctx context.Context, provider, sessionID string, inputSHA [32]byte, sc intake.Screen, incidentID uuid.UUID) (intake.Screen, error) {
	if err := s.q.SaveUSSDStep(ctx, opdb.SaveUSSDStepParams{
		Provider: provider, ProviderSessionID: sessionID, InputSha256: inputSHA[:],
		Response: sc.Text, EndSession: sc.End, IncidentID: pgID(incidentID),
	}); err != nil {
		return intake.Screen{}, fmt.Errorf("opstore: ussd step save: %w", err)
	}
	stored, ok, err := s.Lookup(ctx, provider, sessionID, inputSHA)
	if err != nil {
		return intake.Screen{}, err
	}
	if !ok {
		return intake.Screen{}, fmt.Errorf("opstore: ussd step vanished after save")
	}
	return stored, nil
}
