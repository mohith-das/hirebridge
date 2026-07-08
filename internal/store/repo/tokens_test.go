package repo_test

import (
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"hirebridge/internal/store/repo"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	os.MkdirAll("testdata", 0755)
	db, err := sql.Open("sqlite3", "testdata/tokens_test.db?_foreign_keys=1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatalf("wal: %v", err)
	}
	// Minimal schema subset for magic_tokens
	db.Exec(`CREATE TABLE IF NOT EXISTS magic_tokens (
		token_hash       TEXT PRIMARY KEY,
		user_id          TEXT,
		device_code_hash TEXT,
		expires_at       INTEGER NOT NULL,
		used_at          INTEGER
	)`)
	t.Cleanup(func() { db.Close() })
	return db
}

func TestConsumeMagicToken_Fresh(t *testing.T) {
	db := testDB(t)
	token := repo.GenerateToken()
	uid := "user-1"
	if err := repo.InsertMagicToken(db, token, &uid, nil, 15*time.Minute); err != nil {
		t.Fatalf("insert: %v", err)
	}
	mt, consumed, err := repo.ConsumeMagicToken(db, token)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if !consumed || mt == nil {
		t.Fatal("expected consumed=true for fresh token")
	}
	if !mt.UsedAt.Valid {
		t.Fatal("expected used_at to be set")
	}
}

func TestConsumeMagicToken_Replay(t *testing.T) {
	db := testDB(t)
	token := repo.GenerateToken()
	uid := "user-2"
	if err := repo.InsertMagicToken(db, token, &uid, nil, 15*time.Minute); err != nil {
		t.Fatalf("insert: %v", err)
	}
	_, consumed, err := repo.ConsumeMagicToken(db, token)
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if !consumed {
		t.Fatal("first consume should succeed")
	}
	_, consumed, err = repo.ConsumeMagicToken(db, token)
	if err != nil {
		t.Fatalf("second consume: %v", err)
	}
	if consumed {
		t.Fatal("replay consume must return consumed=false")
	}
}

func TestConsumeMagicToken_Expired(t *testing.T) {
	db := testDB(t)
	token := repo.GenerateToken()
	uid := "user-3"
	if err := repo.InsertMagicToken(db, token, &uid, nil, -1*time.Second); err != nil {
		t.Fatalf("insert: %v", err)
	}
	_, consumed, err := repo.ConsumeMagicToken(db, token)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if consumed {
		t.Fatal("expired token must return consumed=false")
	}
}
