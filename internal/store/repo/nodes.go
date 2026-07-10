package repo

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"time"
)

func UpdateNodeToken(db *sql.DB, nodeID, tokenHash string) error {
	_, err := db.Exec(
		`UPDATE nodes SET node_token_hash = ? WHERE id = ?`,
		tokenHash, nodeID,
	)
	return err
}

func SetNodeUser(db *sql.DB, nodeID, userID string) error {
	_, err := db.Exec(
		`UPDATE nodes SET user_id = ? WHERE id = ? AND user_id IS NULL`,
		userID, nodeID,
	)
	return err
}

// RotateNodeIntroSecret generates a fresh 64-char hex intro_secret (32 random
// bytes) and persists it on the node row. Called when a node is reissued a
// token via the device-auth flow; rotating on re-issue makes any previously
// captured outbox payload un-signable.
func RotateNodeIntroSecret(db *sql.DB, nodeID string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	secret := hex.EncodeToString(b)
	if _, err := db.Exec(
		`UPDATE nodes SET intro_secret = ? WHERE id = ?`,
		secret, nodeID,
	); err != nil {
		return "", err
	}
	return secret, nil
}

// SetNodeIntroSecret persists an explicitly-supplied secret on the node row.
// Used by tests and by code paths that need to seed a known value.
func SetNodeIntroSecret(db *sql.DB, nodeID, secret string) error {
	_, err := db.Exec(
		`UPDATE nodes SET intro_secret = ? WHERE id = ?`,
		secret, nodeID,
	)
	return err
}

type Node struct {
	ID                string
	UserID            sql.NullString
	NodeType          string
	EndpointURL       string
	LastPingTimestamp sql.NullInt64
	IsActive          bool
	NodeTokenHash     sql.NullString
	PublicKey         []byte
	DisplayName       sql.NullString
	CreatedAt         sql.NullInt64
	RevokedAt         sql.NullInt64
	IntroSecret       sql.NullString
}

func NodesByUser(db *sql.DB, userID string) ([]Node, error) {
	rows, err := db.Query(
		`SELECT id, user_id, node_type, endpoint_url, last_ping_timestamp, is_active,
		        node_token_hash, public_key, display_name, created_at, revoked_at, intro_secret
		 FROM nodes WHERE user_id = ? ORDER BY created_at DESC`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []Node
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.ID, &n.UserID, &n.NodeType, &n.EndpointURL,
			&n.LastPingTimestamp, &n.IsActive, &n.NodeTokenHash, &n.PublicKey,
			&n.DisplayName, &n.CreatedAt, &n.RevokedAt, &n.IntroSecret); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

func RevokeNode(db *sql.DB, nodeID string) error {
	now := time.Now().Unix()

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT candidate_id FROM snapshots WHERE node_id = ?`, nodeID)
	if err != nil {
		return err
	}

	var candidateIDs []string
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			continue
		}
		candidateIDs = append(candidateIDs, cid)
	}
	rows.Close()

	for _, cid := range candidateIDs {
		tx.Exec(`DELETE FROM snapshots_fts WHERE candidate_id = ?`, cid)
		tx.Exec(`DELETE FROM candidate_vec WHERE candidate_id = ?`, cid)
	}

	_, err = tx.Exec(`DELETE FROM snapshots WHERE node_id = ?`, nodeID)
	if err != nil {
		return err
	}

	result, err := tx.Exec(
		`UPDATE nodes SET is_active = 0, revoked_at = ? WHERE id = ?`,
		now, nodeID,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}

	return tx.Commit()
}

func SetNodePublicKey(db *sql.DB, nodeID string, pub []byte) error {
	_, err := db.Exec(`UPDATE nodes SET public_key = ? WHERE id = ?`, pub, nodeID)
	return err
}

func UpdateNodePing(db *sql.DB, nodeID string) error {
	_, err := db.Exec(
		`UPDATE nodes SET last_ping_timestamp = ? WHERE id = ?`,
		time.Now().Unix(), nodeID,
	)
	return err
}

func NodeByID(db *sql.DB, nodeID string) (*Node, error) {
	var n Node
	err := db.QueryRow(
		`SELECT id, user_id, node_type, endpoint_url, last_ping_timestamp, is_active,
		        node_token_hash, public_key, display_name, created_at, revoked_at, intro_secret
		 FROM nodes WHERE id = ?`, nodeID,
	).Scan(&n.ID, &n.UserID, &n.NodeType, &n.EndpointURL,
		&n.LastPingTimestamp, &n.IsActive, &n.NodeTokenHash, &n.PublicKey,
		&n.DisplayName, &n.CreatedAt, &n.RevokedAt, &n.IntroSecret)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &n, err
}

// DeliveryTarget is a non-revoked node suitable for HMAC-signed introduction
// delivery: node_type='LivingCV' with non-empty endpoint_url AND intro_secret.
type DeliveryTarget struct {
	NodeID      string
	EndpointURL string
	IntroSecret string
}

// ResolveDeliveryTarget walks snapshot.node → user → active LivingCV node.
// Returns nil if no suitable target exists (the outbox marks the request
// undeliverable in that case).
func ResolveDeliveryTarget(db *sql.DB, snapshotNodeID string) (*DeliveryTarget, error) {
	var userID sql.NullString
	if err := db.QueryRow(
		`SELECT user_id FROM nodes WHERE id = ?`, snapshotNodeID,
	).Scan(&userID); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if !userID.Valid || userID.String == "" {
		return nil, nil
	}

	var target DeliveryTarget
	err := db.QueryRow(
		`SELECT id, endpoint_url, intro_secret
		 FROM nodes
		 WHERE user_id = ?
		   AND is_active = 1
		   AND revoked_at IS NULL
		   AND node_type = 'LivingCV'
		   AND endpoint_url != ''
		   AND (intro_secret IS NOT NULL AND intro_secret != '')
		 ORDER BY created_at DESC
		 LIMIT 1`,
		userID.String,
	).Scan(&target.NodeID, &target.EndpointURL, &target.IntroSecret)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &target, nil
}