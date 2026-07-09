package federation

import (
	"bytes"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"hirebridge/internal/store/repo"
)

type Handler struct {
	DB       *sql.DB
	Identity *Identity
	Logger   *slog.Logger
	Config   *Config
}

func NewHandler(db *sql.DB, ident *Identity, logger *slog.Logger, cfg *Config) *Handler {
	return &Handler{DB: db, Identity: ident, Logger: logger, Config: cfg}
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/fed/handshake", h.handshake)
	mux.HandleFunc("/fed/health", h.health)
	mux.HandleFunc("/fed/snapshots", h.snapshots)
	mux.HandleFunc("/fed/profile/", h.profile)
	mux.HandleFunc("/fed/search", h.search)
	mux.HandleFunc("/fed/register", h.register)
	mux.HandleFunc("/fed/peers", h.peers)
	mux.HandleFunc("/fed/heartbeat", h.heartbeat)
	return h.fedAuth(mux)
}

func (h *Handler) fedAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sig := r.Header.Get("X-Fed-Signature")
		pubKey := r.Header.Get("X-Fed-Public-Key")
		if sig == "" || pubKey == "" {
			http.Error(w, `{"error":"missing federation auth headers"}`, http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))

		if !VerifySignatureStr(pubKey, string(body), sig) {
			http.Error(w, `{"error":"invalid federation signature"}`, http.StatusUnauthorized)
			return
		}

		// Bootstrap endpoints (register/handshake) don't require an active
		// peer record yet — that's what they exist to create. All other
		// endpoints (and /fed/health) still gate on active peer lookup.
		if r.URL.Path != "/fed/health" && r.URL.Path != "/fed/register" && r.URL.Path != "/fed/handshake" {
			var isActive bool
			err := h.DB.QueryRow(
				`SELECT is_active FROM federated_instances WHERE instance_key = ? AND revoked_at IS NULL`,
				pubKey,
			).Scan(&isActive)
			if err != nil || !isActive {
				http.Error(w, `{"error":"unknown or inactive peer"}`, http.StatusUnauthorized)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func (h *Handler) handshake(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"instance_name"`
		PublicKey   string `json:"public_key"`
		EndpointURL string `json:"endpoint_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}
	now := time.Now().Unix()
	instanceID := "fed_" + req.Name

	var existingIsActive bool
	err := h.DB.QueryRow(`SELECT is_active FROM federated_instances WHERE id = ?`, instanceID).Scan(&existingIsActive)
	if err == nil && existingIsActive {
		http.Error(w, `{"error":"peer already registered and active"}`, http.StatusConflict)
		return
	}

	trusted := joinSecretMatches(r, h.Config.JoinSecret)
	h.DB.Exec(
		`INSERT OR REPLACE INTO federated_instances (id, name, endpoint_url, public_key, instance_key, is_active, last_seen_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		instanceID, req.Name, req.EndpointURL, req.PublicKey, req.PublicKey, trusted, now, now,
	)
	status := "pending_approval"
	if trusted {
		status = "active"
	}
	writeJSON(w, map[string]any{
		"accepted":    true,
		"instance_id": instanceID,
		"public_key":  h.Identity.PublicKey,
		"version":     "1.0.0",
		"status":      status,
	})
}

func joinSecretMatches(r *http.Request, configured string) bool {
	if configured == "" {
		return false
	}
	got := r.Header.Get("X-Fed-Join-Secret")
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(configured)) == 1
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	var count int
	h.DB.QueryRow(`SELECT count(*) FROM snapshots`).Scan(&count)
	writeJSON(w, map[string]any{
		"instance_name":  h.Config.InstanceName,
		"version":        "1.0.0",
		"snapshot_count": count,
		"public_key":     h.Identity.PublicKey,
	})
}

func (h *Handler) snapshots(w http.ResponseWriter, r *http.Request) {
	since := r.URL.Query().Get("since")
	if since == "" {
		since = "0"
	}
	rows, err := h.DB.Query(
		`SELECT candidate_id, payload_json, signature, node_id, ingested_at
		 FROM snapshots WHERE ingested_at > ? ORDER BY ingested_at ASC LIMIT 100`, since,
	)
	if err != nil {
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var cid, payload, nodeID string
		var sig []byte
		var ingested int64
		rows.Scan(&cid, &payload, &sig, &nodeID, &ingested)
		results = append(results, map[string]any{
			"candidate_id": cid, "payload_json": payload,
			"node_id": nodeID, "ingested_at": ingested,
			"signature": hex.EncodeToString(sig),
		})
	}
	writeJSON(w, results)
}

func (h *Handler) profile(w http.ResponseWriter, r *http.Request) {
	cid := r.URL.Path[len("/fed/profile/"):]
	if cid == "" {
		http.Error(w, `{"error":"missing candidate_id"}`, http.StatusBadRequest)
		return
	}
	var ccid, payload, nodeID string
	var sig []byte
	var ingested int64
	err := h.DB.QueryRow(
		`SELECT candidate_id, payload_json, signature, node_id, ingested_at
		 FROM snapshots WHERE candidate_id = ?`, cid,
	).Scan(&ccid, &payload, &sig, &nodeID, &ingested)
	if err != nil {
		http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{
		"candidate_id": ccid, "payload_json": payload,
		"node_id": nodeID, "ingested_at": ingested,
	})
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}
	if req.Limit <= 0 {
		req.Limit = 20
	}
	sanitized := repo.SanitizeFTS5Query(req.Query)
	if sanitized == "" {
		writeJSON(w, []map[string]string{})
		return
	}
	rows, err := h.DB.Query(
		`SELECT candidate_id FROM snapshots_fts WHERE snapshots_fts MATCH ? LIMIT ?`,
		sanitized, req.Limit,
	)
	if err != nil {
		h.Logger.Warn("fed search failed", "error", err)
		writeJSON(w, []map[string]string{})
		return
	}
	defer rows.Close()

	var results []map[string]string
	for rows.Next() {
		var cid string
		rows.Scan(&cid)
		results = append(results, map[string]string{"candidate_id": cid})
	}
	writeJSON(w, results)
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"instance_name"`
		EndpointURL string `json:"endpoint_url"`
		PublicKey   string `json:"public_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}
	instanceID := "fed_" + req.Name

	var existingIsActive bool
	err := h.DB.QueryRow(`SELECT is_active FROM federated_instances WHERE id = ?`, instanceID).Scan(&existingIsActive)
	if err == nil && existingIsActive {
		http.Error(w, `{"error":"peer already registered and active"}`, http.StatusConflict)
		return
	}

	now := time.Now().Unix()
	trusted := joinSecretMatches(r, h.Config.JoinSecret)
	h.DB.Exec(
		`INSERT OR REPLACE INTO federated_instances (id, name, endpoint_url, public_key, instance_key, is_active, last_seen_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		instanceID, req.Name, req.EndpointURL, req.PublicKey, req.PublicKey, trusted, now, now,
	)
	state := "pending_approval"
	if trusted {
		state = "active"
	}
	writeJSON(w, map[string]string{"status": "registered", "state": state})
}

func (h *Handler) peers(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.Query(
		`SELECT id, name, endpoint_url, public_key, last_seen_at, is_active
		 FROM federated_instances WHERE is_active = 1 ORDER BY name`,
	)
	if err != nil {
		http.Error(w, `{"error":"db_error"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var peers []map[string]any
	for rows.Next() {
		var id, name, endpoint, pubkey string
		var lastSeen int64
		var isActive bool
		rows.Scan(&id, &name, &endpoint, &pubkey, &lastSeen, &isActive)
		peers = append(peers, map[string]any{
			"instance_id": id, "name": name, "endpoint_url": endpoint,
			"public_key": pubkey, "last_seen_at": lastSeen, "is_active": isActive,
		})
	}
	writeJSON(w, peers)
}

func (h *Handler) heartbeat(w http.ResponseWriter, r *http.Request) {
	pubKey := r.Header.Get("X-Fed-Public-Key")
	h.DB.Exec(`UPDATE federated_instances SET last_seen_at = ? WHERE instance_key = ?`,
		time.Now().Unix(), pubKey)
	writeJSON(w, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
