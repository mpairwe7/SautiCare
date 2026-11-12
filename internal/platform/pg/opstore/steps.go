package opstore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"uuid"

	"github.com/jackc/pgx/v5/pgtype"

	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/platform/pg/opstore/opdb"
)

func stepFrom(response string, end bool, consumed int32, mac []byte, keyID string, inc pgtype.UUID) intake.Step {
	st := intake.Step{Screen: intake.Screen{Text: response, End: end}, ConsumedLen: int(consumed),
		MAC: crypto.MAC{KeyID: keyID, Sum: mac}}
	if inc.Valid {
		st.IncidentID = uuid.UUID(inc.Bytes)
	}
	return st
}

// Lookup implements intake.StepStore.
func (s *Store) Lookup(ctx context.Context, provider, sessionID string, macs []crypto.MAC) (intake.Step, bool, error) {
	sums := make([][]byte, len(macs))
	for i, m := range macs {
		sums[i] = m.Sum
	}
	rows, err := s.q.LookupUSSDStep(ctx, opdb.LookupUSSDStepParams{Provider: provider, ProviderSessionID: sessionID, InputMacs: sums})
	if err != nil {
		return intake.Step{}, false, fmt.Errorf("opstore: ussd step lookup: %w", err)
	}
	if len(rows) == 0 {
		return intake.Step{}, false, nil
	}
	r := rows[0]
	return stepFrom(r.Response, r.EndSession, r.ConsumedLen, r.InputMac, r.MacKeyID, r.IncidentID), true, nil
}

// Latest implements intake.StepStore.
func (s *Store) Latest(ctx context.Context, provider, sessionID string) (intake.Step, bool, error) {
	rows, err := s.q.LatestUSSDStep(ctx, opdb.LatestUSSDStepParams{Provider: provider, ProviderSessionID: sessionID})
	if err != nil {
		return intake.Step{}, false, fmt.Errorf("opstore: ussd latest step: %w", err)
	}
	if len(rows) == 0 {
		return intake.Step{}, false, nil
	}
	r := rows[0]
	return stepFrom(r.Response, r.EndSession, r.ConsumedLen, r.InputMac, r.MacKeyID, r.IncidentID), true, nil
}

// Save implements intake.StepStore: the first stored reply wins, and the
// stored reply is returned, so concurrent duplicates answer identically.
func (s *Store) Save(ctx context.Context, provider, sessionID string, st intake.Step) (intake.Screen, error) {
	if len(st.MAC.Sum) != 32 || st.MAC.KeyID == "" || st.ConsumedLen < 0 || st.ConsumedLen > math.MaxInt32 {
		return intake.Screen{}, errors.New("opstore: ussd step needs a 32-byte keyed MAC and a consumed length")
	}
	var inc pgtype.UUID
	if st.IncidentID != (uuid.UUID{}) {
		inc = pgID(st.IncidentID)
	}
	if err := s.q.SaveUSSDStep(ctx, opdb.SaveUSSDStepParams{
		Provider: provider, ProviderSessionID: sessionID, InputMac: st.MAC.Sum, MacKeyID: st.MAC.KeyID,
		ConsumedLen: int32(st.ConsumedLen), //nolint:gosec // G115: bounded by the check above
		Response:    st.Screen.Text, EndSession: st.Screen.End, IncidentID: inc,
	}); err != nil {
		return intake.Screen{}, fmt.Errorf("opstore: ussd step save: %w", err)
	}
	stored, ok, err := s.Lookup(ctx, provider, sessionID, []crypto.MAC{st.MAC})
	if err != nil {
		return intake.Screen{}, err
	}
	if !ok {
		return intake.Screen{}, errors.New("opstore: ussd step vanished after save")
	}
	return stored.Screen, nil
}

// StoreIdentifier implements incident.Store.
func (s *Store) StoreIdentifier(ctx context.Context, p incident.IdentifierParams) (uuid.UUID, error) {
	id, err := s.q.StoreIdentifier(ctx, opdb.StoreIdentifierParams{
		ID: pgID(p.ID), IncidentID: pgID(p.IncidentID), Kind: p.Kind,
		Ct: p.Sealed.Ciphertext, KeyID: p.Sealed.KeyID, IdemKey: p.IdemKey,
	})
	if err != nil {
		return uuid.UUID{}, mapErr(err)
	}
	return uuid.UUID(id.Bytes), nil
}
