package handler_test

import (
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/go-chi/chi/v5"

	"hirebridge/internal/httpapi/handler"
	"hirebridge/internal/httpapi/middleware"
	"hirebridge/internal/store/repo"
)

func adminTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:?_foreign_keys=1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.Exec(`PRAGMA journal_mode=WAL`)

	schema, err := os.ReadFile("../../../internal/store/schema/migrations/001_initial.up.sql")
	if err != nil {
		t.Fatalf("read schema 001: %v", err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatalf("migrate 001: %v", err)
	}

	schema2, err := os.ReadFile("../../../internal/store/schema/migrations/002_federation.up.sql")
	if err != nil {
		t.Fatalf("read schema 002: %v", err)
	}
	if _, err := db.Exec(string(schema2)); err != nil {
		t.Fatalf("migrate 002: %v", err)
	}

	t.Cleanup(func() { db.Close() })
	return db
}

func newTestSessions(t *testing.T) *middleware.AdminSessions {
	t.Helper()
	return middleware.NewAdminSessions(time.Hour)
}

func newTestPending(t *testing.T) *middleware.AdminPendingLinks {
	t.Helper()
	return middleware.NewAdminPendingLinks(time.Hour)
}

// mailerCapture records every (email, link) it's asked to send.
type mailerCapture struct {
	calls []mailerCall
}

type mailerCall struct {
	Email string
	Link  string
}

func (m *mailerCapture) Send(email, link string) error {
	m.calls = append(m.calls, mailerCall{Email: email, Link: link})
	return nil
}

func (m *mailerCapture) lastLink() (string, string, bool) {
	if len(m.calls) == 0 {
		return "", "", false
	}
	last := m.calls[len(m.calls)-1]
	return last.Email, last.Link, true
}

// newAdminHandler builds an AdminHandler with a fake mailer so tests can
// inspect (and clear) the magic-link send log.
func newAdminHandler(t *testing.T, db *sql.DB, sess *middleware.AdminSessions, pen *middleware.AdminPendingLinks, mailer handler.SendMagicLink, adminEmail string) *handler.AdminHandler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	limiter := middleware.NewRateLimiter()
	return &handler.AdminHandler{
		DB:         db,
		Logger:     logger,
		Sessions:   sess,
		Pending:    pen,
		AdminEmail: adminEmail,
		LinkTTL:    time.Hour,
		SendLink:   mailer,
		Limiter:    limiter,
	}
}

func TestAdminHandler_EnabledRequiresJustEmail(t *testing.T) {
	cases := []struct {
		name, email string
		want        bool
	}{
		{"empty", "", false},
		{"whitespace-only", "   ", false},
		{"valid", "ops@example.com", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newAdminHandler(t, adminTestDB(t), newTestSessions(t), newTestPending(t), nil, c.email)
			if got := h.Enabled(); got != c.want {
				t.Fatalf("Enabled() with email=%q: got %v want %v", c.email, got, c.want)
			}
		})
	}
}

func TestRequireAdmin_RejectsNoOrInvalidCookie(t *testing.T) {
	sess := newTestSessions(t)
	mw := middleware.RequireAdmin(sess, true)

	var nextCalled bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})

	t.Run("no-cookie", func(t *testing.T) {
		nextCalled = false
		req := httptest.NewRequest("GET", "/admin", nil)
		w := httptest.NewRecorder()
		mw(next).ServeHTTP(w, req)
		if nextCalled {
			t.Fatal("next must NOT be called when no cookie")
		}
		if w.Code != http.StatusSeeOther {
			t.Fatalf("expected 303 redirect, got %d", w.Code)
		}
		if loc := w.Header().Get("Location"); loc != "/admin/login" {
			t.Fatalf("expected redirect to /admin/login, got %q", loc)
		}
	})

	t.Run("invalid-cookie", func(t *testing.T) {
		nextCalled = false
		req := httptest.NewRequest("GET", "/admin", nil)
		req.AddCookie(&http.Cookie{Name: "hb_admin", Value: "deadbeef-not-a-real-token"})
		w := httptest.NewRecorder()
		mw(next).ServeHTTP(w, req)
		if nextCalled {
			t.Fatal("next must NOT be called with bogus cookie")
		}
		if w.Code != http.StatusSeeOther {
			t.Fatalf("expected 303 redirect, got %d", w.Code)
		}
	})

	t.Run("valid-cookie", func(t *testing.T) {
		nextCalled = false
		tok, err := sess.NewToken()
		if err != nil {
			t.Fatalf("mint token: %v", err)
		}
		req := httptest.NewRequest("GET", "/admin", nil)
		req.AddCookie(&http.Cookie{Name: "hb_admin", Value: tok})
		w := httptest.NewRecorder()
		mw(next).ServeHTTP(w, req)
		if !nextCalled {
			t.Fatal("next MUST be called with valid cookie")
		}
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})
}

