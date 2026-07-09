package service

import (
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"hirebridge/internal/store/repo"
)

func ingestTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:?_foreign_keys=1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.Exec(`PRAGMA journal_mode=WAL`)

	schema, err := os.ReadFile("../../internal/store/schema/migrations/001_initial.up.sql")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// vec0 native SQL functions are not loaded in test; CreateVirtualTables is skipped.
	// snapshots_fts is a virtual table, so create it without vec0 triggers.
	if _, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS snapshots_fts USING fts5(candidate_id, content)`); err != nil {
		t.Fatalf("create fts5: %v", err)
	}

	t.Cleanup(func() { db.Close() })
	return db
}

func TestSnapshotingest_SignedSnapshotAcceptedAndTamperedRejected(t *testing.T) {
	db := ingestTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	svc := NewIngestService(db, logger)

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	nodeID := repo.NewID()

	now := mustNow(t, db)
	db.Exec(`INSERT INTO nodes (id, user_id, node_type, endpoint_url, is_active, created_at, public_key)
		VALUES (?, NULL, 'JobOps', 'http://example.com', 1, ?, ?)`,
		nodeID, now, pub)

	payload := json.RawMessage(`{"name":"Jane","title":"Engineer"}`)
	msg := []byte(payload)
	sig := ed25519.Sign(priv, msg)
	sigHex := hex.EncodeToString(sig)

	input := &SnapshotInput{
		CandidateID: "c1",
		Payload:     payload,
		Signature:   sigHex,
	}
	if err := svc.Process(nodeID, input); err != nil {
		t.Fatalf("valid signed snapshot rejected: %v", err)
	}

	// verify it was stored
	snap, err := repo.GetSnapshotByCandidate(db, "c1")
	if err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	if snap == nil {
		t.Fatal("snapshot not found after ingest")
	}
	if snap.PayloadJSON != string(payload) {
		t.Fatalf("payload mismatch: %q", snap.PayloadJSON)
	}

	// test tampered payload is rejected
	tampered := json.RawMessage(`{"name":"John"}`)
	input2 := &SnapshotInput{
		CandidateID: "c2",
		Payload:     tampered,
		Signature:   sigHex,
	}
	if err := svc.Process(nodeID, input2); err == nil {
		t.Fatal("tampered payload should be rejected")
	}
}

func TestGetTalentProfile_ReturnsHexVerifiableKey(t *testing.T) {
	db := ingestTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	searchSvc := NewSearchService(db, logger, 384)

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	nodeID := repo.NewID()
	candidateID := "cp1"

	payload := json.RawMessage(`{"skills":["Go","Rust"]}`)
	msg := []byte(payload)
	sig := ed25519.Sign(priv, msg)

	now := mustNow(t, db)
	db.Exec(`INSERT INTO nodes (id, user_id, node_type, endpoint_url, is_active, created_at, public_key)
		VALUES (?, NULL, 'JobOps', 'http://example.com', 1, ?, ?)`,
		nodeID, now, pub)
	db.Exec(`INSERT INTO snapshots (id, node_id, candidate_id, payload_json, signature, ingested_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		repo.NewID(), nodeID, candidateID, string(payload), sig, now)

	profile, err := searchSvc.GetTalentProfile(candidateID)
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if profile == nil {
		t.Fatal("profile not found")
	}

	expectedPubHex := hex.EncodeToString(pub)
	if profile.PublicKey != expectedPubHex {
		t.Fatalf("public_key: expected hex %s, got %s", expectedPubHex, profile.PublicKey)
	}

	actualPub, err := hex.DecodeString(profile.PublicKey)
	if err != nil {
		t.Fatalf("public key not valid hex: %v", err)
	}

	actualSig, err := hex.DecodeString(profile.Signature)
	if err != nil {
		t.Fatalf("signature not valid hex: %v", err)
	}

	if !ed25519.Verify(actualPub, []byte(profile.Payload), actualSig) {
		t.Fatal("signature returned by get_talent_profile does not verify against payload")
	}
}

func mustNow(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var now int64
	db.QueryRow("SELECT CAST(strftime('%s','now') AS INTEGER)").Scan(&now)
	return now
}

func TestRevokeNode_RemovesDataFromIndex(t *testing.T) {
	db := ingestTestDB(t)

	nodeID := repo.NewID()
	now := mustNow(t, db)
	db.Exec(`INSERT INTO nodes (id, user_id, node_type, endpoint_url, is_active, created_at)
		VALUES (?, NULL, 'JobOps', 'http://example.com', 1, ?)`, nodeID, now)

	candidateID := "revoke-test-1"
	payload := json.RawMessage(`{"name":"Test Candidate"}`)
	snapID := repo.NewID()

	db.Exec(`INSERT INTO snapshots (id, node_id, candidate_id, payload_json, ingested_at)
		VALUES (?, ?, ?, ?, ?)`, snapID, nodeID, candidateID, string(payload), now)

	repo.ReplaceFTS5Row(db, candidateID, string(payload))

	// verify data exists before revoke
	snap, err := repo.GetSnapshotByCandidate(db, candidateID)
	if err != nil || snap == nil {
		t.Fatalf("snapshot should exist before revoke: err=%v snap=%v", err, snap)
	}

	// revoke the node
	if err := repo.RevokeNode(db, nodeID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// verify snapshot is gone
	snap2, err := repo.GetSnapshotByCandidate(db, candidateID)
	if err != nil {
		t.Fatalf("get snapshot after revoke: %v", err)
	}
	if snap2 != nil {
		t.Fatal("snapshot should be nil after node revoke")
	}

	// verify FTS5 row is gone
	var ftsCount int
	db.QueryRow(`SELECT count(*) FROM snapshots_fts WHERE candidate_id = ?`, candidateID).Scan(&ftsCount)
	if ftsCount != 0 {
		t.Fatalf("FTS5 row should be deleted, got %d rows", ftsCount)
	}

	// verify node is inactive
	n, err := repo.NodeByID(db, nodeID)
	if err != nil || n == nil {
		t.Fatalf("node not found: %v", err)
	}
	if n.IsActive {
		t.Fatal("node should be inactive after revoke")
	}
}
