package repo

import (
	"database/sql"
	"time"
)

func InsertAuditLog(db *sql.DB, actorUserID, action, target string) error {
	id := NewID()
	ts := time.Now().Unix()
	_, err := db.Exec(
		`INSERT INTO audit_log (id, actor_user_id, action, target, ts) VALUES (?, ?, ?, ?, ?)`,
		id, actorUserID, action, target, ts,
	)
	return err
}
