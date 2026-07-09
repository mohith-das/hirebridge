package federation_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"hirebridge/internal/federation"
)

func fedTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:?_foreign_keys=1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)

	schema, err := os.ReadFile("../../internal/store/schema/migrations/001_initial.up.sql")
	if err != nil {
		t.Fatalf("read schema 001: %v", err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatalf("migrate 001: %v", err)
	}

	schema2, err := os.ReadFile("../../internal/store/schema/migrations/002_federation.up.sql")
	if err != nil {
		t.Fatalf("read schema 002: %v", err)
	}
	if _, err := db.Exec(string(schema2)); err != nil {
		t.Fatalf("migrate 002: %v", err)
	}

	t.Cleanup(func() { db.Close() })
	return db
}

func makeTestHandler(t *testing.T, db *sql.DB) *federation.Handler {
	return makeTestHandlerWithSecret(t, db, "")
}

func makeTestHandlerWithSecret(t *testing.T, db *sql.DB, joinSecret string) *federation.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	os.MkdirAll("testdata", 0755)
	ident, _ := federation.LoadOrCreateIdentity("testdata/fed_test_key.json")
	cfg := &federation.Config{
		Enabled:      true,
		Port:         ":0",
		InstanceName: "test-instance",
		JoinSecret:   joinSecret,
	}

	handler := federation.NewHandler(db, ident, logger, cfg)

	t.Cleanup(func() { os.Remove("testdata/fed_test_key.json") })
	return handler
}

func TestFedAuth_RejectsUnknownPublicKey(t *testing.T) {
	db := fedTestDB(t)
	handler := makeTestHandler(t, db)

	// Generate a random keypair not in the DB
	ident, err := federation.LoadOrCreateIdentity("testdata/fed_unknown_key.json")
	if err != nil {
		t.Fatalf("create identity: %v", err)
	}
	t.Cleanup(func() { os.Remove("testdata/fed_unknown_key.json") })

	body := []byte(`{"query":"test"}`)
	sig := ident.Sign(body)

	req := httptest.NewRequest("POST", "/fed/search", bytes.NewReader(body))
	req.Header.Set("X-Fed-Public-Key", ident.PublicKey)
	req.Header.Set("X-Fed-Signature", sig)

	w := httptest.NewRecorder()
	handler.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unknown peer, got %d: %s", w.Code, w.Body.String())
	}
}

