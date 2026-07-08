package repo

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"time"
)

func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func CreateUser(db *sql.DB, email string) (string, error) {
	id := NewID()
	now := time.Now().Unix()

	var userID string
	err := db.QueryRow(
		`INSERT INTO users (id, email, created_at) VALUES (?, ?, ?)
		 ON CONFLICT(email) DO UPDATE SET email=excluded.email
		 RETURNING id`,
		id, email, now,
	).Scan(&userID)
	return userID, err
}
