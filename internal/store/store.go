package store

import (
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/mattn/go-sqlite3"
)

const DriverName = "sqlite3_vec"

func RegisterDriver(vec0Path string, logger *slog.Logger) {
	sql.Register(DriverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			if err := conn.LoadExtension(vec0Path, ""); err != nil {
				logger.Warn("failed to load vec0 extension, semantic search disabled", "path", vec0Path, "error", err)
			}
			return nil
		},
	})
}

func Open(driverName, dbPath string) (*sql.DB, error) {
	db, err := sql.Open(driverName, dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
		"PRAGMA cache_size=-2000",
	}

	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("pragma %s: %w", p, err)
		}
	}

	return db, nil
}
