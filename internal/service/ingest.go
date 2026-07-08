package service

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"

	"hirebridge/internal/crypto"
	"hirebridge/internal/store/repo"
)

type IngestService struct {
	DB     *sql.DB
	Logger *slog.Logger
}

func NewIngestService(db *sql.DB, logger *slog.Logger) *IngestService {
	return &IngestService{DB: db, Logger: logger}
}

type SnapshotInput struct {
	CandidateID string          `json:"candidate_id"`
	Payload     json.RawMessage `json:"payload"`
	Embedding   [][]float64     `json:"embedding"`
	Signature   string          `json:"signature"`
}

func (s *IngestService) Process(nodeID string, input *SnapshotInput) error {
	n, err := repo.NodeByID(s.DB, nodeID)
	if err != nil {
		return fmt.Errorf("lookup node: %w", err)
	}
	if n == nil {
		return fmt.Errorf("node not found")
	}
	if !n.IsActive {
		return fmt.Errorf("node is inactive or revoked")
	}

	payloadBytes := []byte(input.Payload)
	if len(payloadBytes) > 1<<20 {
		return fmt.Errorf("payload too large: %d bytes, max 1MB", len(payloadBytes))
	}

	if n.PublicKey != nil && len(n.PublicKey) > 0 {
		if input.Signature == "" {
			return fmt.Errorf("signature required when node has a public key")
		}
		valid, err := crypto.VerifySignature(n.PublicKey, payloadBytes, input.Signature)
		if err != nil {
			return fmt.Errorf("verify signature: %w", err)
		}
		if !valid {
			return fmt.Errorf("signature verification failed")
		}
		s.Logger.Info("snapshot signature verified",
			"node_id", nodeID,
			"candidate_id", input.CandidateID,
		)
	}

	payloadJSON := string(input.Payload)

	var sigBytes []byte
	if input.Signature != "" {
		sigBytes = []byte(input.Signature)
	}

	if err := repo.UpsertSnapshot(s.DB, repo.NewID(), nodeID, input.CandidateID, payloadJSON, sigBytes); err != nil {
		return fmt.Errorf("upsert snapshot: %w", err)
	}

	if err := repo.ReplaceFTS5Row(s.DB, input.CandidateID, payloadJSON); err != nil {
		s.Logger.Warn("failed to update FTS5 index",
			"error", err,
			"candidate_id", input.CandidateID,
		)
	}

	if len(input.Embedding) > 0 {
		if err := repo.UpsertVec0Embedding(s.DB, input.CandidateID, input.Embedding); err != nil {
			s.Logger.Warn("failed to upsert vec0 embedding",
				"error", err,
				"candidate_id", input.CandidateID,
			)
		}
	}

	if err := repo.UpdateNodePing(s.DB, nodeID); err != nil {
		s.Logger.Warn("failed to update node ping",
			"error", err,
			"node_id", nodeID,
		)
	}

	s.Logger.Info("snapshot ingested",
		"node_id", nodeID,
		"candidate_id", input.CandidateID,
		"payload_bytes", len(payloadJSON),
	)

	return nil
}
