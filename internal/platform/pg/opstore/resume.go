package opstore

import (
	"context"
	"fmt"
	"math"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/platform/pg/opstore/opdb"
)

// ResumeSession implements incident.Store.
func (s *Store) ResumeSession(ctx context.Context, p incident.ResumeParams) (incident.Opened, error) {
	params := opdb.ResumeSessionParams{
		IncidentID:        pgID(p.IncidentID),
		Channel:           string(p.Channel),
		SessionID:         pgID(p.SessionID),
		ProviderSessionID: p.ProviderSessionID,
		NetworkCode:       strPtr(p.NetworkCode),
		IdemKey:           p.IdemKey,
	}
	if p.Phone.Ciphertext != nil {
		params.PhoneCt, params.PhoneKeyID = p.Phone.Ciphertext, strPtr(p.Phone.KeyID)
	}
	if p.PhoneMAC.Sum != nil {
		params.PhoneMac, params.PhoneMacKeyID = p.PhoneMAC.Sum, strPtr(p.PhoneMAC.KeyID)
	}
	r, err := s.q.ResumeSession(ctx, params)
	if err != nil {
		return incident.Opened{}, mapErr(err)
	}
	ref, err := incident.ParseReference(r.Reference)
	if err != nil {
		return incident.Opened{}, err
	}
	o := incident.Opened{IncidentID: uuid.UUID(r.IncidentID.Bytes), Reference: ref, T1: r.T1.Time.UTC(), Created: r.Created}
	if r.EventID.Valid {
		o.EventID = uuid.UUID(r.EventID.Bytes)
	}
	return o, nil
}

// FindResumable returns the most recently active unfinished, open incident
// of a caller (any of macs: current and previous key period) whose last
// activity is within window of the database clock.
func (s *Store) FindResumable(ctx context.Context, macs []crypto.MAC, window time.Duration) (uuid.UUID, bool, error) {
	if len(macs) == 0 {
		return uuid.UUID{}, false, nil
	}
	secs := window.Seconds()
	if secs <= 0 || secs > math.MaxInt32 {
		return uuid.UUID{}, false, fmt.Errorf("opstore: resume window %v out of range", window)
	}
	sums := make([][]byte, len(macs))
	for i, m := range macs {
		sums[i] = m.Sum
	}
	ids, err := s.q.FindResumable(ctx, opdb.FindResumableParams{Macs: sums, WindowSeconds: int32(secs)})
	if err != nil {
		return uuid.UUID{}, false, fmt.Errorf("opstore: find resumable: %w", err)
	}
	if len(ids) == 0 {
		return uuid.UUID{}, false, nil
	}
	return uuid.UUID(ids[0].Bytes), true, nil
}

// ListIdle implements intake.IdleReader: open, unfinished incidents with no
// event for idleFor and no abandoned flag, oldest first (at most 500 a sweep).
func (s *Store) ListIdle(ctx context.Context, idleFor time.Duration) ([]intake.Idle, error) {
	secs := idleFor.Seconds()
	if secs <= 0 || secs > math.MaxInt32 {
		return nil, fmt.Errorf("opstore: idle time %v out of range", idleFor)
	}
	rows, err := s.q.ListIdle(ctx, int32(secs))
	if err != nil {
		return nil, fmt.Errorf("opstore: list idle: %w", err)
	}
	out := make([]intake.Idle, len(rows))
	for i, r := range rows {
		out[i] = intake.Idle{ID: uuid.UUID(r.ID.Bytes), Danger: deref(r.Danger), Category: deref(r.Category), Dispatched: r.Dispatched}
	}
	return out, nil
}
