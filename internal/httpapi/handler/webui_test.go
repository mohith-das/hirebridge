package handler_test

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"hirebridge/internal/httpapi/handler"
	"hirebridge/internal/httpapi/middleware"
)

func tempDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:?_foreign_keys=1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.Exec(`PRAGMA journal_mode=WAL`)

	schema, err := os.ReadFile("../../../internal/store/schema/migrations/001_initial.up.sql")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	t.Cleanup(func() { db.Close() })
	return db
}

func TestGenerateAPIKey_CreatesToken(t *testing.T) {
	db := tempDB(t)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	h := &handler.WebUIHandler{
		DB:       db,
		BaseURL:  "http://localhost",
		Logger:   logger,
		StaleAge: 90 * time.Second,
	}

	seedUserID := "test-u-1"
	now := time.Now().Unix()
	db.Exec(`INSERT INTO users (id, email, created_at) VALUES (?, ?, ?)`, seedUserID, "t@test.com", now)

	req := httptest.NewRequest("POST", "/dashboard/recruiter/apikey", nil)
	ctx := context.WithValue(req.Context(), middleware.UserIDKey, seedUserID)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	h.GenerateAPIKey(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	bodyStr := string(body[:n])
	resp.Body.Close()

	if !strings.Contains(bodyStr, "not be shown again") {
		t.Fatal("response missing 'not be shown again' warning")
	}
	if strings.Contains(bodyStr, "No MCP API key generated yet") {
		t.Fatal("response should not show 'No MCP API key' placeholder")
	}

	var count int
	db.QueryRow(`SELECT count(*) FROM api_tokens WHERE label='mcp-api' AND scope='talent:search' AND user_id=?`,
		seedUserID).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 mcp-api token, got %d", count)
	}

	var tokenHash string
	db.QueryRow(`SELECT token_hash FROM api_tokens WHERE label='mcp-api' AND user_id=?`, seedUserID).Scan(&tokenHash)
	if tokenHash == "" {
		t.Fatal("minted token has empty token_hash")
	}

	// Verify that a 64-char hex token appears exactly once in the body (inside <pre>)
	// Look after the "mcp-key" pre element
	after := strings.Index(bodyStr, `id="mcp-key"`)
	if after < 0 {
		t.Fatal("missing mcp-key element in response")
	}
	preClose := strings.Index(bodyStr[after:], "</pre>")
	if preClose < 0 {
		t.Fatal("missing </pre> after mcp-key")
	}
	inner := bodyStr[after+strings.Index(bodyStr[after:], ">")+1 : after+preClose]
	inner = strings.TrimSpace(inner)
	if len(inner) != 64 || !isHex(inner) {
		t.Fatalf("mcp-key content is not a 64-char hex token: len=%d content=%.64s", len(inner), inner)
	}
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return len(s) > 0
}
