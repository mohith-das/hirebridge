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
		created_at INTEGER NOT NULL, delivered_at INTEGER,
		recruiter_name TEXT NOT NULL DEFAULT '', recruiter_email TEXT NOT NULL DEFAULT '',
		recruiter_company TEXT, attempts INTEGER NOT NULL DEFAULT 0,
		last_error TEXT, next_attempt_at INTEGER
	)`)
	t.Cleanup(func() { db.Close() })
	return db
}

func TestInsertIntroductionRequest(t *testing.T) {
	db := introsTestDB(t)
	id := repo.NewID()
	if err := repo.InsertIntroductionRequest(db, id, "c-1", "ru-1", "n-1", "Alex", "[email protected]", "Acme"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var gotID, status, name, email, company string
	if err := db.QueryRow(
		"SELECT id, status, recruiter_name, recruiter_email, recruiter_company FROM introduction_requests WHERE id=?",
		id,
	).Scan(&gotID, &status, &name, &email, &company); err != nil {
		t.Fatalf("select: %v", err)
	}
	if gotID != id {
		t.Errorf("expected id %s, got %s", id, gotID)
	}
	if status != "queued" {
		t.Errorf("expected status queued, got %s", status)
	}
	if name != "Alex" || email != "[email protected]" || company != "Acme" {
		t.Errorf("recruiter fields: name=%q email=%q company=%q", name, email, company)
	}
}

func TestInsertIntroductionRequest_OptionalCompany(t *testing.T) {
	db := introsTestDB(t)
	id := repo.NewID()
	if err := repo.InsertIntroductionRequest(db, id, "c-1", "ru-1", "n-1", "Alex", "[email protected]", ""); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var company sql.NullString
	db.QueryRow(`SELECT recruiter_company FROM introduction_requests WHERE id=?`, id).Scan(&company)
	if company.Valid {
		t.Errorf("expected recruiter_company to be NULL when omitted, got %q", company.String)
	}
}

func TestInsertIntroductionRequest_UniqueIDs(t *testing.T) {
	db := introsTestDB(t)
	id1 := repo.NewID()
	id2 := repo.NewID()
	if err := repo.InsertIntroductionRequest(db, id1, "c-1", "ru-1", "n-1", "Alex", "[email protected]", ""); err != nil {
		t.Fatalf("insert1: %v", err)
	}
	if err := repo.InsertIntroductionRequest(db, id2, "c-1", "ru-1", "n-1", "Alex", "[email protected]", ""); err != nil {
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

func TestMarkIntroDelivered(t *testing.T) {
	db := introsTestDB(t)
	id := repo.NewID()
	if err := repo.InsertIntroductionRequest(db, id, "c-1", "ru-1", "n-1", "Alex", "[email protected]", ""); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := repo.MarkIntroDelivered(db, id); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}
	row, err := repo.IntroductionRequestByID(db, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.Status != "delivered" {
		t.Errorf("expected delivered, got %s", row.Status)
	}
	if !row.DeliveredAt.Valid {
		t.Error("expected delivered_at stamped")
	}
}

func TestMarkIntroRetrying(t *testing.T) {
	db := introsTestDB(t)
	id := repo.NewID()
	if err := repo.InsertIntroductionRequest(db, id, "c-1", "ru-1", "n-1", "Alex", "[email protected]", ""); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := repo.MarkIntroRetrying(db, id, "boom", 999); err != nil {
		t.Fatalf("mark retrying: %v", err)
	}
	row, err := repo.IntroductionRequestByID(db, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.Status != "retrying" {
		t.Errorf("expected retrying, got %s", row.Status)
	}
	if row.Attempts != 1 {
		t.Errorf("expected attempts=1, got %d", row.Attempts)
	}
	if !row.LastError.Valid || row.LastError.String != "boom" {
		t.Errorf("expected last_error=boom, got %v", row.LastError)
	}
	if !row.NextAttemptAt.Valid || row.NextAttemptAt.Int64 != 999 {
		t.Errorf("expected next_attempt_at=999, got %v", row.NextAttemptAt)
	}
}

func TestMarkIntroUndeliverable(t *testing.T) {
	db := introsTestDB(t)
	id := repo.NewID()
	if err := repo.InsertIntroductionRequest(db, id, "c-1", "ru-1", "n-1", "Alex", "[email protected]", ""); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := repo.MarkIntroUndeliverable(db, id, "no target node"); err != nil {
		t.Fatalf("mark undeliverable: %v", err)
	}
	row, err := repo.IntroductionRequestByID(db, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.Status != "undeliverable" {
		t.Errorf("expected undeliverable, got %s", row.Status)
	}
}

func TestPendingIntroductionRequests(t *testing.T) {
	db := introsTestDB(t)
	now := int64(1_000)

	idReady := repo.NewID()
	idFuture := repo.NewID()
	idDelivered := repo.NewID()
	idUndeliverable := repo.NewID()
	if err := repo.InsertIntroductionRequest(db, idReady, "c1", "ru-1", "n-1", "Alex", "[email protected]", ""); err != nil {
		t.Fatalf("insert ready: %v", err)
	}
	if err := repo.InsertIntroductionRequest(db, idFuture, "c1", "ru-1", "n-1", "Alex", "[email protected]", ""); err != nil {
		t.Fatalf("insert future: %v", err)
	}
	if err := repo.InsertIntroductionRequest(db, idDelivered, "c1", "ru-1", "n-1", "Alex", "[email protected]", ""); err != nil {
		t.Fatalf("insert delivered: %v", err)
	}
	if err := repo.InsertIntroductionRequest(db, idUndeliverable, "c1", "ru-1", "n-1", "Alex", "[email protected]", ""); err != nil {
		t.Fatalf("insert undeliverable: %v", err)
	}

	// Schedule idFuture to be ready only after `now`.
	if err := repo.MarkIntroRetrying(db, idFuture, "transient", now+500); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if err := repo.MarkIntroDelivered(db, idDelivered); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if err := repo.MarkIntroUndeliverable(db, idUndeliverable, "no target"); err != nil {
		t.Fatalf("undeliverable: %v", err)
	}

	rows, err := repo.PendingIntroductionRequests(db, now, 100)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != idReady {
		t.Fatalf("expected only the ready row, got %+v", rows)
	}
}