package service_test

import (
	"crypto/ed25519"
	"database/sql"
	"encoding/hex"
	"os"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"hirebridge/internal/store/repo"
)

// openIngestDB returns an in-memory SQLite with the canonical migrations
// applied, so the IngestService is exercised against the real schema.
func openIngestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:?_foreign_keys=1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)

	for _, mig := range []string{
		"../../internal/store/schema/migrations/001_initial.up.sql",
		"../../internal/store/schema/migrations/002_federation.up.sql",
		"../../internal/store/schema/migrations/003_intro_secret.up.sql",
		"../../internal/store/schema/migrations/004_intro_recruiter.up.sql",
	} {
		schema, err := os.ReadFile(mig)
		if err != nil {
			t.Fatalf("read %s: %v", mig, err)
		}
		if _, err := db.Exec(string(schema)); err != nil {
			t.Fatalf("apply %s: %v", mig, err)
		}
	}

	t.Cleanup(func() { db.Close() })
	return db
}

// seedNode inserts an active JobOps node with the given ed25519 public key
// and returns its id.
func seedNode(t *testing.T, db *sql.DB, pub ed25519.PublicKey) string {
	t.Helper()
	nodeID := repo.NewID()
	if _, err := db.Exec(
		`INSERT INTO nodes (id, user_id, node_type, endpoint_url, is_active, created_at, public_key)
		 VALUES (?, NULL, 'JobOps', 'http://test.local', 1, ?, ?)`,
		nodeID, time.Now().Unix(), pub,
	); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	return nodeID
}

func signPayload(t *testing.T, priv ed25519.PrivateKey, payload []byte) string {
	t.Helper()
	sig := ed25519.Sign(priv, payload)
	return hex.EncodeToString(sig)
}

func snapshotExists(t *testing.T, db *sql.DB, candidateID string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM snapshots WHERE candidate_id = ?`, candidateID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n > 0
}