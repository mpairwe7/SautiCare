package crypto

import (
	"crypto/mldsa"
	"fmt"
)

// ML-DSA context strings. Each signature purpose has its own, so a
// signature made for one can never verify as another, even under one key.
const (
	// CheckpointContext signs audit checkpoints.
	CheckpointContext = "sauti-audit-checkpoint/v1"
	// TriageRulesContext signs triage rule bundles (clinical sign-off).
	TriageRulesContext = "sauti-triage-rules/v1"
)

// SignWithContext signs msg with ML-DSA under the given context string.
func SignWithContext(sk *mldsa.PrivateKey, context string, msg []byte) ([]byte, error) {
	return sk.Sign(nil, msg, &mldsa.Options{Context: context})
}

// VerifyWithContext verifies an ML-DSA-65 signature made under context
// against an encoded public key.
func VerifyWithContext(pub []byte, context string, msg, sig []byte) error {
	pk, err := mldsa.NewPublicKey(mldsa.MLDSA65(), pub)
	if err != nil {
		return fmt.Errorf("crypto: parse ML-DSA-65 public key: %w", err)
	}
	if err := mldsa.Verify(pk, msg, sig, &mldsa.Options{Context: context}); err != nil {
		return fmt.Errorf("crypto: %s signature: %w", context, err)
	}
	return nil
}

// SignMLDSA65 signs msg with the checkpoint context.
func SignMLDSA65(sk *mldsa.PrivateKey, msg []byte) ([]byte, error) {
	return SignWithContext(sk, CheckpointContext, msg)
}

// VerifyMLDSA65 verifies an ML-DSA-65 checkpoint signature against an encoded
// public key.
func VerifyMLDSA65(pub, msg, sig []byte) error {
	return VerifyWithContext(pub, CheckpointContext, msg, sig)
}
