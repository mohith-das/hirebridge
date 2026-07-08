package repo

import (
	"database/sql"
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
}

func NodesByUser(db *sql.DB, userID string) ([]Node, error) {
	rows, err := db.Query(
		`SELECT id, user_id, node_type, endpoint_url, last_ping_timestamp, is_active,
		        node_token_hash, public_key, display_name, created_at, revoked_at
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
			&n.DisplayName, &n.CreatedAt, &n.RevokedAt); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

func RevokeNode(db *sql.DB, nodeID string) error {
	now := time.Now().Unix()
	result, err := db.Exec(
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
	return nil
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
		        node_token_hash, public_key, display_name, created_at, revoked_at
		 FROM nodes WHERE id = ?`, nodeID,
	).Scan(&n.ID, &n.UserID, &n.NodeType, &n.EndpointURL,
		&n.LastPingTimestamp, &n.IsActive, &n.NodeTokenHash, &n.PublicKey,
		&n.DisplayName, &n.CreatedAt, &n.RevokedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &n, err
}
