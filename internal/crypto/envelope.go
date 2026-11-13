package crypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"io"
)

const aadDomain = "sauti-aad/v1"

// AAD binds a ciphertext to the exact place it is stored. Opening it under
// any other store, table, column or row fails authentication.
type AAD struct {
	Store, Table, Column, RowID string
}

func (a AAD) complete() bool {
	return a.Store != "" && a.Table != "" && a.Column != "" && a.RowID != ""
}

// Bytes encodes the AAD unambiguously: a domain tag, then each field as a
// 4-byte big-endian length followed by its bytes.
func (a AAD) Bytes() []byte {
	out := make([]byte, 0, 64)
	out = append(out, aadDomain...)
	for _, f := range []string{a.Store, a.Table, a.Column, a.RowID} {
		out = binary.BigEndian.AppendUint32(out, uint32(len(f))) //nolint:gosec // identifiers are short
		out = append(out, f...)
	}
	return out
}

// Sealed is a ciphertext tagged with the ID of the key period that sealed it.
// Ciphertext layout: nonce (12 bytes) ‖ GCM ciphertext ‖ tag (16 bytes).
type Sealed struct {
	KeyID      string
	Ciphertext []byte
}

// Envelope performs AES-256-GCM encryption with keys from a KeyProvider.
type Envelope struct {
	keys KeyProvider
	rand io.Reader
}

// NewEnvelope returns an Envelope. rand must be crypto/rand.Reader outside tests.
func NewEnvelope(keys KeyProvider, rand io.Reader) *Envelope {
	return &Envelope{keys: keys, rand: rand}
}

func gcmFor(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("crypto: data key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts plaintext under the current key of class, bound to aad.
func (e *Envelope) Seal(ctx context.Context, class string, plaintext []byte, aad AAD) (Sealed, error) {
	if !aad.complete() {
		return Sealed{}, ErrIncompleteAAD
	}
	dk, err := e.keys.Current(ctx, class)
	if err != nil {
		return Sealed{}, fmt.Errorf("crypto: current key for %s: %w", class, err)
	}
	g, err := gcmFor(dk.Key)
	if err != nil {
		return Sealed{}, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := io.ReadFull(e.rand, nonce); err != nil {
		return Sealed{}, fmt.Errorf("crypto: nonce: %w", err)
	}
	ct := g.Seal(nonce, nonce, plaintext, aad.Bytes())
	return Sealed{KeyID: dk.ID, Ciphertext: ct}, nil
}

// Open decrypts s, which must have been sealed with the same aad.
func (e *Envelope) Open(ctx context.Context, s Sealed, aad AAD) ([]byte, error) {
	if !aad.complete() {
		return nil, ErrIncompleteAAD
	}
	dk, err := e.keys.ByID(ctx, s.KeyID)
	if err != nil {
		return nil, err
	}
	g, err := gcmFor(dk.Key)
	if err != nil {
		return nil, err
	}
	if len(s.Ciphertext) < g.NonceSize()+g.Overhead() {
		return nil, ErrDecrypt
	}
	nonce, body := s.Ciphertext[:g.NonceSize()], s.Ciphertext[g.NonceSize():]
	pt, err := g.Open(nil, nonce, body, aad.Bytes())
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}
