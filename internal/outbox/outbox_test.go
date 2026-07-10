package outbox

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"hirebridge/internal/store/repo"
)

// outboxTestDB constructs an in-memory SQLite with the full schema. It uses
// the same migration files as production, so tests catch schema/contract
// drift.
func outboxTestDB(t *testing.T) *sql.DB {
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

// seedUser inserts a user row if absent (idempotent).
func seedUser(t *testing.T, db *sql.DB, userID string) {
	t.Helper()
	now := time.Now().Unix()
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO users (id, email, created_at) VALUES (?, ?, ?)`,
		userID, userID+"@test.local", now,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

// seedTwoNodes creates a JobOps node (the snapshot's owner) and a LivingCV
// node (the delivery target) under the same user. Returns (jobopsNodeID,
// livingcvNodeID). The user row is created automatically.
func seedTwoNodes(t *testing.T, db *sql.DB, userID, jobopsURL, livingcvURL, introSecret string) (string, string) {
	t.Helper()
	seedUser(t, db, userID)
	now := time.Now().Unix()

	jobops := repo.NewID()
	if _, err := db.Exec(
		`INSERT INTO nodes (id, user_id, node_type, endpoint_url, is_active, created_at)
		 VALUES (?, ?, 'JobOps', ?, 1, ?)`,
		jobops, userID, jobopsURL, now,
	); err != nil {
		t.Fatalf("insert jobops node: %v", err)
	}

	livingcv := repo.NewID()
	if _, err := db.Exec(
		`INSERT INTO nodes (id, user_id, node_type, endpoint_url, is_active, created_at, intro_secret)
		 VALUES (?, ?, 'LivingCV', ?, 1, ?, ?)`,
		livingcv, userID, livingcvURL, now, introSecret,
	); err != nil {
		t.Fatalf("insert livingcv node: %v", err)
	}
	return jobops, livingcv
}

func newTestWorker(t *testing.T, db *sql.DB) *Worker {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewWorker(db, logger, Config{PollInterval: 10 * time.Millisecond})
}

func TestSignIntro_DeterministicHMAC(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	body := []byte(`{"hello":"world"}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	if got := SignIntro(secret, body); got != want {
		t.Fatalf("SignIntro mismatch: got %s, want %s", got, want)
	}
}

func TestOutbox_DeliversSignedPayloadToHittingInbox(t *testing.T) {
	db := outboxTestDB(t)
	const introSecret = "deadbeefcafebabe0123456789abcdef0123456789abcdef0123456789abcdef"

	// A local httptest server plays the LivingCV /api/inbox receiver.
	var received atomic.Int32
	var lastSig string
	var lastBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/inbox" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		lastBody = body
		lastSig = r.Header.Get(SignatureHeader)
		received.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)

	jobopsNodeID, _ := seedTwoNodes(t, db, "user-1", "http://jobops.local", server.URL, introSecret)

	candidateID := "cand-1"
	if _, err := db.Exec(
		`INSERT INTO snapshots (id, node_id, candidate_id, payload_json, ingested_at)
		 VALUES (?, ?, ?, '{}', ?)`,
		repo.NewID(), jobopsNodeID, candidateID, time.Now().Unix(),
	); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	requestID := repo.NewID()
	recruiterID := "recruiter-user-1"
	seedUser(t, db, recruiterID)
	if err := repo.InsertIntroductionRequest(db, requestID, candidateID, recruiterID, jobopsNodeID, "Alex", "[email protected]", "Acme"); err != nil {
		t.Fatalf("queue intro: %v", err)
	}

	w := newTestWorker(t, db)
	w.SetHTTP(server.Client())
	w.DrainOnce(context.Background())

	if got := received.Load(); got != 1 {
		t.Fatalf("expected exactly one delivery, got %d", got)
	}

	// Verify signature.
	wantSig := SignIntro(introSecret, lastBody)
	if !hmac.Equal([]byte(wantSig), []byte(lastSig)) {
		t.Fatalf("signature mismatch: got %s, want %s (HMAC of %d bytes)", lastSig, wantSig, len(lastBody))
	}

	// Verify body shape.
	var payload map[string]any
	if err := json.Unmarshal(lastBody, &payload); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	if payload["request_id"] != requestID {
		t.Errorf("request_id: got %v, want %s", payload["request_id"], requestID)
	}
	if payload["candidate_id"] != candidateID {
		t.Errorf("candidate_id: got %v, want %s", payload["candidate_id"], candidateID)
	}
	identity, ok := payload["recruiter_identity"].(map[string]any)
	if !ok {
		t.Fatalf("recruiter_identity missing or not object: %v", payload["recruiter_identity"])
	}
	if identity["name"] != "Alex" || identity["email"] != "[email protected]" || identity["company"] != "Acme" {
		t.Errorf("recruiter_identity: %+v", identity)
	}
	if _, ok := payload["ts"].(string); !ok {
		t.Errorf("ts missing or not string: %v", payload["ts"])
	}

	// Verify row state.
	row, err := repo.IntroductionRequestByID(db, requestID)
	if err != nil {
		t.Fatalf("lookup row: %v", err)
	}
	if row.Status != "delivered" {
		t.Errorf("expected delivered, got %s", row.Status)
	}
	if !row.DeliveredAt.Valid {
		t.Error("expected delivered_at stamped")
	}
}

