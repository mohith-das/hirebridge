package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

// AdminSessions is an in-memory store of opaque admin session tokens.
// Mirrors the pattern in ratelimit.go: map + mutex + periodic reaper.
type AdminSessions struct {
	mu       sync.Mutex
	sessions map[string]time.Time
	ttl      time.Duration
}

func NewAdminSessions(ttl time.Duration) *AdminSessions {
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}
	s := &AdminSessions{
		sessions: make(map[string]time.Time),
		ttl:      ttl,
	}
	go s.reapLoop(5 * time.Minute)
	return s
}

func (s *AdminSessions) reapLoop(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		s.mu.Lock()
		for k, exp := range s.sessions {
			if now.After(exp) {
				delete(s.sessions, k)
			}
		}
		s.mu.Unlock()
	}
}

// NewToken mints a new opaque session token and stores it. The returned
// string is what gets set as the hb_admin cookie.
func (s *AdminSessions) NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	s.mu.Lock()
	s.sessions[tok] = time.Now().Add(s.ttl)
	s.mu.Unlock()
	return tok, nil
}

func (s *AdminSessions) Valid(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[tok]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, tok)
		return false
	}
	return true
}

func (s *AdminSessions) Revoke(tok string) {
	if tok == "" {
		return
	}
	s.mu.Lock()
	delete(s.sessions, tok)
	s.mu.Unlock()
}

// AdminPendingLinks stores single-use, in-memory admin login tokens
// (token_hash → expiry). Distinct from AdminSessions (post-login cookies)
// and from the magic_tokens DB table used by the normal user flow.
// Mirrors the in-memory pattern in ratelimit.go.
type AdminPendingLinks struct {
	mu    sync.Mutex
	links map[string]time.Time
	ttl   time.Duration
}

func NewAdminPendingLinks(ttl time.Duration) *AdminPendingLinks {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	p := &AdminPendingLinks{
		links: make(map[string]time.Time),
		ttl:   ttl,
	}
	go p.reapLoop(5 * time.Minute)
	return p
}

func (p *AdminPendingLinks) reapLoop(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		p.mu.Lock()
		for k, exp := range p.links {
			if now.After(exp) {
				delete(p.links, k)
			}
		}
		p.mu.Unlock()
	}
}

// Put stores a token hash with the configured TTL.
func (p *AdminPendingLinks) Put(tokenHash string) {
	p.mu.Lock()
	p.links[tokenHash] = time.Now().Add(p.ttl)
	p.mu.Unlock()
}

// Consume is single-use: deletes on lookup (whether or not it was valid).
// Returns true iff the hash was present and not yet expired.
func (p *AdminPendingLinks) Consume(tokenHash string) bool {
	if tokenHash == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	exp, ok := p.links[tokenHash]
	if !ok {
		return false
	}
	delete(p.links, tokenHash)
	if time.Now().After(exp) {
		return false
	}
	return true
}

func adminCookieValue(r *http.Request) string {
	c, err := r.Cookie("hb_admin")
	if err != nil {
		return ""
	}
	return c.Value
}

// AdminCookieValue is the exported form of adminCookieValue.
func AdminCookieValue(r *http.Request) string { return adminCookieValue(r) }

// SetAdminCookie sets the hb_admin cookie. `ttl` controls MaxAge; secure
// should be true when the request was received over TLS.
func SetAdminCookie(w http.ResponseWriter, token string, ttl time.Duration, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     "hb_admin",
		Value:    token,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(ttl.Seconds()),
	})
}

// ClearAdminCookie blanks the hb_admin cookie (MaxAge=-1).
func ClearAdminCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "hb_admin",
		Value:    "",
		Path:     "/admin",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

// RequireAdmin gates admin routes on a non-expired hb_admin cookie.
// If `enabled` is false, every request returns 404 (admin is deploy-time
// opt-in via env vars).
func RequireAdmin(sessions *AdminSessions, enabled bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !enabled {
				http.NotFound(w, r)
				return
			}
			tok := AdminCookieValue(r)
			if !sessions.Valid(tok) {
				if tok != "" {
					ClearAdminCookie(w)
				}
				http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
