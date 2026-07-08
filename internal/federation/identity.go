package federation

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

type Identity struct {
	PublicKey string `json:"public_key"`
	Seed      string `json:"seed"`
	priv      ed25519.PrivateKey
}

func LoadOrCreateIdentity(keyPath string) (*Identity, error) {
	if data, err := os.ReadFile(keyPath); err == nil {
		var id Identity
		if err := json.Unmarshal(data, &id); err != nil {
			return nil, fmt.Errorf("parse identity: %w", err)
		}
		seed, err := hex.DecodeString(id.Seed)
		if err != nil {
			return nil, fmt.Errorf("decode seed: %w", err)
		}
		id.priv = ed25519.NewKeyFromSeed(seed)
		return &id, nil
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	seed := priv.Seed()
	id := &Identity{
		PublicKey: hex.EncodeToString(priv.Public().(ed25519.PublicKey)),
		Seed:      hex.EncodeToString(seed),
		priv:      priv,
	}

	jsonData, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(keyPath, jsonData, 0600); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}

	return id, nil
}

func (id *Identity) PublicKeyBytes() ed25519.PublicKey {
	return id.priv.Public().(ed25519.PublicKey)
}

func (id *Identity) Sign(message []byte) string {
	return hex.EncodeToString(ed25519.Sign(id.priv, message))
}

func VerifySignatureStr(pubKeyHex, message, sigHex string) bool {
	pub, err := hex.DecodeString(pubKeyHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, []byte(message), sig)
}