func TestOutbox_RetriesOnNon2xxThenSucceeds(t *testing.T) {
	db := outboxTestDB(t)
	const introSecret = "f00d"

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	jobopsNodeID, _ := seedTwoNodes(t, db, "user-1", "http://jobops.local", server.URL, introSecret)

	candidateID := "cand-2"
	if _, err := db.Exec(
		`INSERT INTO snapshots (id, node_id, candidate_id, payload_json, ingested_at)
		 VALUES (?, ?, ?, '{}', ?)`,
		repo.NewID(), jobopsNodeID, candidateID, time.Now().Unix(),
	); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	requestID := repo.NewID()
	recruiterID := "recruiter-user-2"
	seedUser(t, db, recruiterID)
	if err := repo.InsertIntroductionRequest(db, requestID, candidateID, recruiterID, jobopsNodeID, "Alex", "[email protected]", ""); err != nil {
		t.Fatalf("queue intro: %v", err)
	}

	// Use a tiny backoff schedule so the second attempt is immediately eligible.
	w := newTestWorker(t, db)
	w.cfg.Backoffs = []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond}
	w.SetHTTP(server.Client())

	// First attempt: 500 → retrying with attempts=1.
	w.DrainOnce(context.Background())
	row, err := repo.IntroductionRequestByID(db, requestID)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if row.Status != "retrying" {
		t.Fatalf("expected retrying after first failure, got %s", row.Status)
	}
	if row.Attempts != 1 {
		t.Errorf("attempts: got %d, want 1", row.Attempts)
	}
	if !row.LastError.Valid || !strings.Contains(row.LastError.String, "500") {
		t.Errorf("last_error: %v", row.LastError)
	}
	if !row.NextAttemptAt.Valid {
		t.Error("expected next_attempt_at scheduled")
	}

	// Wait for backoff to elapse, then drain again → 200 → delivered.
	time.Sleep(20 * time.Millisecond)
	w.DrainOnce(context.Background())

	row, _ = repo.IntroductionRequestByID(db, requestID)
	if row.Status != "delivered" {
		t.Errorf("expected delivered after retry, got %s", row.Status)
	}
	if calls.Load() != 2 {
		t.Errorf("expected 2 HTTP calls, got %d", calls.Load())
	}
}

func TestOutbox_GivesUpAfterMaxAttempts(t *testing.T) {
	db := outboxTestDB(t)
	const introSecret = "f00d"

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)

	jobopsNodeID, _ := seedTwoNodes(t, db, "user-1", "http://jobops.local", server.URL, introSecret)

	candidateID := "cand-3"
	if _, err := db.Exec(
		`INSERT INTO snapshots (id, node_id, candidate_id, payload_json, ingested_at)
		 VALUES (?, ?, ?, '{}', ?)`,
		repo.NewID(), jobopsNodeID, candidateID, time.Now().Unix(),
	); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	requestID := repo.NewID()
	recruiterID := "recruiter-user-3"
	seedUser(t, db, recruiterID)
	if err := repo.InsertIntroductionRequest(db, requestID, candidateID, recruiterID, jobopsNodeID, "Alex", "[email protected]", ""); err != nil {
		t.Fatalf("queue intro: %v", err)
	}

	w := newTestWorker(t, db)
	w.cfg.MaxAttempts = 3
	w.cfg.Backoffs = []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond}
	w.SetHTTP(server.Client())

	for i := 0; i < 5; i++ {
		w.DrainOnce(context.Background())
		time.Sleep(20 * time.Millisecond)
	}

	row, err := repo.IntroductionRequestByID(db, requestID)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if row.Status != "failed" {
		t.Errorf("expected failed after max attempts, got %s (attempts=%d)", row.Status, row.Attempts)
	}
	if row.Attempts != 3 {
		t.Errorf("expected exactly 3 attempts, got %d", row.Attempts)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("expected 3 HTTP calls, got %d", got)
	}
}