func TestFedAuth_RejectsInactivePeer(t *testing.T) {
	db := fedTestDB(t)
	handler := makeTestHandler(t, db)

	ident, err := federation.LoadOrCreateIdentity("testdata/fed_inactive_key.json")
	if err != nil {
		t.Fatalf("create identity: %v", err)
	}
	t.Cleanup(func() { os.Remove("testdata/fed_inactive_key.json") })

	// Insert peer with is_active=0
	now := time.Now().Unix()
	db.Exec(
		`INSERT INTO federated_instances (id, name, endpoint_url, public_key, instance_key, is_active, last_seen_at, created_at)
		 VALUES (?, ?, ?, ?, ?, 0, ?, ?)`,
		"fed_inactive", "inactive", "http://inactive:8400", ident.PublicKey, ident.PublicKey, now, now,
	)

	body := []byte(`{"query":"test"}`)
	sig := ident.Sign(body)

	req := httptest.NewRequest("POST", "/fed/search", bytes.NewReader(body))
	req.Header.Set("X-Fed-Public-Key", ident.PublicKey)
	req.Header.Set("X-Fed-Signature", sig)

	w := httptest.NewRecorder()
	handler.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for inactive peer, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegister_RejectsOverwriteOfActivePeer(t *testing.T) {
	db := fedTestDB(t)
	handler := makeTestHandler(t, db)

	// Insert an active peer
	now := time.Now().Unix()
	originalKey := "abc123"
	originalURL := "http://original:8400"
	db.Exec(
		`INSERT INTO federated_instances (id, name, endpoint_url, public_key, instance_key, is_active, last_seen_at, created_at)
		 VALUES (?, ?, ?, ?, ?, 1, ?, ?)`,
		"fed_mypeer", "mypeer", originalURL, originalKey, originalKey, now, now,
	)

	// Try to register with same name via the handler
	registerBody := map[string]string{
		"instance_name": "mypeer",
		"endpoint_url":  "http://attacker:8400",
		"public_key":    "deadbeef",
	}
	body, _ := json.Marshal(registerBody)

	// Need a valid signature from ANY registered active peer to pass fedAuth.
	// We'll use the original peer's identity — but we don't have its private key.
	// The fedAuth check happens before handshake/register, so we need:
	// 1. A valid signature -> need any active peer's key pair
	// 2. The register check then rejects the name collision

	// Create an active peer whose keypair we control
	attackerIdent, _ := federation.LoadOrCreateIdentity("testdata/fed_attacker_key.json")
	t.Cleanup(func() { os.Remove("testdata/fed_attacker_key.json") })

	db.Exec(
		`INSERT OR REPLACE INTO federated_instances (id, name, endpoint_url, public_key, instance_key, is_active, last_seen_at, created_at)
		 VALUES (?, ?, ?, ?, ?, 1, ?, ?)`,
		"fed_attacker", "attacker", "http://attacker:8400", attackerIdent.PublicKey, attackerIdent.PublicKey, now, now,
	)

	// Now register with the attacker's key (valid, active) but try to claim "mypeer" name
	body, _ = json.Marshal(registerBody)
	sig := attackerIdent.Sign(body)

	req := httptest.NewRequest("POST", "/fed/register", bytes.NewReader(body))
	req.Header.Set("X-Fed-Public-Key", attackerIdent.PublicKey)
	req.Header.Set("X-Fed-Signature", sig)

	w := httptest.NewRecorder()
	handler.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for name collision, got %d: %s", w.Code, w.Body.String())
	}

	// Verify original peer was NOT overwritten
	var url, pubkey string
	var isActive bool
	err := db.QueryRow(
		`SELECT endpoint_url, public_key, is_active FROM federated_instances WHERE id = ?`,
		"fed_mypeer",
	).Scan(&url, &pubkey, &isActive)
	if err != nil {
		t.Fatalf("original peer not found: %v", err)
	}
	if url != originalURL || pubkey != originalKey || !isActive {
		t.Fatalf("original peer was modified: url=%s key=%s active=%v", url, pubkey, isActive)
	}
}

// doRegister sends a register request signed by the given identity, with an
// optional X-Fed-Join-Secret header. Returns the recorded response.
func doRegister(t *testing.T, h *federation.Handler, ident *federation.Identity, body []byte, joinSecret string) *httptest.ResponseRecorder {
	t.Helper()
	sig := ident.Sign(body)
	req := httptest.NewRequest("POST", "/fed/register", bytes.NewReader(body))
	req.Header.Set("X-Fed-Public-Key", ident.PublicKey)
	req.Header.Set("X-Fed-Signature", sig)
	if joinSecret != "" {
		req.Header.Set("X-Fed-Join-Secret", joinSecret)
	}
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	return w
}

func peerIsActive(t *testing.T, db *sql.DB, id string) bool {
	t.Helper()
	var isActive bool
	err := db.QueryRow(`SELECT is_active FROM federated_instances WHERE id = ?`, id).Scan(&isActive)
	if err != nil {
		t.Fatalf("peer lookup: %v", err)
	}
	return isActive
}

