package opstore

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgtype"

	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/platform/pg/opstore/opdb"
	"sautihealth.com/sauti/internal/responder"
)

func tsArg(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

// RegisterResponder implements responder.Store (console role).
func (s *Store) RegisterResponder(ctx context.Context, p responder.RegisterParams) error {
	if err := s.q.RegisterResponder(ctx, opdb.RegisterResponderParams{
		ID: pgID(p.ID), Role: string(p.Role), NameCt: p.Name.Ciphertext, NameKeyID: p.Name.KeyID,
		PhoneCt: p.Phone.Ciphertext, PhoneKeyID: p.Phone.KeyID, PhoneMac: p.PhoneMAC.Sum, PhoneMacKeyID: p.PhoneMAC.KeyID,
		Ward: p.Ward, Stage: p.Stage, CertifiedAt: tsArg(p.Gates.CertifiedAt), InsuredUntil: tsArg(p.Gates.InsuredUntil),
		KitOk: p.Gates.KitOK, ResearchConsent: p.ResearchConsent,
	}); err != nil {
		return mapErr(err)
	}
	return nil
}

// SetGates updates a responder's gates and status (console role).
func (s *Store) SetGates(ctx context.Context, id uuid.UUID, g responder.Gates) error {
	if err := s.q.SetResponderGates(ctx, opdb.SetResponderGatesParams{ID: pgID(id), CertifiedAt: tsArg(g.CertifiedAt),
		InsuredUntil: tsArg(g.InsuredUntil), KitOk: g.KitOK, Status: g.Status}); err != nil {
		return mapErr(err)
	}
	return nil
}

// SetAvailability switches a responder on (a new 8-hour period on the
// database clock) or off; source is ussd, ivr or console.
func (s *Store) SetAvailability(ctx context.Context, id uuid.UUID, available bool, source string) (responder.Period, error) {
	r, err := s.q.SetAvailability(ctx, opdb.SetAvailabilityParams{ID: pgID(id), Available: available, Source: source})
	if err != nil {
		return responder.Period{}, mapErr(err)
	}
	return responder.Period{From: r.PeriodFrom.Time.UTC(), Until: r.PeriodUntil.Time.UTC()}, nil
}

func tsOpt(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}

// Responders returns the registry as dispatch sees it, with each
// responder's latest availability period (dispatch role).
func (s *Store) Responders(ctx context.Context) ([]responder.Responder, error) {
	rows, err := s.q.ListResponders(ctx)
	if err != nil {
		return nil, fmt.Errorf("opstore: responders: %w", err)
	}
	out := make([]responder.Responder, 0, len(rows))
	for _, r := range rows {
		x := responder.Responder{ID: uuid.UUID(r.ID.Bytes), Role: responder.Role(r.Role), Ward: r.Ward, Stage: r.Stage,
			CertifiedAt: tsOpt(r.CertifiedAt), InsuredUntil: tsOpt(r.InsuredUntil), KitOK: r.KitOk,
			ResearchConsent: r.ResearchConsent, Status: r.Status}
		if r.AvailableFrom.Valid && r.AvailableUntil.Valid {
			x.Available = &responder.Period{From: r.AvailableFrom.Time.UTC(), Until: r.AvailableUntil.Time.UTC()}
		}
		out = append(out, x)
	}
	return out, nil
}

// ResponderByPhone finds the registered responder calling from a number
// (keyed hash, current or previous key period).
func (s *Store) ResponderByPhone(ctx context.Context, macs []crypto.MAC) (uuid.UUID, bool, error) {
	sums := make([][]byte, len(macs))
	for i, m := range macs {
		sums[i] = m.Sum
	}
	ids, err := s.q.ResponderByPhone(ctx, sums)
	if err != nil {
		return uuid.UUID{}, false, fmt.Errorf("opstore: responder by phone: %w", err)
	}
	if len(ids) != 1 { // none, or ambiguous: never guess who is calling
		return uuid.UUID{}, false, nil
	}
	return uuid.UUID(ids[0].Bytes), true, nil
}
