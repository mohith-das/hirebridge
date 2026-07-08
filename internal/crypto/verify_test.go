package crypto_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"

	"hirebridge/internal/crypto"
)

func TestVerifySignature_Valid(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	msg := []byte(`{"name":"Jane","title":"Engineer"}`)
	sig := ed25519.Sign(priv, msg)
	sigHex := hex.EncodeToString(sig)

	ok, err := crypto.VerifySignature(pub, msg, sigHex)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected valid signature to verify")
	}
}

func TestVerifySignature_TamperedPayload(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	msg := []byte(`{"name":"Jane"}`)
	sig := ed25519.Sign(priv, msg)
	sigHex := hex.EncodeToString(sig)

	tampered := []byte(`{"name":"John"}`)
	ok, err := crypto.VerifySignature(pub, tampered, sigHex)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("tampered payload should not verify")
	}
}

func TestVerifySignature_WrongKey(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	msg := []byte(`data`)
	sig := ed25519.Sign(priv, msg)
	sigHex := hex.EncodeToString(sig)

	ok, err := crypto.VerifySignature(otherPub, msg, sigHex)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("wrong key should not verify")
	}
}

func TestVerifySignature_MalformedHex(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, err := crypto.VerifySignature(pub, []byte("x"), "not-hex")
	if err == nil {
		t.Fatal("expected error for malformed hex")
	}
}