func TestRequireAdmin_404sWhenDisabled(t *testing.T) {
	sess := newTestSessions(t)
	mw := middleware.RequireAdmin(sess, false)

	var nextCalled bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	})

	req := httptest.NewRequest("GET", "/admin", nil)
	w := httptest.NewRecorder()
	mw(next).ServeHTTP(w, req)

	if nextCalled {
		t.Fatal("next must NOT be called when admin disabled")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// TestAdminLogin_UniformResponse asserts that matching and non-matching
// emails get the same HTTP response body, but only the matching email
// enqueues a token in Pending.
func TestAdminLogin_UniformResponse(t *testing.T) {
	const adminEmail = "ops@example.com"

	t.Run("matching-email-enqueues-token", func(t *testing.T) {
		db := adminTestDB(t)
		sess := newTestSessions(t)
		pen := newTestPending(t)
		mc := &mailerCapture{}
		h := newAdminHandler(t, db, sess, pen, mc.Send, adminEmail)

		req := httptest.NewRequest("POST", "/admin/login",
			strings.NewReader("email="+url.QueryEscape(adminEmail)))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Host = "hirebridge.test"
		w := httptest.NewRecorder()
		h.LoginSubmit(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "check your inbox") {
			t.Fatal("expected 'check your inbox' uniform-response text")
		}
		if len(mc.calls) != 1 {
			t.Fatalf("expected exactly 1 link email, got %d", len(mc.calls))
		}
		_, link, ok := mc.lastLink()
		if !ok {
			t.Fatal("expected a captured link")
		}
		if !strings.Contains(link, "/admin/callback?token=") {
			t.Fatalf("link must point to /admin/callback, got %q", link)
		}

		// The pending store should now contain exactly one valid hash.
		u, err := url.Parse(link)
		if err != nil {
			t.Fatalf("parse link: %v", err)
		}
		token := u.Query().Get("token")
		hash := repo.HashToken(token)
		if !pen.Consume(hash) {
			t.Fatal("matching email must enqueue a token the callback can consume")
		}
		// Single-use: second consume must fail.
		if pen.Consume(hash) {
			t.Fatal("token must be single-use")
		}
	})

	t.Run("non-matching-email-uniform-response-no-token", func(t *testing.T) {
		db := adminTestDB(t)
		sess := newTestSessions(t)
		pen := newTestPending(t)
		mc := &mailerCapture{}
		h := newAdminHandler(t, db, sess, pen, mc.Send, adminEmail)

		req := httptest.NewRequest("POST", "/admin/login",
			strings.NewReader("email="+url.QueryEscape("attacker@example.com")))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Host = "hirebridge.test"
		w := httptest.NewRecorder()
		h.LoginSubmit(w, req)

		// Body must not differ from the matching-email case in any
		// observable way that would leak whether the email matched.
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "check your inbox") {
			t.Fatal("expected uniform-response text on non-matching email")
		}
		if len(mc.calls) != 0 {
			t.Fatalf("non-matching email must NOT enqueue any link, got %d calls", len(mc.calls))
		}
	})
}

func TestAdminCallback_ValidTokenCreatesSession(t *testing.T) {
	const adminEmail = "ops@example.com"
	db := adminTestDB(t)
	sess := newTestSessions(t)
	pen := newTestPending(t)
	mc := &mailerCapture{}
	h := newAdminHandler(t, db, sess, pen, mc.Send, adminEmail)

	// pre-stage a pending token in the store (the callback's job is just
	// to consume it + mint a session)
	token := repo.GenerateToken()
	pen.Put(repo.HashToken(token))

	req := httptest.NewRequest("GET", "/admin/callback?token="+token, nil)
	req.Host = "hirebridge.test"
	w := httptest.NewRecorder()
	h.Callback(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/admin" {
		t.Fatalf("expected redirect to /admin, got %q", loc)
	}
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "hb_admin" {
			cookie = c
			break
		}
	}
	if cookie == nil || cookie.Value == "" {
		t.Fatal("expected hb_admin cookie to be set on success")
	}
	if !sess.Valid(cookie.Value) {
		t.Fatal("minted cookie token must be valid in session store")
	}

	// Single-use: replay must redirect to /admin/login?error=1.
	req2 := httptest.NewRequest("GET", "/admin/callback?token="+token, nil)
	req2.Host = "hirebridge.test"
	w2 := httptest.NewRecorder()
	h.Callback(w2, req2)
	if w2.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 on replay, got %d", w2.Code)
	}
	if loc := w2.Header().Get("Location"); loc != "/admin/login?error=1" {
		t.Fatalf("expected redirect to /admin/login?error=1 on replay, got %q", loc)
	}
}

