package repo

import (
	"database/sql"
	"time"
)

func InsertAuditLog(db *sql.DB, actorUserID, action, target string) {
	id := NewID()
	ts := time.Now().Unix()
	_, _ = db.Exec(
		`INSERT INTO audit_log (id, actor_user_id, action, target, ts) VALUES (?, ?, ?, ?, ?)`,
		id, actorUserID, action, target, ts,
	)
}
