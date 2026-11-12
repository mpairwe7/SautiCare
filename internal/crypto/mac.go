package crypto

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
)

const macInfo = "sauti/mac/v1"

// MAC is a keyed hash of an identifier under one key period. It lets a
// redial find its incident without decrypting anything (spec §6.4), and it
// becomes unlinkable once that period's key is destroyed.
type MAC struct {
	KeyID string
	Sum   []byte
}

func macWith(dk DataKey, msg []byte) (MAC, error) {
	k, err := hkdf.Key(sha256.New, dk.Key, nil, macInfo, 32)
	if err != nil {
		return MAC{}, fmt.Errorf("crypto: derive mac key: %w", err)
	}
	h := hmac.New(sha256.New, k)
	h.Write(msg)
	return MAC{KeyID: dk.ID, Sum: h.Sum(nil)}, nil
}

// MAC computes the keyed hash of msg under the current key of class.
func (e *Envelope) MAC(ctx context.Context, class string, msg []byte) (MAC, error) {
	dk, err := e.keys.Current(ctx, class)
	if err != nil {
		return MAC{}, fmt.Errorf("crypto: current key for %s: %w", class, err)
	}
	return macWith(dk, msg)
}

// LookupMACs returns the candidates to search for: the current period's MAC
// and, if a previous period key still exists, the previous one.
func (e *Envelope) LookupMACs(ctx context.Context, class string, msg []byte) ([]MAC, error) {
	cur, err := e.MAC(ctx, class, msg)
	if err != nil {
		return nil, err
	}
	out := []MAC{cur}
	prev, ok, err := e.keys.Previous(ctx, class)
	if err != nil {
		return nil, fmt.Errorf("crypto: previous key for %s: %w", class, err)
	}
	if ok {
		pm, err := macWith(prev, msg)
		if err != nil {
			return nil, err
		}
		out = append(out, pm)
	}
	return out, nil
}
