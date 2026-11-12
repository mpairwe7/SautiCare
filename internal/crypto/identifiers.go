package crypto

import (
	"context"

	"sautihealth.com/sauti/internal/pii"
)

// Identifiers seals and hashes personal identifiers of the operational store.
// It satisfies incident.PhoneSealer.
type Identifiers struct {
	Env *Envelope
}

// SealPhone encrypts the number bound to its storage location.
func (i Identifiers) SealPhone(ctx context.Context, p pii.Phone, aad AAD) (Sealed, error) {
	return i.Env.Seal(ctx, ClassIdentifiers, []byte(p.E164()), aad)
}

// PhoneMAC returns the current-period lookup hash of the number.
func (i Identifiers) PhoneMAC(ctx context.Context, p pii.Phone) (MAC, error) {
	return i.Env.MAC(ctx, ClassIdentifiers, []byte(p.E164()))
}

// PhoneLookupMACs returns current and previous-period hashes for a redial.
func (i Identifiers) PhoneLookupMACs(ctx context.Context, p pii.Phone) ([]MAC, error) {
	return i.Env.LookupMACs(ctx, ClassIdentifiers, []byte(p.E164()))
}
