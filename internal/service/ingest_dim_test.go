package service_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"testing"

	"hirebridge/internal/service"
)

// fakeLogger discards everything; we don't assert on log output here.
func fakeLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// helper: build a fixed-dim vector.
func vec(dim int) []float64 {
	out := make([]float64, dim)
	for i := range out {
		out[i] = float64(i) * 0.001
	}
	return out
}

// TestIngest_RejectsMismatchedEmbeddingDim asserts the fix-4 contract: an
// embedding whose inner dimension differs from HB_EMBED_DIM must be rejected
// with a typed ErrEmbedDimMismatch that the HTTP layer surfaces as 400.
func TestIngest_RejectsMismatchedEmbeddingDim(t *testing.T) {
	// Use the canonical 384-dim default; provide a 256-dim vector.
	const wantDim = 384
	const gotDim = 256

	db := openIngestDB(t)
	logger := fakeLogger()

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	nodeID := seedNode(t, db, pub)

	payload := json.RawMessage(`{"name":"Mismatched"}`)
	sigHex := signPayload(t, priv, payload)

	input := &service.SnapshotInput{
		CandidateID: "c-bad-dim",
		Payload:     payload,
		Signature:   sigHex,
		Embedding:   [][]float64{vec(gotDim)},
	}
	svc := service.NewIngestService(db, logger, wantDim)

	err := svc.Process(nodeID, input)
	if err == nil {
		t.Fatal("expected dim mismatch error, got nil")
	}
	if !errors.Is(err, service.ErrEmbedDimMismatch) {
		t.Fatalf("expected ErrEmbedDimMismatch, got %v", err)
	}
	if got, want := err.Error(), "embedding dimension mismatch: got 256, want 384"; got != want {
		t.Errorf("error message: got %q, want %q", got, want)
	}

	// Snapshot must NOT have been written.
	if exists := snapshotExists(t, db, "c-bad-dim"); exists {
		t.Error("dim-mismatched snapshot should NOT be persisted")
	}
}

// TestIngest_AcceptsCorrectDimEmbedding is the positive control: a vector
// of exactly the configured dimension must pass through and be stored.
func TestIngest_AcceptsCorrectDimEmbedding(t *testing.T) {
	const wantDim = 384

	db := openIngestDB(t)
	logger := fakeLogger()

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	nodeID := seedNode(t, db, pub)

	payload := json.RawMessage(`{"name":"Correct"}`)
	sigHex := signPayload(t, priv, payload)

	input := &service.SnapshotInput{
		CandidateID: "c-good-dim",
		Payload:     payload,
		Signature:   sigHex,
		Embedding:   [][]float64{vec(wantDim)},
	}
	svc := service.NewIngestService(db, logger, wantDim)

	if err := svc.Process(nodeID, input); err != nil {
		t.Fatalf("expected success at correct dim, got %v", err)
	}
	if !snapshotExists(t, db, "c-good-dim") {
		t.Error("correct-dim snapshot should be persisted")
	}
}

// TestIngest_NoEmbeddingSkipsDimCheck verifies the legacy "no embedding
// provided" path is unchanged.
func TestIngest_NoEmbeddingSkipsDimCheck(t *testing.T) {
	const wantDim = 384

	db := openIngestDB(t)
	logger := fakeLogger()

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	nodeID := seedNode(t, db, pub)

	payload := json.RawMessage(`{"name":"NoEmbed"}`)
	sigHex := signPayload(t, priv, payload)

	input := &service.SnapshotInput{
		CandidateID: "c-no-embed",
		Payload:     payload,
		Signature:   sigHex,
		Embedding:   nil,
	}
	svc := service.NewIngestService(db, logger, wantDim)

	if err := svc.Process(nodeID, input); err != nil {
		t.Fatalf("expected success without embedding, got %v", err)
	}
}

// TestIngest_ZeroEmbedDimDisablesCheck: when EmbedDim=0 (legacy callers),
// no validation runs even with a wrong-dim vector.
func TestIngest_ZeroEmbedDimDisablesCheck(t *testing.T) {
	db := openIngestDB(t)
	logger := fakeLogger()

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	nodeID := seedNode(t, db, pub)

	payload := json.RawMessage(`{"name":"LegacyDim"}`)
	sigHex := signPayload(t, priv, payload)

	input := &service.SnapshotInput{
		CandidateID: "c-legacy-dim",
		Payload:     payload,
		Signature:   sigHex,
		Embedding:   [][]float64{vec(123)},
	}
	svc := service.NewIngestService(db, logger, 0)

	if err := svc.Process(nodeID, input); err != nil {
		t.Fatalf("EmbedDim=0 should accept any dim: %v", err)
	}
}