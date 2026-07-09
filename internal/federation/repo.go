package federation

import (
	"database/sql"
	"time"
)

// FederatedInstance is a row in federated_instances.
type FederatedInstance struct {
	ID           string
	Name         string
	EndpointURL  string
	PublicKey    string
	InstanceKey  string
	LastSeenAt   sql.NullInt64
	IsActive     bool
	CreatedAt    int64
	RevokedAt    sql.NullInt64
}

// ListFederatedInstances returns every peer (active, pending, or revoked)
// ordered by name. Used by the admin panel.
func ListFederatedInstances(db *sql.DB) ([]FederatedInstance, error) {
	rows, err := db.Query(
		`SELECT id, name, endpoint_url, public_key, instance_key,
		        last_seen_at, is_active, created_at, revoked_at
		 FROM federated_instances ORDER BY name`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []FederatedInstance
	for rows.Next() {
		var p FederatedInstance
		if err := rows.Scan(&p.ID, &p.Name, &p.EndpointURL, &p.PublicKey,
			&p.InstanceKey, &p.LastSeenAt, &p.IsActive,
			&p.CreatedAt, &p.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ApproveFederatedInstance marks a peer active (and clears revoked_at).
func ApproveFederatedInstance(db *sql.DB, id string) error {
	_, err := db.Exec(
		`UPDATE federated_instances
		 SET is_active = 1, revoked_at = NULL
		 WHERE id = ?`,
		id,
	)
	return err
}

// RevokeFederatedInstance marks a peer inactive and records the revocation
// timestamp. revoking flips is_active=0 only when the row exists; a missing
// id returns sql.ErrNoRows so the caller can respond 404.
func RevokeFederatedInstance(db *sql.DB, id string) error {
	now := time.Now().Unix()
	result, err := db.Exec(
		`UPDATE federated_instances
		 SET is_active = 0, revoked_at = ?
		 WHERE id = ?`,
		now, id,
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
