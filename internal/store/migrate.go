package store

import (
	"database/sql"
	"fmt"
	"io/fs"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

func RunMigrations(db *sql.DB, migrations fs.FS) error {
	src, err := iofs.New(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("iofs source: %w", err)
	}

	drv, err := sqlite3.WithInstance(db, &sqlite3.Config{})
	if err != nil {
		return fmt.Errorf("sqlite3 driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "sqlite3", drv)
	if err != nil {
		return fmt.Errorf("migrate instance: %w", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migrate up: %w", err)
	}

	return nil
}
