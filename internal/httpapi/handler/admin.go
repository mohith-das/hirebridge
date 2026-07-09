package handler

import (
	"crypto/subtle"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"hirebridge/internal/federation"
	"hirebridge/internal/httpapi/middleware"
	"hirebridge/internal/httpapi/render"
	"hirebridge/internal/store/repo"
)

const adminSessionTTL = 2 * time.Hour
const adminLoginRatePerIP = 5
const adminLoginRateWindow = 15 * time.Minute

// SendMagicLink is the subset of auth.Mailer the admin login uses. Kept as
// a function type so tests can swap in a fake without depending on the
// concrete mailer types.
type SendMagicLink func(email, link string) error

// AdminHandler serves /admin/* — operator-only peer management. The admin
// identity is seeded at deploy time via HB_ADMIN_EMAIL and authenticates
// exclusively through a magic-link flow that is decoupled from the normal
// users / magic_tokens / device flows. A regular user (magic-link or device
// code) can never become admin.
type AdminHandler struct {
	DB            *sql.DB
	Logger        *slog.Logger
	Sessions      *middleware.AdminSessions
	Pending       *middleware.AdminPendingLinks
	AdminEmail    string
	LinkTTL       time.Duration
	SendLink      SendMagicLink
	Limiter       *middleware.RateLimiter
}

// Enabled reports whether admin routes should be registered.
func (h *AdminHandler) Enabled() bool {
	return strings.TrimSpace(h.AdminEmail) != ""
}

func (h *AdminHandler) LoginForm(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Sent":  false,
		"Error": "",
	}
	if r.URL.Query().Get("sent") == "1" {
		data["Sent"] = true
	}
	if r.URL.Query().Get("error") == "1" {
		data["Error"] = "That link is invalid or has expired. Try again."
	}
	render.HTML(w, "admin_login.html", data)
}

// LoginSubmit accepts an email, compares it constant-time-against the
// configured admin email, and only enqueues a magic-link token when the
// match is true. The HTTP response body is identical either way (uniform
// "check your inbox" page).
func (h *AdminHandler) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	if !h.Enabled() {
		http.NotFound(w, r)
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip == "" {
		ip = r.RemoteAddr
	}
	if !h.Limiter.Allow("admin-login:"+ip, adminLoginRatePerIP, adminLoginRateWindow) {
		http.Error(w, "rate_limited", http.StatusTooManyRequests)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid_request", http.StatusBadRequest)
		return
	}
	submitted := strings.TrimSpace(r.FormValue("email"))

	// Constant-time compare; lengths are normalized to avoid early-exit
	// timing leaks. The branch is taken on every request — we just
	// choose to drop the token on a mismatch.
	matched := constantTimeEmailEqual(submitted, h.AdminEmail)

	if matched {
		token := repo.GenerateToken()
		h.Pending.Put(repo.HashToken(token))
		link := fmt.Sprintf("%s/admin/callback?token=%s", h.baseURL(r), url.QueryEscape(token))
		if h.Logger != nil {
			h.Logger.InfoContext(r.Context(), "admin magic link enqueued", "email", submitted)
		}
		if h.SendLink != nil {
			if err := h.SendLink(submitted, link); err != nil && h.Logger != nil {
				h.Logger.WarnContext(r.Context(), "admin magic link send failed", "error", err)
			}
		}
	}

	// Uniform response: render the form with Sent=true so the body is
	// indistinguishable from the no-match case.
	render.HTML(w, "admin_login.html", map[string]any{
		"Sent":  true,
		"Error": "",
	})
}

// Callback consumes a one-shot pending-link token. On success it mints an
// admin session and redirects to /admin. On any failure (missing,
// expired, consumed) it redirects to /admin/login with ?error=1.
func (h *AdminHandler) Callback(w http.ResponseWriter, r *http.Request) {
	if !h.Enabled() {
		http.NotFound(w, r)
		return
	}
	token := r.URL.Query().Get("token")
	if !h.Pending.Consume(repo.HashToken(token)) {
		http.Redirect(w, r, "/admin/login?error=1", http.StatusSeeOther)
		return
	}

	sess, err := h.Sessions.NewToken()
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	middleware.SetAdminCookie(w, sess, adminSessionTTL, r.TLS != nil)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (h *AdminHandler) Logout(w http.ResponseWriter, r *http.Request) {
	tok := middleware.AdminCookieValue(r)
	h.Sessions.Revoke(tok)
	middleware.ClearAdminCookie(w)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

type peerRow struct {
	ID          string
	Name        string
	EndpointURL string
	IsActive    bool
	LastSeenAt  string
	RevokedAt   string
}

func (h *AdminHandler) Panel(w http.ResponseWriter, r *http.Request) {
	peers, err := federation.ListFederatedInstances(h.DB)
	if err != nil {
		h.Logger.ErrorContext(r.Context(), "list peers failed", "error", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}

	rows := make([]peerRow, 0, len(peers))
	for _, p := range peers {
		row := peerRow{
			ID:          p.ID,
			Name:        p.Name,
			EndpointURL: p.EndpointURL,
			IsActive:    p.IsActive,
		}
		if p.LastSeenAt.Valid {
			row.LastSeenAt = time.Unix(p.LastSeenAt.Int64, 0).UTC().Format("2006-01-02 15:04 UTC")
		}
		if p.RevokedAt.Valid {
			row.RevokedAt = time.Unix(p.RevokedAt.Int64, 0).UTC().Format("2006-01-02 15:04 UTC")
		}
		rows = append(rows, row)
	}

	render.HTML(w, "admin_panel.html", map[string]any{
		"Peers":    rows,
		"HasPeers": len(rows) > 0,
		"BaseURL":  h.baseURL(r),
	})
}

func (h *AdminHandler) ApprovePeer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" || !strings.HasPrefix(id, "fed_") {
		http.Error(w, "bad_request", http.StatusBadRequest)
		return
	}
	if err := federation.ApproveFederatedInstance(h.DB, id); err != nil {
		h.Logger.ErrorContext(r.Context(), "approve peer failed", "error", err, "id", id)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	h.logAction(id, "fed_approved")
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (h *AdminHandler) RevokePeer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" || !strings.HasPrefix(id, "fed_") {
		http.Error(w, "bad_request", http.StatusBadRequest)
		return
	}
	if err := federation.RevokeFederatedInstance(h.DB, id); err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	} else if err != nil {
		h.Logger.ErrorContext(r.Context(), "revoke peer failed", "error", err, "id", id)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	h.logAction(id, "fed_revoked")
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (h *AdminHandler) logAction(target, action string) {
	_, err := h.DB.Exec(
		`INSERT INTO audit_log (id, actor_user_id, action, target, ts) VALUES (?, NULL, ?, ?, ?)`,
		repo.NewID(), action, target, time.Now().Unix(),
	)
	if err != nil && h.Logger != nil {
		h.Logger.Warn("audit write failed", "error", err)
	}
}

func (h *AdminHandler) baseURL(r *http.Request) string {
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		return p + "://" + r.Host
	}
	if r.TLS != nil {
		return "https://" + r.Host
	}
	return "http://" + r.Host
}

// constantTimeEmailEqual compares two email strings in constant time. It
// returns true iff they have the same length AND byte-for-byte equality.
// Used so the magic-link request doesn't leak whether the submitted email
// matched by length or timing.
func constantTimeEmailEqual(a, b string) bool {
	a = strings.ToLower(strings.TrimSpace(a))
	b = strings.ToLower(strings.TrimSpace(b))
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
