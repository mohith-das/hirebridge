package crypto

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
)

// VerifySignature checks an ed25519 signature over the exact message bytes.
//
// Contract: the node transmits a JSON payload as raw bytes. HireBridge verifies
// the signature over those exact bytes without reformatting or re-marshaling.
// The embedding field is not covered by the signature.
func VerifySignature(pubKey []byte, message []byte, sigHex string) (bool, error) {
	if pubKey == nil || len(pubKey) != ed25519.PublicKeySize {
		return false, fmt.Errorf("invalid public key")
	}

	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return false, fmt.Errorf("decode signature: %w", err)
	}
	if len(sig) != ed25519.SignatureSize {
		return false, fmt.Errorf("invalid signature length: got %d, want %d", len(sig), ed25519.SignatureSize)
	}

	return ed25519.Verify(pubKey, message, sig), nil
}
