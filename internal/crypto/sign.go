package crypto

import (
	"crypto/mldsa"
	"fmt"
)

// CheckpointContext is the ML-DSA context string for audit checkpoints. It
// domain-separates these signatures from any other use of the same key.
const CheckpointContext = "sauti-audit-checkpoint/v1"

// SignMLDSA65 signs msg with the checkpoint context.
func SignMLDSA65(sk *mldsa.PrivateKey, msg []byte) ([]byte, error) {
	return sk.Sign(nil, msg, &mldsa.Options{Context: CheckpointContext})
}

// VerifyMLDSA65 verifies an ML-DSA-65 checkpoint signature against an encoded
// public key.
func VerifyMLDSA65(pub, msg, sig []byte) error {
	pk, err := mldsa.NewPublicKey(mldsa.MLDSA65(), pub)
	if err != nil {
		return fmt.Errorf("crypto: parse ML-DSA-65 public key: %w", err)
	}
	if err := mldsa.Verify(pk, msg, sig, &mldsa.Options{Context: CheckpointContext}); err != nil {
		return fmt.Errorf("crypto: checkpoint signature: %w", err)
	}
	return nil
}
