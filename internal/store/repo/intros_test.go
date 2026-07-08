package repo_test

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"hirebridge/internal/store/repo"
)

func introsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:?_foreign_keys=1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.Exec(`CREATE TABLE IF NOT EXISTS introduction_requests (
		id TEXT PRIMARY KEY, candidate_id TEXT NOT NULL, recruiter_user_id TEXT NOT NULL,
		node_id TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'queued',
		created_at INTEGER NOT NULL, delivered_at INTEGER
	)`)
	t.Cleanup(func() { db.Close() })
	return db
}

func TestInsertIntroductionRequest(t *testing.T) {
	db := introsTestDB(t)
	id := repo.NewID()
	if err := repo.InsertIntroductionRequest(db, id, "c-1", "ru-1", "n-1"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var gotID, status string
	if err := db.QueryRow("SELECT id, status FROM introduction_requests WHERE id=?", id).Scan(&gotID, &status); err != nil {
		t.Fatalf("select: %v", err)
	}
	if gotID != id {
		t.Errorf("expected id %s, got %s", id, gotID)
	}
	if status != "queued" {
		t.Errorf("expected status queued, got %s", status)
	}
}

func TestInsertIntroductionRequest_UniqueIDs(t *testing.T) {
	db := introsTestDB(t)
	id1 := repo.NewID()
	id2 := repo.NewID()
	if err := repo.InsertIntroductionRequest(db, id1, "c-1", "ru-1", "n-1"); err != nil {
		t.Fatalf("insert1: %v", err)
	}
	if err := repo.InsertIntroductionRequest(db, id2, "c-1", "ru-1", "n-1"); err != nil {
		t.Fatalf("insert2: %v", err)
	}
	if id1 == id2 {
		t.Error("NewID must produce unique ids")
	}

	var count int
	db.QueryRow("SELECT count(*) FROM introduction_requests").Scan(&count)
	if count != 2 {
		t.Errorf("expected 2 rows, got %d", count)
	}
}
