package repo

import (
	"database/sql"
	"time"
)

func InsertIntroductionRequest(db *sql.DB, id, candidateID, recruiterUserID, nodeID string) error {
	now := time.Now().Unix()
	_, err := db.Exec(
		`INSERT INTO introduction_requests (id, candidate_id, recruiter_user_id, node_id, status, created_at)
		 VALUES (?, ?, ?, ?, 'queued', ?)`,
		id, candidateID, recruiterUserID, nodeID, now,
	)
	return err
}