func TestOutbox_MarksUndeliverableWhenNoTargetNode(t *testing.T) {
	db := outboxTestDB(t)
	const introSecret = "f00d"

	// Only a JobOps node — no LivingCV target with endpoint_url + intro_secret.
	jobopsNodeID := repo.NewID()
	now := time.Now().Unix()
	userID := "user-orphan"
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO users (id, email, created_at) VALUES (?, ?, ?)`,
		userID, userID+"@test.local", now,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO nodes (id, user_id, node_type, endpoint_url, is_active, created_at)
		 VALUES (?, ?, 'JobOps', 'http://jobops.local', 1, ?)`,
		jobopsNodeID, userID, now,
	); err != nil {
		t.Fatalf("seed jobops: %v", err)
	}
	candidateID := "cand-orphan"
	if _, err := db.Exec(
		`INSERT INTO snapshots (id, node_id, candidate_id, payload_json, ingested_at)
		 VALUES (?, ?, ?, '{}', ?)`,
		repo.NewID(), jobopsNodeID, candidateID, now,
	); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	requestID := repo.NewID()
	recruiterID := "recruiter-user-orphan"
	seedUser(t, db, recruiterID)
	if err := repo.InsertIntroductionRequest(db, requestID, candidateID, recruiterID, jobopsNodeID, "Alex", "[email protected]", ""); err != nil {
		t.Fatalf("queue intro: %v", err)
	}

	w := newTestWorker(t, db)
	w.DrainOnce(context.Background())

	row, err := repo.IntroductionRequestByID(db, requestID)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if row.Status != "undeliverable" {
		t.Errorf("expected undeliverable, got %s", row.Status)
	}
	if !row.LastError.Valid || !strings.Contains(row.LastError.String, "no active LivingCV node") {
		t.Errorf("last_error: %v", row.LastError)
	}

	// Second drain must NOT re-attempt: undeliverable is excluded from
	// PendingIntroductionRequests.
	w.DrainOnce(context.Background())
	row, _ = repo.IntroductionRequestByID(db, requestID)
	if row.Status != "undeliverable" {
		t.Errorf("status drifted on second drain: %s", row.Status)
	}
}

func TestOutbox_SkipsLivingCVTargetWithoutIntroSecret(t *testing.T) {
	db := outboxTestDB(t)
	jobopsNodeID := repo.NewID()
	livingcvNoSecret := repo.NewID()
	now := time.Now().Unix()
	userID := "user-mixed"

	if _, err := db.Exec(
		`INSERT OR IGNORE INTO users (id, email, created_at) VALUES (?, ?, ?)`,
		userID, userID+"@test.local", now,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO nodes (id, user_id, node_type, endpoint_url, is_active, created_at)
		 VALUES (?, ?, 'JobOps', 'http://jobops.local', 1, ?)`,
		jobopsNodeID, userID, now,
	); err != nil {
		t.Fatalf("seed jobops: %v", err)
	}
	// LivingCV node present but intro_secret is empty → must NOT be picked.
	if _, err := db.Exec(
		`INSERT INTO nodes (id, user_id, node_type, endpoint_url, is_active, created_at, intro_secret)
		 VALUES (?, ?, 'LivingCV', 'http://livingcv.local', 1, ?, '')`,
		livingcvNoSecret, userID, now,
	); err != nil {
		t.Fatalf("seed livingcv: %v", err)
	}

	candidateID := "cand-mixed"
	if _, err := db.Exec(
		`INSERT INTO snapshots (id, node_id, candidate_id, payload_json, ingested_at)
		 VALUES (?, ?, ?, '{}', ?)`,
		repo.NewID(), jobopsNodeID, candidateID, now,
	); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	requestID := repo.NewID()
	recruiterID := "recruiter-user-mixed"
	seedUser(t, db, recruiterID)
	if err := repo.InsertIntroductionRequest(db, requestID, candidateID, recruiterID, jobopsNodeID, "Alex", "[email protected]", ""); err != nil {
		t.Fatalf("queue intro: %v", err)
	}

	w := newTestWorker(t, db)
	w.DrainOnce(context.Background())

	row, _ := repo.IntroductionRequestByID(db, requestID)
	if row.Status != "undeliverable" {
		t.Errorf("expected undeliverable (no usable LivingCV target), got %s", row.Status)
	}
}