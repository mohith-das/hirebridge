package store

import (
	"database/sql"
	"fmt"
	"log/slog"
)

func CreateVirtualTables(db *sql.DB, embedDim int, logger *slog.Logger) error {
	_, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS snapshots_fts USING fts5(
		candidate_id UNINDEXED,
		content,
		tokenize='porter unicode61'
	)`)
	if err != nil {
		return fmt.Errorf("create snapshots_fts: %w", err)
	}

	vecSQL := fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS candidate_vec USING vec0(
		candidate_id TEXT,
		embedding float[%d]
	)`, embedDim)

	_, err = db.Exec(vecSQL)
	if err != nil {
		logger.Warn("failed to create candidate_vec virtual table, semantic search disabled", "error", err)
	}

	return nil
}
