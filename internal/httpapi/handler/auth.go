package handler

import (
	"crypto/ed25519"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"hirebridge/internal/auth"
	"hirebridge/internal/httpapi/middleware"
	"hirebridge/internal/httpapi/render"
	"hirebridge/internal/store/repo"
)

type AuthHandler struct {
	Svc    *auth.Service
	DB     *sql.DB
	Logger *slog.Logger
	Limiter *middleware.RateLimiter
}

func (h *AuthHandler) RequestMagicLink(w http.ResponseWriter, r *http.Request) {
	email := r.FormValue("email")
	uc := r.FormValue("uc")

	if email == "" {
		http.Error(w, `{"error":"email_required"}`, http.StatusBadRequest)
		return
	}

	clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if clientIP == "" {
		clientIP = r.RemoteAddr
	}
	ipKey := "ip:" + clientIP
	emailKey := "email:" + email

	if !h.Limiter.Allow(ipKey, 10, 15*time.Minute) || !h.Limiter.Allow(emailKey, 5, 15*time.Minute) {
		http.Error(w, `{"error":"rate_limited"}`, http.StatusTooManyRequests)
		return
	}

	if err := h.Svc.RequestMagicLink(email, uc); err != nil {
		h.Logger.ErrorContext(r.Context(), "magic link request failed", "error", err, "email", email)
		http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"ok": "check your email"})
}

func (h *AuthHandler) Callback(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	result, err := h.Svc.VerifyMagicCallback(token)
	if err != nil {
		h.Logger.ErrorContext(r.Context(), "magic callback error", "error", err)
		http.Error(w, "invalid or expired link", http.StatusBadRequest)
		return
	}
	if result == nil {
		http.Error(w, "invalid or expired link", http.StatusBadRequest)
		return
	}

	if result.PendingDevice {
		http.Redirect(w, r, "/device?pending=1", http.StatusSeeOther)
		return
	}

	if result.AccessToken != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     "hb_token",
			Value:    result.AccessToken,
			Path:     "/",
			HttpOnly: true,
			Secure:   r.TLS != nil,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   0,
		})
	}

	if result.DeviceApproved {
		http.Redirect(w, r, "/device?approved=1", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	token := middleware.BearerFromRequest(r)
	if token != "" {
		_ = repo.RevokeAPIToken(h.DB, token)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "hb_token",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"ok": "logged out"})
}

func (h *AuthHandler) InitDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NodeType    string `json:"node_type"`
		EndpointURL string `json:"endpoint_url"`
		PublicKey   string `json:"public_key"`
	}
	if r.Body != nil {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		json.Unmarshal(body, &req)
	}

	var pubKey []byte
	if req.PublicKey != "" {
		var err error
		pubKey, err = hex.DecodeString(req.PublicKey)
		if err != nil || len(pubKey) != ed25519.PublicKeySize {
			http.Error(w, `{"error":"invalid_public_key"}`, http.StatusBadRequest)
			return
		}
	}

	resp, err := h.Svc.InitiateDeviceFlow(req.NodeType, req.EndpointURL, pubKey)
	if err != nil {
		h.Logger.ErrorContext(r.Context(), "device init failed", "error", err)
		http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *AuthHandler) DevicePage(w http.ResponseWriter, r *http.Request) {
	uc := r.URL.Query().Get("uc")
	approved := r.URL.Query().Get("approved") == "1"
	userID := middleware.UserIDFromContext(r.Context())

	data := map[string]any{
		"UserCode": uc,
		"Approved": approved,
		"LoggedIn": userID != "",
	}

	render.HTML(w, "device.html", data)
}

func (h *AuthHandler) ApproveDevice(w http.ResponseWriter, r *http.Request) {
	uc := r.FormValue("uc")
	userID := middleware.UserIDFromContext(r.Context())

	if userID == "" {
		http.Error(w, "not logged in", http.StatusUnauthorized)
		return
	}

	if uc == "" {
		http.Error(w, "missing user_code", http.StatusBadRequest)
		return
	}

	ds, err := repo.DeviceSessionByUserCode(h.DB, uc)
	if err != nil || ds == nil {
		http.Error(w, "invalid user code", http.StatusBadRequest)
		return
	}

	if err := repo.ApproveDeviceSession(h.DB, ds.DeviceCodeHash, userID); err != nil {
		h.Logger.ErrorContext(r.Context(), "approve device error", "error", err)
		http.Error(w, "approval failed", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/device?uc="+uc+"&approved=1", http.StatusSeeOther)
}

func (h *AuthHandler) TokenEndpoint(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}

	grantType := r.FormValue("grant_type")
	if grantType != "urn:ietf:params:oauth:grant-type:device_code" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "unsupported_grant_type"})
		return
	}

	deviceCode := r.FormValue("device_code")
	if deviceCode == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid_request"})
		return
	}

	resp, err := h.Svc.PollToken(deviceCode)
	if err != nil {
		h.Logger.ErrorContext(r.Context(), "token poll error", "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "internal"})
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if resp.Error != "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(resp)
		return
	}

	json.NewEncoder(w).Encode(resp)
}
