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

// SealText encrypts a free-text identifier from a screen answer (a typed
// landmark, a callback number) bound to its storage location.
func (i Identifiers) SealText(ctx context.Context, plaintext []byte, aad AAD) (Sealed, error) {
	return i.Env.Seal(ctx, ClassIdentifiers, plaintext, aad)
}

// StepMAC returns the current-period keyed hash for a USSD step key. Step
// input can hold a callback number, so it is never hashed without a key
// (M0 review rule; ADR-0021).
func (i Identifiers) StepMAC(ctx context.Context, msg []byte) (MAC, error) {
	return i.Env.macInfo(ctx, ClassIdentifiers, stepInfo, msg)
}

// StepLookupMACs returns current and previous-period step hashes, so a retry
// that crosses a key rotation still finds its stored reply.
func (i Identifiers) StepLookupMACs(ctx context.Context, msg []byte) ([]MAC, error) {
	return i.Env.lookupMACsInfo(ctx, ClassIdentifiers, stepInfo, msg)
}