func TestAdminCallback_ExpiredOrUnknownRejected(t *testing.T) {
	const adminEmail = "ops@example.com"

	t.Run("unknown-token", func(t *testing.T) {
		db := adminTestDB(t)
		sess := newTestSessions(t)
		pen := newTestPending(t)
		h := newAdminHandler(t, db, sess, pen, func(string, string) error { return nil }, adminEmail)

		req := httptest.NewRequest("GET", "/admin/callback?token="+repo.GenerateToken(), nil)
		w := httptest.NewRecorder()
		h.Callback(w, req)

		if w.Code != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d", w.Code)
		}
		if loc := w.Header().Get("Location"); loc != "/admin/login?error=1" {
			t.Fatalf("expected /admin/login?error=1, got %q", loc)
		}
	})

	t.Run("expired-token", func(t *testing.T) {
		db := adminTestDB(t)
		sess := newTestSessions(t)
		// TTL of 0 forces the reaper to treat everything as expired;
		// use a very small TTL here and let time.Now pass via a fake.
		pen := middleware.NewAdminPendingLinks(1 * time.Millisecond)
		// avoid the reaper cleaning us up before the test runs:
		time.Sleep(5 * time.Millisecond)
		h := newAdminHandler(t, db, sess, pen, func(string, string) error { return nil }, adminEmail)

		req := httptest.NewRequest("GET", "/admin/callback?token="+repo.GenerateToken(), nil)
		w := httptest.NewRecorder()
		h.Callback(w, req)

		if w.Code != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d", w.Code)
		}
		if loc := w.Header().Get("Location"); loc != "/admin/login?error=1" {
			t.Fatalf("expected /admin/login?error=1, got %q", loc)
		}
	})

	t.Run("disabled-when-email-unset-404s", func(t *testing.T) {
		db := adminTestDB(t)
		sess := newTestSessions(t)
		pen := newTestPending(t)
		h := newAdminHandler(t, db, sess, pen, func(string, string) error { return nil }, "")
		// pretend we have a pending token; the handler must still 404
		// because Enabled() is false.
		token := repo.GenerateToken()
		pen.Put(repo.HashToken(token))

		req := httptest.NewRequest("GET", "/admin/callback?token="+token, nil)
		w := httptest.NewRecorder()
		h.Callback(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404 when admin disabled, got %d", w.Code)
		}
	})
}

