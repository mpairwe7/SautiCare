// Package devkeys is the development KeyProvider and Signer: keys in a local
// 0600 JSON file. It exists so that M0–M7 can run without OpenBao and an HSM,
// and it refuses to open when SAUTI_ENV=production (ADR-0013). The production
// adapter is internal/platform/openbao (P0).
package devkeys

import (
	"context"
	"crypto/mldsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"sautihealth.com/sauti/internal/crypto"
)

var (
	// ErrProductionForbidden is returned when dev keys are opened outside dev/test.
	ErrProductionForbidden = errors.New("devkeys: development keys are forbidden in production")
	// ErrInsecurePermissions is returned when the key file is group/world accessible.
	ErrInsecurePermissions = errors.New("devkeys: keys.json must not be readable by group or others")
)

const fileName = "keys.json"

// classes are the key classes generated for development.
var classes = []string{crypto.ClassIdentifiers}

type keyFile struct {
	Version int                  `json:"version"`
	Classes map[string]classKeys `json:"classes"`
	Signer  signerKey            `json:"signer"`
}

type classKeys struct {
	Current  dataKey `json:"current"`
	Previous dataKey `json:"previous"`
}

type dataKey struct {
	ID  string `json:"id"`
	Key []byte `json:"key"`
}

type signerKey struct {
	ID   string `json:"id"`
	Seed []byte `json:"seed"`
}

// Generate writes a fresh keys.json into dir with mode 0600.
func Generate(dir string, rand io.Reader) error {
	read := func(n int) ([]byte, error) {
		b := make([]byte, n)
		_, err := io.ReadFull(rand, b)
		return b, err
	}
	kf := keyFile{Version: 1, Classes: map[string]classKeys{}}
	for _, c := range classes {
		cur, err := read(32)
		if err != nil {
			return err
		}
		prev, err := read(32)
		if err != nil {
			return err
		}
		kf.Classes[c] = classKeys{
			Current:  dataKey{ID: "dev:" + c + ":2", Key: cur},
			Previous: dataKey{ID: "dev:" + c + ":1", Key: prev},
		}
	}
	seed, err := read(mldsa.PrivateKeySize)
	if err != nil {
		return err
	}
	kf.Signer = signerKey{ID: "dev:audit-checkpoint:1", Seed: seed}
	b, err := json.MarshalIndent(kf, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, fileName), b, 0o600)
}

// Store implements crypto.KeyProvider from a dev key file.
type Store struct {
	kf     keyFile
	byID   map[string]crypto.DataKey
	signer *signer
}

// Open loads keys.json from dir. env is the value of SAUTI_ENV.
func Open(dir, env string) (*Store, error) {
	// Allowlist, not denylist: only an explicit dev or test environment may
	// use development keys (review finding M7).
	if env != "dev" && env != "test" {
		return nil, ErrProductionForbidden
	}
	path := filepath.Join(dir, fileName)
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("devkeys: %w", err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, ErrInsecurePermissions
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("devkeys: %w", err)
	}
	var kf keyFile
	if err := json.Unmarshal(b, &kf); err != nil {
		return nil, fmt.Errorf("devkeys: parse %s: %w", fileName, err)
	}
	s := &Store{kf: kf, byID: map[string]crypto.DataKey{}}
	for _, ck := range kf.Classes {
		for _, k := range []dataKey{ck.Current, ck.Previous} {
			if k.ID != "" {
				s.byID[k.ID] = crypto.DataKey{ID: k.ID, Key: k.Key}
			}
		}
	}
	sk, err := mldsa.NewPrivateKey(mldsa.MLDSA65(), kf.Signer.Seed)
	if err != nil {
		return nil, fmt.Errorf("devkeys: signer seed: %w", err)
	}
	s.signer = &signer{id: kf.Signer.ID, sk: sk}
	return s, nil
}

// Current implements crypto.KeyProvider.
func (s *Store) Current(_ context.Context, class string) (crypto.DataKey, error) {
	ck, ok := s.kf.Classes[class]
	if !ok {
		return crypto.DataKey{}, fmt.Errorf("%w: class %s", crypto.ErrUnknownKey, class)
	}
	return crypto.DataKey{ID: ck.Current.ID, Key: ck.Current.Key}, nil
}

// Previous implements crypto.KeyProvider.
func (s *Store) Previous(_ context.Context, class string) (crypto.DataKey, bool, error) {
	ck, ok := s.kf.Classes[class]
	if !ok || ck.Previous.ID == "" {
		return crypto.DataKey{}, false, nil
	}
	return crypto.DataKey{ID: ck.Previous.ID, Key: ck.Previous.Key}, true, nil
}

// ByID implements crypto.KeyProvider.
func (s *Store) ByID(_ context.Context, id string) (crypto.DataKey, error) {
	k, ok := s.byID[id]
	if !ok {
		return crypto.DataKey{}, crypto.ErrUnknownKey
	}
	return k, nil
}

// Signer returns the checkpoint signer.
func (s *Store) Signer() crypto.Signer { return s.signer }

type signer struct {
	id string
	sk *mldsa.PrivateKey
}

func (g *signer) KeyID() string     { return g.id }
func (g *signer) PublicKey() []byte { return g.sk.PublicKey().Bytes() }
func (g *signer) Sign(_ context.Context, msg []byte) ([]byte, error) {
	return crypto.SignMLDSA65(g.sk, msg)
}
