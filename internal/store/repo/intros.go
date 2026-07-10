package repo

import (
	"database/sql"
	"errors"
	"time"
)

// IntroductionRequest mirrors a row in introduction_requests.
type IntroductionRequest struct {
	ID               string
	CandidateID      string
	RecruiterUserID  string
	NodeID           string
	Status           string
	CreatedAt        int64
	DeliveredAt      sql.NullInt64
	RecruiterName    string
	RecruiterEmail   string
	RecruiterCompany sql.NullString
	Attempts         int
	LastError        sql.NullString
	NextAttemptAt    sql.NullInt64
}

// ErrNotFound is returned when a lookup misses.
var ErrNotFound = errors.New("not found")

// InsertIntroductionRequest persists a fresh row in queued state. The outbox
// worker drains it asynchronously. Recruiter name + email are required;
// company is optional. next_attempt_at is left NULL so the new row is
// immediately eligible (PendingIntroductionRequests matches NULL).
func InsertIntroductionRequest(db *sql.DB, id, candidateID, recruiterUserID, nodeID, recruiterName, recruiterEmail, recruiterCompany string) error {
	now := time.Now().Unix()
	var company interface{}
	if recruiterCompany != "" {
		company = recruiterCompany
	}
	_, err := db.Exec(
		`INSERT INTO introduction_requests
		   (id, candidate_id, recruiter_user_id, node_id, status, created_at,
		    recruiter_name, recruiter_email, recruiter_company, attempts, next_attempt_at)
		 VALUES (?, ?, ?, ?, 'queued', ?, ?, ?, ?, 0, NULL)`,
		id, candidateID, recruiterUserID, nodeID, now,
		recruiterName, recruiterEmail, company,
	)
	return err
}

// IntroductionRequestByID fetches a single row by id.
func IntroductionRequestByID(db *sql.DB, id string) (*IntroductionRequest, error) {
	row := db.QueryRow(
		`SELECT id, candidate_id, recruiter_user_id, node_id, status, created_at,
		        delivered_at, recruiter_name, recruiter_email, recruiter_company,
		        attempts, last_error, next_attempt_at
		 FROM introduction_requests WHERE id = ?`, id,
	)
	var r IntroductionRequest
	err := row.Scan(
		&r.ID, &r.CandidateID, &r.RecruiterUserID, &r.NodeID, &r.Status, &r.CreatedAt,
		&r.DeliveredAt, &r.RecruiterName, &r.RecruiterEmail, &r.RecruiterCompany,
		&r.Attempts, &r.LastError, &r.NextAttemptAt,
	)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// PendingIntroductionRequests returns up to limit rows that are eligible for
// delivery: status IN ('queued','retrying'), and next_attempt_at IS NULL OR
// <= now. The outbox worker uses this as its primary drain query.
func PendingIntroductionRequests(db *sql.DB, now int64, limit int) ([]IntroductionRequest, error) {
	rows, err := db.Query(
		`SELECT id, candidate_id, recruiter_user_id, node_id, status, created_at,
		        delivered_at, recruiter_name, recruiter_email, recruiter_company,
		        attempts, last_error, next_attempt_at
		 FROM introduction_requests
		 WHERE status IN ('queued','retrying')
		   AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
		 ORDER BY created_at ASC
		 LIMIT ?`, now, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []IntroductionRequest
	for rows.Next() {
		var r IntroductionRequest
		if err := rows.Scan(
			&r.ID, &r.CandidateID, &r.RecruiterUserID, &r.NodeID, &r.Status, &r.CreatedAt,
			&r.DeliveredAt, &r.RecruiterName, &r.RecruiterEmail, &r.RecruiterCompany,
			&r.Attempts, &r.LastError, &r.NextAttemptAt,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkIntroDelivered transitions the row to 'delivered' and stamps delivered_at.
func MarkIntroDelivered(db *sql.DB, id string) error {
	now := time.Now().Unix()
	res, err := db.Exec(
		`UPDATE introduction_requests
		 SET status='delivered', delivered_at=?, last_error=NULL, next_attempt_at=NULL
		 WHERE id=? AND status IN ('queued','retrying')`,
		now, id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkIntroRetrying bumps attempts, records last_error, schedules next_attempt_at,
// and leaves the row in 'retrying' state so the worker picks it up again.
func MarkIntroRetrying(db *sql.DB, id, lastErr string, nextAttemptAt int64) error {
	res, err := db.Exec(
		`UPDATE introduction_requests
		 SET status='retrying', attempts=attempts+1, last_error=?, next_attempt_at=?
		 WHERE id=? AND status IN ('queued','retrying')`,
		lastErr, nextAttemptAt, id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkIntroFailed transitions the row to 'failed' (max attempts reached).
func MarkIntroFailed(db *sql.DB, id, lastErr string) error {
	res, err := db.Exec(
		`UPDATE introduction_requests
		 SET status='failed', attempts=attempts+1, last_error=?, next_attempt_at=NULL
		 WHERE id=? AND status IN ('queued','retrying')`,
		lastErr, id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkIntroUndeliverable transitions the row to 'undeliverable' (no target
// node) so the worker never picks it up again. status='undeliverable' is
// excluded from the PendingIntroductionRequests query.
func MarkIntroUndeliverable(db *sql.DB, id, reason string) error {
	res, err := db.Exec(
		`UPDATE introduction_requests
		 SET status='undeliverable', last_error=?, next_attempt_at=NULL
		 WHERE id=? AND status IN ('queued','retrying','delivered')`,
		reason, id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}