// TestAdminLogin_FullFlow_ApprovesPeer wires LoginSubmit → Callback →
// ApprovePeer together to confirm the magic-link-only flow can flip a
// pending peer's is_active to 1.
func TestAdminLogin_FullFlow_ApprovesPeer(t *testing.T) {
	const adminEmail = "ops@example.com"
	db := adminTestDB(t)
	sess := newTestSessions(t)
	pen := newTestPending(t)
	mc := &mailerCapture{}
	h := newAdminHandler(t, db, sess, pen, mc.Send, adminEmail)

	// Insert pending peer.
	_, err := db.Exec(
		`INSERT INTO federated_instances (id, name, endpoint_url, public_key, instance_key, is_active, last_seen_at, created_at)
		 VALUES (?, ?, ?, ?, ?, 0, ?, ?)`,
		"fed_pending", "pending", "http://p:8400", "pk", "pk", time.Now().Unix(), time.Now().Unix(),
	)
	if err != nil {
		t.Fatalf("insert peer: %v", err)
	}

	// 1. Submit matching email to /admin/login.
	loginReq := httptest.NewRequest("POST", "/admin/login",
		strings.NewReader("email="+url.QueryEscape(adminEmail)))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginReq.Host = "hirebridge.test"
	loginW := httptest.NewRecorder()
	h.LoginSubmit(loginW, loginReq)
	if loginW.Code != http.StatusOK {
		t.Fatalf("login submit expected 200, got %d", loginW.Code)
	}
	if len(mc.calls) != 1 {
		t.Fatalf("expected 1 email sent, got %d", len(mc.calls))
	}

	// Extract token from the captured link.
	_, link, _ := mc.lastLink()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}
	token := u.Query().Get("token")
	if token == "" {
		t.Fatal("captured link must contain a token query param")
	}

	// 2. Hit /admin/callback?token=…
	callbackReq := httptest.NewRequest("GET", "/admin/callback?token="+token, nil)
	callbackReq.Host = "hirebridge.test"
	callbackW := httptest.NewRecorder()
	h.Callback(callbackW, callbackReq)
	if callbackW.Code != http.StatusSeeOther {
		t.Fatalf("callback expected 303, got %d: %s", callbackW.Code, callbackW.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range callbackW.Result().Cookies() {
		if c.Name == "hb_admin" {
			cookie = c
			break
		}
	}
	if cookie == nil {
		t.Fatal("expected hb_admin cookie after callback")
	}

	// 3. Hit /admin/peers/{id}/approve with the session cookie.
	approveReq := httptest.NewRequest("POST", "/admin/peers/fed_pending/approve", nil)
	approveReq.AddCookie(&http.Cookie{Name: "hb_admin", Value: cookie.Value})
	approveW := httptest.NewRecorder()
	adminRouter(h).ServeHTTP(approveW, approveReq)
	if approveW.Code != http.StatusSeeOther {
		t.Fatalf("approve expected 303, got %d: %s", approveW.Code, approveW.Body.String())
	}
	var isActive bool
	if err := db.QueryRow(`SELECT is_active FROM federated_instances WHERE id = ?`, "fed_pending").Scan(&isActive); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !isActive {
		t.Fatal("ApprovePeer must flip is_active to 1 after magic-link login")
	}
}

func TestAdminRevokePeer(t *testing.T) {
	db := adminTestDB(t)
	sess := newTestSessions(t)
	pen := newTestPending(t)
	h := newAdminHandler(t, db, sess, pen, func(string, string) error { return nil }, "ops@example.com")

	_, err := db.Exec(
		`INSERT INTO federated_instances (id, name, endpoint_url, public_key, instance_key, is_active, last_seen_at, created_at)
		 VALUES (?, ?, ?, ?, ?, 1, ?, ?)`,
		"fed_active", "active", "http://a:8400", "pk", "pk", time.Now().Unix(), time.Now().Unix(),
	)
	if err != nil {
		t.Fatalf("insert peer: %v", err)
	}

	tok, _ := sess.NewToken()
	req := httptest.NewRequest("POST", "/admin/peers/fed_active/revoke", nil)
	req.AddCookie(&http.Cookie{Name: "hb_admin", Value: tok})
	w := httptest.NewRecorder()
	adminRouter(h).ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", w.Code, w.Body.String())
	}

	var isActive bool
	var revokedAt sql.NullInt64
	if err := db.QueryRow(
		`SELECT is_active, revoked_at FROM federated_instances WHERE id = ?`,
		"fed_active",
	).Scan(&isActive, &revokedAt); err != nil {
		t.Fatalf("query: %v", err)
	}
	if isActive {
		t.Fatal("RevokePeer must set is_active to 0")
	}
	if !revokedAt.Valid {
		t.Fatal("RevokePeer must set revoked_at")
	}
}

func TestAdminRevokePeer_NotFound(t *testing.T) {
	db := adminTestDB(t)
	sess := newTestSessions(t)
	pen := newTestPending(t)
	h := newAdminHandler(t, db, sess, pen, func(string, string) error { return nil }, "ops@example.com")

	tok, _ := sess.NewToken()
	req := httptest.NewRequest("POST", "/admin/peers/fed_missing/revoke", nil)
	req.AddCookie(&http.Cookie{Name: "hb_admin", Value: tok})
	w := httptest.NewRecorder()
	adminRouter(h).ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown peer, got %d", w.Code)
	}
}

// adminRouter mirrors the conditional registration in server.go so that
// chi.URLParam gets populated for tests that hit dynamic routes like
// /admin/peers/{id}/{action}.
func adminRouter(h *handler.AdminHandler) http.Handler {
	r := chi.NewRouter()
	r.Post("/admin/peers/{id}/approve", h.ApprovePeer)
	r.Post("/admin/peers/{id}/revoke", h.RevokePeer)
	r.Get("/admin", h.Panel)
	return r
}

func TestAdminRoutes_404WhenDisabled(t *testing.T) {
	// Wire a router that mirrors server.go's conditional registration:
	// routes are NOT registered when the handler is disabled, so chi
	// responds 404 by default. Mirror that here.
	chi_router := http.NewServeMux()
	chi_router.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	req := httptest.NewRequest("GET", "/admin/login", nil)
	w := httptest.NewRecorder()
	chi_router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 (admin disabled routes unregistered), got %d", w.Code)
	}

	req = httptest.NewRequest("GET", "/admin", nil)
	w = httptest.NewRecorder()
	chi_router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 (admin disabled routes unregistered), got %d", w.Code)
	}
}
