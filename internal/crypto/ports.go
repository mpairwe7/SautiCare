// Package crypto implements the cryptographic controls of the data
// architecture (spec §6.4, ADR-0013): AES-256-GCM envelope encryption of
// identifiers with row-bound associated data, keyed lookup hashes rotated with
// key periods, and ML-DSA-65 signatures for audit checkpoints.
//
// Key material never lives here. It comes through the KeyProvider and Signer
// ports: OpenBao + HSM in production (P0), a local file in development
// (internal/platform/devkeys), which refuses to run in production.
package crypto

import (
	"context"
	"errors"
)

// ClassIdentifiers is the key class for caller and responder identifiers in
// the operational store. Destroying a period's key crypto-shreds that period.
const ClassIdentifiers = "operational.identifiers"

var (
	// ErrUnknownKey reports a key ID the provider does not hold, for example
	// because its period was destroyed (crypto-shredding).
	ErrUnknownKey = errors.New("crypto: unknown or destroyed key")
	// ErrDecrypt reports a ciphertext that fails authentication: wrong key,
	// tampered bytes, or associated data that does not match its row.
	ErrDecrypt = errors.New("crypto: decryption failed")
)

// DataKey is a 256-bit data-encryption key for one class and key period.
type DataKey struct {
	ID  string
	Key []byte
}

// KeyProvider supplies data keys. Current is used for new writes; Previous
// (if any) is kept for lookups across a period boundary; ByID opens old data.
type KeyProvider interface {
	Current(ctx context.Context, class string) (DataKey, error)
	Previous(ctx context.Context, class string) (DataKey, bool, error)
	ByID(ctx context.Context, id string) (DataKey, error)
}

// Signer signs audit checkpoints with ML-DSA-65.
type Signer interface {
	KeyID() string
	PublicKey() []byte
	Sign(ctx context.Context, msg []byte) ([]byte, error)
}

// Witness stores signed checkpoints outside the audit database, write-once,
// so that a rewrite of the ledger cannot also rewrite its witnesses.
type Witness interface {
	Put(ctx context.Context, name string, data []byte) error
}
