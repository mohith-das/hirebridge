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
		`SELECT id, endpoint_url, public_key FROM federated_instances WHERE is_active = 1`,
	)
	if err != nil {
		s.Logger.Warn("sync: query peers failed", "error", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id, url, pubkey string
		rows.Scan(&id, &url, &pubkey)
		s.syncPeer(id, url, pubkey)
	}
}

type peerSnapshot struct {
	CandidateID string `json:"candidate_id"`
	PayloadJSON string `json:"payload_json"`
	NodeID      string `json:"node_id"`
	IngestedAt  int64  `json:"ingested_at"`
}

func (s *Syncer) syncPeer(instanceID, endpointURL, pubkey string) {
	since := s.lastSyncFor(instanceID)
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
		if _, err := s.DB.Exec(
			`INSERT OR REPLACE INTO federated_snapshots
			 (id, peer_instance_id, candidate_id, payload_preview, origin_node_id, origin_endpoint, ingested_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			fmt.Sprintf("fs_%s", snap.CandidateID),
			instanceID,
			snap.CandidateID,
			truncate(snap.PayloadJSON, 2048),
			snap.NodeID,
			endpointURL,
			snap.IngestedAt,
		); err != nil {
			s.Logger.Warn("sync: insert federated_snapshot failed", "error", err)
		}

		if _, err := s.DB.Exec(
			`INSERT OR REPLACE INTO fed_snapshots_fts (candidate_id, content) VALUES (?, ?)`,
			snap.CandidateID, truncate(snap.PayloadJSON, 2048),
		); err != nil {
			s.Logger.Warn("sync: insert fed_snapshots_fts failed", "error", err)
		}
	}

	if _, err := s.DB.Exec(`UPDATE federated_instances SET last_seen_at = ? WHERE id = ?`,
		time.Now().Unix(), instanceID); err != nil {
		s.Logger.Warn("sync: update last_seen failed", "error", err)
	}
}

func (s *Syncer) lastSyncFor(instanceID string) int64 {
	var ts int64
	s.DB.QueryRow(
		`SELECT coalesce(max(ingested_at), 0) FROM federated_snapshots WHERE peer_instance_id = ?`,
		instanceID,
	).Scan(&ts)
	return ts
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
