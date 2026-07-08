package repo

import "database/sql"

type SearchHit struct {
	CandidateID string
	BM25Score   float64
	VecScore    float64
}

func FTS5Search(db *sql.DB, query string, limit int) ([]SearchHit, error) {
	rows, err := db.Query(
		`SELECT candidate_id, bm25(snapshots_fts, 0) AS rank
		 FROM snapshots_fts WHERE snapshots_fts MATCH ?
		 ORDER BY rank LIMIT ?`, query, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hits []SearchHit
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.CandidateID, &h.BM25Score); err != nil {
			continue
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

func Vec0Search(db *sql.DB, blob []byte, k int) ([]SearchHit, error) {
	rows, err := db.Query(
		`SELECT candidate_id, distance
		 FROM candidate_vec WHERE embedding MATCH ? AND k=?
		 ORDER BY distance`, blob, k,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hits []SearchHit
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.CandidateID, &h.VecScore); err != nil {
			continue
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

func SnapshotsByCandidateIDs(db *sql.DB, ids []string) (map[string]SnapshotInfo, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	query := `SELECT s.candidate_id, s.node_id, n.endpoint_url, n.display_name, n.is_active, s.ingested_at
		FROM snapshots s JOIN nodes n ON s.node_id = n.id
		WHERE s.candidate_id IN (` + placeholders(len(ids)) + `)`

	args := make([]interface{}, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]SnapshotInfo)
	for rows.Next() {
		var s SnapshotInfo
		var nn struct {
			endpoint   string
			dispName   sql.NullString
			isActive   bool
		}
		if err := rows.Scan(&s.CandidateID, &s.NodeID, &nn.endpoint, &nn.dispName, &nn.isActive, &s.IngestedAt); err != nil {
			continue
		}
		s.EndpointURL = nn.endpoint
		if nn.dispName.Valid {
			s.DisplayName = nn.dispName.String
		}
		s.IsActive = nn.isActive
		result[s.CandidateID] = s
	}
	return result, rows.Err()
}

func GetSnapshotByCandidate(db *sql.DB, candidateID string) (*SnapshotDetail, error) {
	var s SnapshotDetail
	err := db.QueryRow(
		`SELECT s.candidate_id, s.payload_json, s.signature, s.node_id,
		        n.endpoint_url, n.public_key, s.ingested_at
		 FROM snapshots s JOIN nodes n ON s.node_id = n.id
		 WHERE s.candidate_id = ?`, candidateID,
	).Scan(&s.CandidateID, &s.PayloadJSON, &s.Signature, &s.NodeID,
		&s.EndpointURL, &s.PublicKey, &s.IngestedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &s, err
}

type SnapshotInfo struct {
	CandidateID string
	NodeID      string
	EndpointURL string
	DisplayName string
	IsActive    bool
	IngestedAt  int64
}

type SnapshotDetail struct {
	CandidateID string
	PayloadJSON string
	Signature   []byte
	NodeID      string
	EndpointURL string
	PublicKey   []byte
	IngestedAt  int64
}

func placeholders(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			s += ","
		}
		s += "?"
	}
	return s
}
