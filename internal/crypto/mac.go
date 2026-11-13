package crypto

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
)

// HKDF info strings: one derived MAC key per purpose, so a MAC computed for
// one purpose never equals one computed for another over the same bytes.
const (
	macInfo  = "sauti/mac/v1"
	stepInfo = "sauti/ussd-step/v1"
)

// MAC is a keyed hash of an identifier under one key period. It lets a
// redial find its incident without decrypting anything (spec §6.4), and it
// becomes unlinkable once that period's key is destroyed.
type MAC struct {
	KeyID string
	Sum   []byte
}

func macWithInfo(dk DataKey, info string, msg []byte) (MAC, error) {
	// Never derive a MAC key from a short or empty key: the result would be
	// computable without any secret.
	if len(dk.Key) != 32 {
		return MAC{}, fmt.Errorf("crypto: data key %s must be 32 bytes, got %d", dk.ID, len(dk.Key))
	}
	k, err := hkdf.Key(sha256.New, dk.Key, nil, info, 32)
	if err != nil {
		return MAC{}, fmt.Errorf("crypto: derive mac key: %w", err)
	}
	h := hmac.New(sha256.New, k)
	h.Write(msg)
	return MAC{KeyID: dk.ID, Sum: h.Sum(nil)}, nil
}

// MAC computes the keyed hash of msg under the current key of class.
func (e *Envelope) MAC(ctx context.Context, class string, msg []byte) (MAC, error) {
	return e.macInfo(ctx, class, macInfo, msg)
}

func (e *Envelope) macInfo(ctx context.Context, class, info string, msg []byte) (MAC, error) {
	dk, err := e.keys.Current(ctx, class)
	if err != nil {
		return MAC{}, fmt.Errorf("crypto: current key for %s: %w", class, err)
	}
	return macWithInfo(dk, info, msg)
}

// LookupMACs returns the candidates to search for: the current period's MAC
// and, if a previous period key still exists, the previous one.
func (e *Envelope) LookupMACs(ctx context.Context, class string, msg []byte) ([]MAC, error) {
	return e.lookupMACsInfo(ctx, class, macInfo, msg)
}

func (e *Envelope) lookupMACsInfo(ctx context.Context, class, info string, msg []byte) ([]MAC, error) {
	cur, err := e.macInfo(ctx, class, info, msg)
	if err != nil {
		return nil, err
	}
	out := []MAC{cur}
	prev, ok, err := e.keys.Previous(ctx, class)
	if err != nil {
		return nil, fmt.Errorf("crypto: previous key for %s: %w", class, err)
	}
	if ok {
		pm, err := macWithInfo(prev, info, msg)
		if err != nil {
			return nil, err
		}
		out = append(out, pm)
	}
	return out, nil
}
