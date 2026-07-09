package repo

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

func GenerateUserCode() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	const charset = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	n := uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])
	var code [8]byte
	for i := 7; i >= 0; i-- {
		code[i] = charset[n%32]
		n /= 32
	}
	return string(code[:4]) + "-" + string(code[4:])
}

type DeviceSession struct {
	DeviceCodeHash string
	UserCode       string
	UserID         sql.NullString
	Status         string
	NodeID         sql.NullString
	CreatedAt      int64
	ExpiresAt      int64
	ApprovedAt     sql.NullInt64
	ConsumedAt     sql.NullInt64
	PollInterval   int
	LastPollAt     sql.NullInt64
}

func deviceCodeHash(deviceCode string) string {
	h := sha256.Sum256([]byte(deviceCode))
	return hex.EncodeToString(h[:])
}

func InsertDeviceSession(db *sql.DB, nodeType, endpointURL sql.NullString, publicKey []byte, ttl time.Duration) (deviceCode, userCode string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate device_code: %w", err)
	}
	deviceCode = hex.EncodeToString(raw)
	codeHash := deviceCodeHash(deviceCode)
	userCode = GenerateUserCode()
	now := time.Now().Unix()
	expiresAt := now + int64(ttl.Seconds())

	var nodeID sql.NullString
	if nodeType.Valid && endpointURL.Valid {
		id := NewID()
		_, err = db.Exec(
			`INSERT INTO nodes (id, user_id, node_type, endpoint_url, is_active, created_at, public_key)
			 VALUES (?, NULL, ?, ?, 1, ?, ?)`,
			id, nodeType.String, endpointURL.String, now, publicKey,
		)
		if err != nil {
			return "", "", fmt.Errorf("create node: %w", err)
		}
		nodeID = sql.NullString{String: id, Valid: true}
	}

	_, err = db.Exec(
		`INSERT INTO device_sessions (device_code_hash, user_code, node_id, status, created_at, expires_at, poll_interval)
		 VALUES (?, ?, ?, 'pending', ?, ?, 5)`,
		codeHash, userCode, nodeID, now, expiresAt,
	)
	if err != nil {
		return "", "", fmt.Errorf("insert device session: %w", err)
	}

	return deviceCode, userCode, nil
}

func DeviceSessionByCodeHash(db *sql.DB, codeHash string) (*DeviceSession, error) {
	var ds DeviceSession
	err := db.QueryRow(
		`SELECT device_code_hash, user_code, user_id, status, node_id, created_at, expires_at, approved_at, consumed_at, poll_interval, last_poll_at
		 FROM device_sessions WHERE device_code_hash = ?`, codeHash,
	).Scan(&ds.DeviceCodeHash, &ds.UserCode, &ds.UserID, &ds.Status, &ds.NodeID,
		&ds.CreatedAt, &ds.ExpiresAt, &ds.ApprovedAt, &ds.ConsumedAt, &ds.PollInterval, &ds.LastPollAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &ds, err
}

func DeviceSessionByUserCode(db *sql.DB, userCode string) (*DeviceSession, error) {
	var ds DeviceSession
	err := db.QueryRow(
		`SELECT device_code_hash, user_code, user_id, status, node_id, created_at, expires_at, approved_at, consumed_at, poll_interval, last_poll_at
		 FROM device_sessions WHERE user_code = ?`, userCode,
	).Scan(&ds.DeviceCodeHash, &ds.UserCode, &ds.UserID, &ds.Status, &ds.NodeID,
		&ds.CreatedAt, &ds.ExpiresAt, &ds.ApprovedAt, &ds.ConsumedAt, &ds.PollInterval, &ds.LastPollAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &ds, err
}

func ApproveDeviceSession(db *sql.DB, codeHash, userID string) error {
	now := time.Now().Unix()
	result, err := db.Exec(
		`UPDATE device_sessions SET status='approved', user_id=?, approved_at=?
		 WHERE device_code_hash=? AND status='pending' AND expires_at >= ?`,
		userID, now, codeHash, now,
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

func ConsumeDeviceSession(db *sql.DB, codeHash string) (*DeviceSession, error) {
	now := time.Now().Unix()

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	_, err = tx.Exec(
		`UPDATE device_sessions SET status='consumed', consumed_at=?, last_poll_at=?
		 WHERE device_code_hash=? AND status='approved'`,
		now, now, codeHash,
	)
	if err != nil {
		return nil, err
	}

	var ds DeviceSession
	err = tx.QueryRow(
		`SELECT device_code_hash, user_code, user_id, status, node_id, created_at, expires_at, approved_at, consumed_at, poll_interval, last_poll_at
		 FROM device_sessions WHERE device_code_hash=?`, codeHash,
	).Scan(&ds.DeviceCodeHash, &ds.UserCode, &ds.UserID, &ds.Status, &ds.NodeID,
		&ds.CreatedAt, &ds.ExpiresAt, &ds.ApprovedAt, &ds.ConsumedAt, &ds.PollInterval, &ds.LastPollAt)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &ds, nil
}

func UpdateLastPoll(db *sql.DB, codeHash string) error {
	_, err := db.Exec(`UPDATE device_sessions SET last_poll_at=? WHERE device_code_hash=?`,
		time.Now().Unix(), codeHash)
	return err
}
