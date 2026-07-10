package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"hirebridge/internal/httpapi/middleware"
	"hirebridge/internal/service"
)

type IngestHandler struct {
	Svc    *service.IngestService
	Logger *slog.Logger
}

func (h *IngestHandler) Snapshot(w http.ResponseWriter, r *http.Request) {
	nodeID := middleware.NodeIDFromContext(r.Context())
	if nodeID == "" {
		http.Error(w, `{"error":"node_not_authenticated"}`, http.StatusForbidden)
		return
	}

	var input service.SnapshotInput
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		http.Error(w, `{"error":"read_body"}`, http.StatusBadRequest)
		return
	}

	candidatePart := struct {
		CandidateID string `json:"candidate_id"`
	}{}
	if err := json.Unmarshal(body, &candidatePart); err != nil {
		http.Error(w, `{"error":"invalid_json"}`, http.StatusBadRequest)
		return
	}

	if err := json.Unmarshal(body, &input); err != nil {
		http.Error(w, `{"error":"invalid_json"}`, http.StatusBadRequest)
		return
	}

	if input.CandidateID == "" {
		http.Error(w, `{"error":"candidate_id_required"}`, http.StatusBadRequest)
		return
	}

	if err := h.Svc.Process(nodeID, &input); err != nil {
		if errors.Is(err, service.ErrEmbedDimMismatch) {
			h.Logger.WarnContext(r.Context(), "ingest rejected: embedding dim mismatch", "error", err, "node_id", nodeID)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		h.Logger.ErrorContext(r.Context(), "ingest failed", "error", err, "node_id", nodeID)
		http.Error(w, `{"error":"ingest_failed"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
}