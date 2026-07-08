package repo

import (
	"database/sql"
	"encoding/binary"
	"math"
	"time"
)

func UpsertSnapshot(db *sql.DB, id, nodeID, candidateID, payloadJSON string, signature []byte) error {
	now := time.Now().Unix()

	_, err := db.Exec(
		`INSERT INTO snapshots (id, node_id, candidate_id, payload_json, signature, ingested_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(node_id, candidate_id) DO UPDATE SET
		   payload_json=excluded.payload_json,
		   signature=excluded.signature,
		   ingested_at=excluded.ingested_at`,
		id, nodeID, candidateID, payloadJSON, signature, now,
	)
	return err
}

func ReplaceFTS5Row(db *sql.DB, candidateID, content string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`DELETE FROM snapshots_fts WHERE candidate_id = ?`, candidateID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`INSERT INTO snapshots_fts (candidate_id, content) VALUES (?, ?)`, candidateID, content)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func UpsertVec0Embedding(db *sql.DB, candidateID string, embedding [][]float64) error {
	if len(embedding) == 0 {
		return nil
	}

	blob := Float64ToBlob(embedding[0])

	_, err := db.Exec(
		`INSERT OR REPLACE INTO candidate_vec (candidate_id, embedding) VALUES (?, ?)`,
		candidateID, blob,
	)
	return err
}

func Float64ToBlob(vec []float64) []byte {
	buf := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(float32(v)))
	}
	return buf
}