func TestJoinSecret_CorrectSecretActivatesPendingPeer(t *testing.T) {
	db := fedTestDB(t)
	handler := makeTestHandlerWithSecret(t, db, "shhh-c0rrect")

	ident, _ := federation.LoadOrCreateIdentity("testdata/fed_joinactive.json")
	t.Cleanup(func() { os.Remove("testdata/fed_joinactive.json") })

	body, _ := json.Marshal(map[string]string{
		"instance_name": "newbie",
		"endpoint_url":  "http://newbie:8400",
		"public_key":    ident.PublicKey,
	})

	w := doRegister(t, handler, ident, body, "shhh-c0rrect")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !peerIsActive(t, db, "fed_newbie") {
		t.Fatal("peer with correct join secret must be inserted active")
	}
}

func TestJoinSecret_WrongSecretStaysPending(t *testing.T) {
	db := fedTestDB(t)
	handler := makeTestHandlerWithSecret(t, db, "shhh-c0rrect")

	ident, _ := federation.LoadOrCreateIdentity("testdata/fed_joinwrong.json")
	t.Cleanup(func() { os.Remove("testdata/fed_joinwrong.json") })

	body, _ := json.Marshal(map[string]string{
		"instance_name": "wrongie",
		"endpoint_url":  "http://wrong:8400",
		"public_key":    ident.PublicKey,
	})

	w := doRegister(t, handler, ident, body, "shhh-WRONG")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if peerIsActive(t, db, "fed_wrongie") {
		t.Fatal("peer with wrong join secret must NOT be active")
	}
}

func TestJoinSecret_AbsentSecretStaysPending(t *testing.T) {
	db := fedTestDB(t)
	handler := makeTestHandlerWithSecret(t, db, "shhh-c0rrect")

	ident, _ := federation.LoadOrCreateIdentity("testdata/fed_joinabsent.json")
	t.Cleanup(func() { os.Remove("testdata/fed_joinabsent.json") })

	body, _ := json.Marshal(map[string]string{
		"instance_name": "absentie",
		"endpoint_url":  "http://absent:8400",
		"public_key":    ident.PublicKey,
	})

	w := doRegister(t, handler, ident, body, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if peerIsActive(t, db, "fed_absentie") {
		t.Fatal("peer with no secret header must NOT be active")
	}
}

func TestJoinSecret_UnsetConfigMeansAlwaysPending(t *testing.T) {
	db := fedTestDB(t)
	handler := makeTestHandlerWithSecret(t, db, "")

	ident, _ := federation.LoadOrCreateIdentity("testdata/fed_jounset.json")
	t.Cleanup(func() { os.Remove("testdata/fed_jounset.json") })

	body, _ := json.Marshal(map[string]string{
		"instance_name": "trusty",
		"endpoint_url":  "http://trusty:8400",
		"public_key":    ident.PublicKey,
	})

	w := doRegister(t, handler, ident, body, "any-value-still-pending")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if peerIsActive(t, db, "fed_trusty") {
		t.Fatal("with no configured secret, all peers must remain pending")
	}
}

func TestJoinSecret_DoesNotOverwriteActivePeer(t *testing.T) {
	db := fedTestDB(t)
	handler := makeTestHandlerWithSecret(t, db, "shhh-c0rrect")

	now := time.Now().Unix()
	db.Exec(
		`INSERT INTO federated_instances (id, name, endpoint_url, public_key, instance_key, is_active, last_seen_at, created_at)
		 VALUES (?, ?, ?, ?, ?, 1, ?, ?)`,
		"fed_already", "already", "http://already:8400", "key", "key", now, now,
	)

	ident, _ := federation.LoadOrCreateIdentity("testdata/fed_jexist.json")
	t.Cleanup(func() { os.Remove("testdata/fed_jexist.json") })

	body, _ := json.Marshal(map[string]string{
		"instance_name": "already",
		"endpoint_url":  "http://attacker:8400",
		"public_key":    ident.PublicKey,
	})

	w := doRegister(t, handler, ident, body, "shhh-c0rrect")
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if !peerIsActive(t, db, "fed_already") {
		t.Fatal("existing active peer must remain active")
	}
}
