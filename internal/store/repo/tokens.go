package repo

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"
)

func GenerateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

type MagicToken struct {
	TokenHash       string
	UserID          sql.NullString
	DeviceCodeHash  sql.NullString
	ExpiresAt       int64
	UsedAt          sql.NullInt64
}

func InsertMagicToken(db *sql.DB, token string, userID *string, deviceCodeHash *string, ttl time.Duration) error {
	hash := HashToken(token)
	expiresAt := time.Now().Add(ttl).Unix()

	_, err := db.Exec(
		`INSERT INTO magic_tokens (token_hash, user_id, device_code_hash, expires_at) VALUES (?, ?, ?, ?)`,
		hash, userID, deviceCodeHash, expiresAt,
	)
	return err
}

func ConsumeMagicToken(db *sql.DB, token string) (*MagicToken, error) {
	hash := HashToken(token)
	now := time.Now().Unix()

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var mt MagicToken
	err = tx.QueryRow(
		`SELECT token_hash, user_id, device_code_hash, expires_at, used_at
		 FROM magic_tokens WHERE token_hash = ?`, hash,
	).Scan(&mt.TokenHash, &mt.UserID, &mt.DeviceCodeHash, &mt.ExpiresAt, &mt.UsedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if mt.UsedAt.Valid || mt.ExpiresAt < now {
		return &mt, nil
	}

	_, err = tx.Exec(
		`UPDATE magic_tokens SET used_at = ? WHERE token_hash = ? AND used_at IS NULL`,
		now, hash,
	)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	mt.UsedAt = sql.NullInt64{Int64: now, Valid: true}
	return &mt, nil
}
