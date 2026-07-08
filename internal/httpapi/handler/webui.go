package handler

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"hirebridge/internal/httpapi/middleware"
	"hirebridge/internal/httpapi/render"
	"hirebridge/internal/store/repo"
)

type WebUIHandler struct {
	DB      *sql.DB
	BaseURL string
	Logger  *slog.Logger
	StaleAge time.Duration
}

func (h *WebUIHandler) Landing(w http.ResponseWriter, r *http.Request) {
	render.HTML(w, "landing.html", nil)
}

func (h *WebUIHandler) DashboardRedirect(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	if userID == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	nodes, _ := repo.NodesByUser(h.DB, userID)
	if len(nodes) > 0 {
		http.Redirect(w, r, "/dashboard/talent", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/dashboard/recruiter", http.StatusSeeOther)
}

func (h *WebUIHandler) TalentDashboard(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	if userID == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	nodes, err := repo.NodesByUser(h.DB, userID)
	if err != nil {
		h.Logger.Error("talent dashboard error", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	type nodeView struct {
		ID          string
		DisplayName string
		NodeType    string
		EndpointURL string
		IsOnline    bool
		LastSync    string
		IsActive    bool
	}

	now := time.Now()
	var views []nodeView
	for _, n := range nodes {
		v := nodeView{
			ID:          n.ID,
			DisplayName: n.ID[:8],
			NodeType:    n.NodeType,
			EndpointURL: n.EndpointURL,
			IsActive:    n.IsActive,
		}
		if n.DisplayName.Valid {
			v.DisplayName = n.DisplayName.String
		}
		if n.LastPingTimestamp.Valid {
			ts := time.Unix(n.LastPingTimestamp.Int64, 0)
			v.LastSync = ts.Format("2006-01-02 15:04")
			v.IsOnline = now.Sub(ts) < h.StaleAge
		}
		views = append(views, v)
	}

	render.HTML(w, "talent_dashboard.html", map[string]any{"Nodes": views})
}

func (h *WebUIHandler) RecruiterDashboard(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	if userID == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	token := middleware.BearerFromRequest(r)
	apiKey := "<generate an API key>"
	if token != "" {
		apiKey = fmt.Sprintf("%s", token)
	}

	var candidateCount int
	h.DB.QueryRow(`SELECT count(*) FROM snapshots`).Scan(&candidateCount)

	var activeNodeCount int
	h.DB.QueryRow(`SELECT count(*) FROM nodes WHERE is_active=1 AND last_ping_timestamp > ?`,
		time.Now().Add(-h.StaleAge).Unix()).Scan(&activeNodeCount)

	type activityRow struct {
		Action string
		Time   string
	}
	var activity []activityRow
	rows, err := h.DB.Query(
		`SELECT action, ts FROM audit_log WHERE actor_user_id=? ORDER BY ts DESC LIMIT 10`,
		userID,
	)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var r activityRow
			var ts int64
			if rows.Scan(&r.Action, &ts) == nil {
				r.Time = time.Unix(ts, 0).Format("2006-01-02 15:04")
				activity = append(activity, r)
			}
		}
	}

	render.HTML(w, "recruiter_dashboard.html", map[string]any{
		"APIKey":         apiKey,
		"BaseURL":        h.BaseURL,
		"TalentCount":    candidateCount,
		"NodeCount":      activeNodeCount,
		"RecentActivity": activity,
	})
}

func (h *WebUIHandler) RevokeNode(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	if userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	nodeID := chi.URLParam(r, "nodeID")
	if nodeID == "" {
		http.Error(w, "missing node id", http.StatusBadRequest)
		return
	}

	n, err := repo.NodeByID(h.DB, nodeID)
	if err != nil || n == nil || !n.UserID.Valid || n.UserID.String != userID {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if err := repo.RevokeNode(h.DB, nodeID); err != nil {
		h.Logger.Error("revoke node failed", "error", err)
		http.Error(w, "failed", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/dashboard/talent", http.StatusSeeOther)
}
