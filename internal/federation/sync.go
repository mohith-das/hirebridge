package federation

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

type Syncer struct {
	DB       *sql.DB
	Client   *Client
	Identity *Identity
	Logger   *slog.Logger
	Interval time.Duration
}

func NewSyncer(db *sql.DB, client *Client, ident *Identity, logger *slog.Logger, interval time.Duration) *Syncer {
	return &Syncer{DB: db, Client: client, Identity: ident, Logger: logger, Interval: interval}
}

func (s *Syncer) Run() {
	s.Logger.Info("federation sync worker started", "interval", s.Interval)
	t := time.NewTicker(s.Interval)
	defer t.Stop()

	for range t.C {
		s.syncOnce()
	}
}

func (s *Syncer) syncOnce() {
	rows, err := s.DB.Query(
		`SELECT endpoint_url, public_key, last_seen_at FROM federated_instances WHERE is_active = 1`,
	)
	if err != nil {
		s.Logger.Warn("sync: query peers failed", "error", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var url, pubkey string
		var lastSeen int64
		rows.Scan(&url, &pubkey, &lastSeen)
		s.syncPeer(url, pubkey)
	}
}

type peerSnapshot struct {
	CandidateID string `json:"candidate_id"`
	PayloadJSON string `json:"payload_json"`
	NodeID      string `json:"node_id"`
	IngestedAt  int64  `json:"ingested_at"`
}

func (s *Syncer) syncPeer(endpointURL, pubkey string) {
	since := s.lastSyncFor(endpointURL)
	resp, err := s.Client.SignedGet(s.Identity, fmt.Sprintf("%s/fed/snapshots?since=%d&limit=50", endpointURL, since))
	if err != nil {
		s.Logger.Warn("sync: fetch failed", "peer", endpointURL, "error", err)
		return
	}

	var snaps []peerSnapshot
	if err := json.Unmarshal(resp, &snaps); err != nil {
		s.Logger.Warn("sync: parse failed", "peer", endpointURL, "error", err)
		return
	}

	for _, snap := range snaps {
		s.DB.Exec(
			`INSERT OR REPLACE INTO federated_snapshots
			 (id, peer_instance_id, candidate_id, payload_preview, origin_node_id, origin_endpoint, ingested_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			fmt.Sprintf("fs_%s", snap.CandidateID),
			endpointURL,
			snap.CandidateID,
			truncate(snap.PayloadJSON, 2048),
			snap.NodeID,
			endpointURL,
			snap.IngestedAt,
		)

		s.DB.Exec(
			`INSERT OR REPLACE INTO fed_snapshots_fts (candidate_id, content) VALUES (?, ?)`,
			snap.CandidateID, truncate(snap.PayloadJSON, 2048),
		)
	}

	s.DB.Exec(`UPDATE federated_instances SET last_seen_at = ? WHERE endpoint_url = ?`,
		time.Now().Unix(), endpointURL)
}

func (s *Syncer) lastSyncFor(endpointURL string) int64 {
	var ts int64
	s.DB.QueryRow(
		`SELECT coalesce(max(ingested_at), 0) FROM federated_snapshots WHERE peer_instance_id = ?`,
		endpointURL,
	).Scan(&ts)
	return ts
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
