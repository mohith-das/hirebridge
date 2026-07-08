package repo

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"
)

type APIToken struct {
	TokenHash  string
	UserID     string
	NodeID     sql.NullString
	Label      sql.NullString
	Scope      sql.NullString
	CreatedAt  int64
	LastUsedAt sql.NullInt64
	RevokedAt  sql.NullInt64
}

func CreateAPIToken(db *sql.DB, userID string, nodeID *string, label, scope string) (string, *APIToken, error) {
	token := GenerateToken()
	hash := HashToken(token)
	now := time.Now().Unix()

	var nid interface{}
	if nodeID != nil {
		nid = *nodeID
	}

	_, err := db.Exec(
		`INSERT INTO api_tokens (token_hash, user_id, node_id, label, scope, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		hash, userID, nid, label, scope, now,
	)
	if err != nil {
		return "", nil, err
	}

	at := &APIToken{
		TokenHash: hash,
		UserID:    userID,
		Label:     sql.NullString{String: label, Valid: label != ""},
		Scope:     sql.NullString{String: scope, Valid: scope != ""},
		CreatedAt: now,
	}
	if nodeID != nil {
		at.NodeID = sql.NullString{String: *nodeID, Valid: true}
	}

	return token, at, nil
}

func APITokenByHash(db *sql.DB, token string) (*APIToken, error) {
	hash := sha256Hex(token)

	var at APIToken
	err := db.QueryRow(
		`SELECT token_hash, user_id, node_id, label, scope, created_at, last_used_at, revoked_at
		 FROM api_tokens WHERE token_hash = ? AND revoked_at IS NULL`, hash,
	).Scan(&at.TokenHash, &at.UserID, &at.NodeID, &at.Label, &at.Scope,
		&at.CreatedAt, &at.LastUsedAt, &at.RevokedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &at, err
}

func TouchAPIToken(db *sql.DB, token string) error {
	hash := sha256Hex(token)
	_, err := db.Exec(`UPDATE api_tokens SET last_used_at=? WHERE token_hash=?`,
		time.Now().Unix(), hash)
	return err
}

func RevokeAPIToken(db *sql.DB, token string) error {
	hash := sha256Hex(token)
	_, err := db.Exec(`UPDATE api_tokens SET revoked_at=? WHERE token_hash=?`,
		time.Now().Unix(), hash)
	return err
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
