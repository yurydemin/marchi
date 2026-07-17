// Package db opens the SQLite database (FR-ST-02: WAL mode, the only
// database MailVault uses) and applies embedded schema migrations.
//
// This package does not itself enforce the Single Writer Pattern — that's
// internal/db/writer, built on top of the *sql.DB this package returns.
package db

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "modernc.org/sqlite"

	"github.com/yurydemin/marchi/internal/db/migrations"
)

// Open opens the SQLite database at path and applies any pending
// migrations. Pragmas (WAL mode, foreign keys, busy timeout) are set via
// DSN query parameters so every connection in the pool gets them uniformly
// — not just whichever connection happens to run first. This matters
// specifically for foreign_keys: SQLite defaults it to off per-connection,
// and missing it would silently leave orphaned rows on cascade deletes
// (FR-AM-06) depending on which pooled connection served the DELETE.
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: opening %s: %w", path, err)
	}
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("db: connecting to %s: %w", path, err)
	}

	if err := applyMigrations(sqlDB); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}

	return sqlDB, nil
}

// Close checkpoints the WAL (NFR-RL-05's "flush SQLite WAL" on graceful
// shutdown — folds the WAL file's contents back into the main database
// file and truncates it, rather than leaving pending writes for the next
// startup to replay) and then closes the database.
//
// A checkpoint failure doesn't prevent closing: an un-checkpointed WAL is
// still safely replayed the next time SQLite opens the database, so this
// returns the checkpoint error (for the caller to log) but always still
// calls sqlDB.Close() regardless.
func Close(sqlDB *sql.DB) error {
	_, checkpointErr := sqlDB.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	closeErr := sqlDB.Close()

	switch {
	case checkpointErr != nil && closeErr != nil:
		return fmt.Errorf("db: wal checkpoint failed (%v) and close also failed: %w", checkpointErr, closeErr)
	case checkpointErr != nil:
		return fmt.Errorf("db: wal checkpoint: %w", checkpointErr)
	case closeErr != nil:
		return fmt.Errorf("db: close: %w", closeErr)
	}
	return nil
}

func applyMigrations(sqlDB *sql.DB) error {
	driver, err := migratesqlite.WithInstance(sqlDB, &migratesqlite.Config{})
	if err != nil {
		return fmt.Errorf("db: creating migration driver: %w", err)
	}
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("db: reading embedded migrations: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "sqlite", driver)
	if err != nil {
		return fmt.Errorf("db: initializing migrator: %w", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("db: applying migrations: %w", err)
	}
	return nil
}
